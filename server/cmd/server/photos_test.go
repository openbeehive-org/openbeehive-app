package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	v1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	"github.com/openbeehive-org/openbeehive-app/server/internal/service"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql/sqltest"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// wired is the server as main assembles it (auth, RPC services, photo
// endpoints, the hardening wrapper), on a fresh database and blob directory.
type wired struct {
	t       *testing.T
	srv     *httptest.Server
	blobs   blob.Store
	blobDir string
}

func newWired(t *testing.T, cfg *config.Config) *wired {
	t.Helper()
	ctx := context.Background()
	cfg.DB.Driver, cfg.DB.DSN = sqltest.New(t)
	cfg.Blob.Backend = config.BlobFS
	cfg.Blob.BaseDir = filepath.Join(t.TempDir(), "blobs")
	store, err := sqlstore.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	blobs, err := blob.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hlc := wsync.NewHLC("test-server")
	photos := service.NewPhotos(store.DB(), hlc, blobs)
	mux := http.NewServeMux()
	interceptors := setupAuth(ctx, cfg, store, blobs, photos, mux)
	registerServices(t.Context(), mux, cfg, store, interceptors, hlc, photos)
	srv := httptest.NewServer(harden(cfg, mux))
	t.Cleanup(srv.Close)
	return &wired{t: t, srv: srv, blobs: blobs, blobDir: cfg.Blob.BaseDir}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("not JSON: %q", r.body)
	}
	return out
}

// do sends a request; headers come as key, value pairs.
func (w *wired) do(method, path string, body []byte, headers ...string) reply {
	w.t.Helper()
	req, err := http.NewRequest(method, w.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := w.srv.Client().Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	return reply{status: res.StatusCode, header: res.Header, body: data}
}

func (w *wired) postJSON(path, token string, body any) map[string]any {
	w.t.Helper()
	raw, _ := json.Marshal(body)
	headers := []string{"Content-Type", "application/json"}
	if token != "" {
		headers = append(headers, "Authorization", "Bearer "+token)
	}
	res := w.do(http.MethodPost, path, raw, headers...)
	if res.status != http.StatusOK {
		w.t.Fatalf("POST %s: %d %s", path, res.status, res.body)
	}
	return res.json(w.t)
}

type member struct{ token, org string }

func (w *wired) signup(email string) member {
	w.t.Helper()
	res := w.postJSON("/auth/signup", "", map[string]string{"email": email, "password": "password123"})
	return member{token: res["token"].(string), org: res["active_org"].(string)}
}

func (w *wired) apiKey(m member, scope string) string {
	w.t.Helper()
	return w.postJSON("/auth/api-keys", m.token, map[string]any{"name": scope, "scope": scope})["token"].(string)
}

// withToken sends every request of the RPC client with the session token.
type withToken struct {
	token string
	next  http.RoundTripper
}

func (w withToken) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if w.token != "" {
		r.Header.Set("Authorization", "Bearer "+w.token)
	}
	return w.next.RoundTrip(r)
}

// rpc returns an RPC connection that acts as the member.
func (w *wired) rpc(m member) *connect.Client {
	client := &http.Client{Transport: withToken{token: m.token, next: w.srv.Client().Transport}}
	return connect.NewClient(connecthttp.NewTransport(client, w.srv.URL))
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newPhoto creates an apiary, a hive and an inspection through the API and
// pushes a photo record for the inspection, like a device does. It returns
// the photo id.
func (w *wired) newPhoto(m member, id string) string {
	w.t.Helper()
	ctx := context.Background()
	conn := w.rpc(m)
	apiary, err := openbeehivev1connect.NewApiaryServiceClient(conn).CreateApiary(ctx, &v1.CreateApiaryRequest{Name: "Home"})
	if err != nil {
		w.t.Fatalf("create apiary: %v", err)
	}
	hive, err := openbeehivev1connect.NewHiveServiceClient(conn).CreateHive(ctx, &v1.CreateHiveRequest{ApiaryId: apiary.Apiary.Id, Name: "H1"})
	if err != nil {
		w.t.Fatalf("create hive: %v", err)
	}
	visit, err := openbeehivev1connect.NewInspectionServiceClient(conn).CreateInspection(ctx, &v1.CreateInspectionRequest{HiveId: hive.Hive.Id})
	if err != nil {
		w.t.Fatalf("create inspection: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"owner_entity": "inspection", "owner_id": visit.Inspection.Id, "deleted": 0})
	res, err := openbeehivev1connect.NewSyncServiceClient(conn).Push(ctx, &v1.PushRequest{Changes: []*v1.Change{{
		Entity: "photo", EntityId: id, ScopeId: apiary.Apiary.Id, Op: v1.ChangeOp_CHANGE_OP_UPSERT,
		PayloadJson: string(payload), Hlc: wsync.NewHLC("device").Now(),
	}}})
	if err != nil || len(res.Rejected) != 0 {
		w.t.Fatalf("push photo: %v %v", err, res.GetRejected())
	}
	return id
}

func loginConfig() *config.Config {
	cfg := &config.Config{Profile: "selfhost", PublicBaseURL: "https://bees.example.org"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.RegistrationOpen = true
	cfg.Auth.SessionSecret = strings.Repeat("s", config.MinSessionSecretBytes)
	cfg.Auth.SessionTTL = time.Hour
	return cfg
}

// The transfer endpoints behind the real authentication: sessions (bearer
// and cookie), API keys with both scopes, and the cross-site rule.
func TestPhotoTransferWithRealCredentials(t *testing.T) {
	w := newWired(t, loginConfig())
	owner, stranger := w.signup("owner@example.org"), w.signup("stranger@example.org")
	writeKey, readKey := w.apiKey(owner, "write"), w.apiKey(owner, "read")
	photo := w.newPhoto(owner, "photo-1")
	other := w.newPhoto(owner, "photo-2")
	full := testJPEG(t, 320, 240)
	auth := func(token string) []string { return []string{"Authorization", "Bearer " + token} }

	// Uploads: refused without credentials, for a non-member, for a read-only key.
	for name, c := range map[string]struct {
		headers []string
		status  int
		code    string
	}{
		"no credentials":        {nil, 401, "not_signed_in"},
		"garbage token":         {auth("not-a-token"), 401, "not_signed_in"},
		"member of another org": {auth(stranger.token), 404, "unknown_photo"},
		"read-only key":         {auth(readKey), 403, "read_only"},
		"cookie, cross-site":    {[]string{"Cookie", "obh_session=" + owner.token, "Sec-Fetch-Site", "cross-site"}, 403, "cross_site_request"},
		"cookie, other origin":  {[]string{"Cookie", "obh_session=" + owner.token, "Origin", "https://evil.example"}, 403, "cross_site_request"},
	} {
		res := w.do(http.MethodPost, "/photos/"+photo, full, c.headers...)
		if res.status != c.status || res.json(t)["code"] != c.code {
			t.Errorf("upload with %s: %d %s, want %d %s", name, res.status, res.body, c.status, c.code)
		}
	}
	if keys := w.keys(); len(keys) != 0 {
		t.Fatalf("a refused upload was stored: %v", keys)
	}

	// Accepted: a session, and a write key.
	if res := w.do(http.MethodPost, "/photos/"+photo, full, auth(owner.token)...); res.status != http.StatusCreated {
		t.Fatalf("upload with a session: %d %s", res.status, res.body)
	}
	if res := w.do(http.MethodPost, "/photos/"+other, full, auth(writeKey)...); res.status != http.StatusCreated {
		t.Fatalf("upload with a write key: %d %s", res.status, res.body)
	}
	for _, key := range w.keys() {
		if !strings.HasPrefix(key, blob.WorkspacePrefix(owner.org)+"photos/") {
			t.Errorf("blob key %q is not under the workspace prefix", key)
		}
	}

	// Downloads: members by session (bearer or cookie) and by either key.
	for name, headers := range map[string][]string{
		"session":   auth(owner.token),
		"cookie":    {"Cookie", "obh_session=" + owner.token},
		"write key": auth(writeKey),
		"read key":  auth(readKey),
	} {
		res := w.do(http.MethodGet, "/photos/"+photo, nil, headers...)
		if res.status != http.StatusOK || !bytes.Equal(res.body, full) {
			t.Errorf("download with %s: %d, %d bytes", name, res.status, len(res.body))
		}
		// Every response carries the server-wide headers; this one must also
		// be typed from the stored bytes and kept out of shared caches.
		if res.header.Get("X-Content-Type-Options") != "nosniff" || res.header.Get("Content-Type") != "image/jpeg" ||
			!strings.HasPrefix(res.header.Get("Cache-Control"), "private") {
			t.Errorf("download with %s: headers %v", name, res.header)
		}
	}
	for name, headers := range map[string][]string{"no credentials": nil, "member of another org": auth(stranger.token)} {
		res := w.do(http.MethodGet, "/photos/"+photo, nil, headers...)
		if res.status == http.StatusOK || bytes.Contains(res.body, full[:20]) {
			t.Errorf("download with %s: %d", name, res.status)
		}
	}

	// The blob directory is not served anywhere.
	key := w.keys()[0]
	for _, path := range []string{"/files/" + key, "/files/", "/" + key, "/photos/" + key} {
		if res := w.do(http.MethodGet, path, nil, auth(owner.token)...); res.status == http.StatusOK {
			t.Errorf("GET %s answered 200", path)
		}
	}
}

func (w *wired) keys() []string {
	w.t.Helper()
	var keys []string
	if err := w.blobs.List(context.Background(), "", func(o blob.Object) error {
		keys = append(keys, o.Key)
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	return keys
}

// Without login the endpoints act as the local user, and still refuse what
// another site makes the browser send.
func TestPhotoTransferWithoutLogin(t *testing.T) {
	w := newWired(t, &config.Config{Profile: "selfhost", PublicBaseURL: "http://localhost:8080"})
	photo := w.newPhoto(member{}, "photo-1")
	full := testJPEG(t, 64, 64)
	if res := w.do(http.MethodPost, "/photos/"+photo, full, "Sec-Fetch-Site", "cross-site"); res.status != http.StatusForbidden {
		t.Fatalf("cross-site upload: %d", res.status)
	}
	if res := w.do(http.MethodPost, "/photos/"+photo, full); res.status != http.StatusCreated {
		t.Fatalf("upload: %d %s", res.status, res.body)
	}
	if res := w.do(http.MethodGet, "/photos/"+photo, nil); res.status != http.StatusOK || !bytes.Equal(res.body, full) {
		t.Fatalf("download: %d", res.status)
	}
	if keys := w.keys(); len(keys) != 1 || !strings.HasPrefix(keys[0], "local/photos/") {
		t.Fatalf("blob keys: %v", keys)
	}
}
