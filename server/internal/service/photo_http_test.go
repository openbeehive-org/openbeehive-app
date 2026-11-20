package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

// caller is who a test request claims to be. The handler under test gets its
// identity from a callback; here that callback trusts these fields. Real
// sessions and API keys are covered by the wiring test in cmd/server.
type caller struct {
	user, org, role string
	readOnly        bool
	unavailable     bool // the session store cannot be reached
	crossSite       bool // the request comes from another site
}

var (
	userA  = caller{user: "user-a", org: "org-a", role: "user"}
	userB  = caller{user: "user-b", org: "org-b", role: "user"}
	nobody = caller{}
)

func (f *photoFixture) handler() http.Handler {
	identify := func(r *http.Request) (auth.Identity, int) {
		switch {
		case r.Header.Get("X-Test-Unavailable") != "":
			return auth.Identity{}, http.StatusServiceUnavailable
		case r.Header.Get("X-Test-User") == "":
			return auth.Identity{}, http.StatusUnauthorized
		}
		return auth.Identity{
			UserID: r.Header.Get("X-Test-User"), OrgID: r.Header.Get("X-Test-Org"),
			Role: r.Header.Get("X-Test-Role"), ReadOnly: r.Header.Get("X-Test-Read-Only") != "",
		}, 0
	}
	sameSite := func(r *http.Request) bool { return r.Header.Get("X-Test-Cross-Site") == "" }
	mux := http.NewServeMux()
	mux.Handle(PhotoPathPrefix, f.photos.Handler(identify, sameSite))
	return mux
}

type transferResult struct {
	status int
	header http.Header
	body   []byte
}

func (r transferResult) code(t *testing.T) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("response is not the JSON error shape: %q", r.body)
	}
	return out.Code
}

// transfer sends one request to the photo endpoints. path is the raw request
// path, so tests can send what a browser would never produce.
func (f *photoFixture) transfer(t *testing.T, method, path string, who caller, body []byte) transferResult {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// What a client says about the body is never what decides.
	req.Header.Set("Content-Type", "image/jpeg")
	set := func(name string, on bool, value string) {
		if on {
			req.Header.Set(name, value)
		}
	}
	set("X-Test-User", who.user != "", who.user)
	set("X-Test-Org", who.org != "", who.org)
	set("X-Test-Role", who.role != "", who.role)
	set("X-Test-Read-Only", who.readOnly, "1")
	set("X-Test-Unavailable", who.unavailable, "1")
	set("X-Test-Cross-Site", who.crossSite, "1")
	// Redirects are reported, not followed: the mux answers a path that is
	// not in canonical form with a redirect to the one that is.
	client := *srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return transferResult{status: res.StatusCode, header: res.Header, body: data}
}

// upload stores the full-size image of a photo as tenant A's user.
func (f *photoFixture) upload(t *testing.T, photoID string, data []byte) {
	t.Helper()
	if res := f.transfer(t, http.MethodPost, PhotoPathPrefix+photoID, userA, data); res.status != http.StatusCreated {
		t.Fatalf("upload %s: %d %s", photoID, res.status, res.body)
	}
}

// newPhoto pushes a photo record of a fresh inspection as tenant A.
func (f *photoFixture) newPhoto(t *testing.T, id string) {
	t.Helper()
	if err := push(f.sync, ctxA, change("photo", id, "apiary-a", photoPayload(t, ownerInspection, f.newVisit(t)))); err != nil {
		t.Fatalf("push photo: %v", err)
	}
}

func TestUploadAndDownload(t *testing.T) {
	f := newPhotoFixture(t)
	f.newPhoto(t, "photo-1")
	full := jpegBytes(t, 640, 480, 3)

	// Before the upload there is nothing to fetch.
	if res := f.transfer(t, http.MethodGet, "/photos/photo-1", userA, nil); res.status != http.StatusNotFound || res.code(t) != "not_uploaded" {
		t.Fatalf("download before upload: %d %s", res.status, res.body)
	}

	res := f.transfer(t, http.MethodPost, "/photos/photo-1", userA, full)
	if res.status != http.StatusCreated {
		t.Fatalf("upload: %d %s", res.status, res.body)
	}
	row := f.photo(t, "photo-1")
	if row.BlobKey != blob.PhotoKey("org-a", "photo-1") || row.BlobType != "image/jpeg" || row.BlobSize != int64(len(full)) {
		t.Fatalf("row after upload: %+v", row)
	}
	if got := f.blob(t, row.BlobKey); !bytes.Equal(got, full) {
		t.Fatal("stored bytes differ from the upload")
	}

	res = f.transfer(t, http.MethodGet, "/photos/photo-1", userA, nil)
	if res.status != http.StatusOK || !bytes.Equal(res.body, full) {
		t.Fatalf("download: %d, %d bytes", res.status, len(res.body))
	}
	for name, want := range map[string]string{
		"Content-Type":                 "image/jpeg",
		"Cache-Control":                "private, max-age=31536000, immutable",
		"Vary":                         "Authorization, Cookie",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Content-Security-Policy":      "default-src 'none'; sandbox",
		"Content-Disposition":          `inline; filename="photo.jpg"`,
	} {
		if got := res.header.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if head := f.transfer(t, http.MethodHead, "/photos/photo-1", userA, nil); head.status != http.StatusOK ||
		len(head.body) != 0 || head.header.Get("Content-Length") != itoa(int64(len(full))) {
		t.Errorf("HEAD: %d, %d body bytes, length %q", head.status, len(head.body), head.header.Get("Content-Length"))
	}

	// A repeated upload is acknowledged, but what is stored stays.
	res = f.transfer(t, http.MethodPost, "/photos/photo-1", userA, jpegBytes(t, 10, 10, 9))
	if res.status != http.StatusOK {
		t.Fatalf("repeated upload: %d %s", res.status, res.body)
	}
	if got := f.blob(t, row.BlobKey); !bytes.Equal(got, full) {
		t.Fatal("a repeated upload replaced the image")
	}
}

// The stored content type is the one found in the bytes, whatever the
// request claims; PNG and WebP are accepted too.
func TestUploadTypeIsSniffed(t *testing.T) {
	f := newPhotoFixture(t)
	for id, c := range map[string]struct {
		data []byte
		want string
		ext  string
	}{
		"photo-png":  {pngBytes(t, 32, 32, 1), "image/png", ".png"},
		"photo-webp": {webpBytes(), "image/webp", ".webp"},
	} {
		f.newPhoto(t, id)
		f.upload(t, id, c.data) // sent as image/jpeg
		res := f.transfer(t, http.MethodGet, "/photos/"+id, userA, nil)
		if res.header.Get("Content-Type") != c.want || !strings.HasSuffix(res.header.Get("Content-Disposition"), c.ext+`"`) {
			t.Errorf("%s: served as %q / %q", id, res.header.Get("Content-Type"), res.header.Get("Content-Disposition"))
		}
	}

	f.newPhoto(t, "photo-x")
	for name, data := range map[string][]byte{
		"html":           []byte("<html><script>alert(1)</script></html>"),
		"svg":            []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"gif":            []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"text":           []byte("just some text"),
		"jpeg-like text": []byte("JFIF pretending"),
		"pdf":            []byte("%PDF-1.7"),
	} {
		res := f.transfer(t, http.MethodPost, "/photos/photo-x", userA, data)
		if res.status != http.StatusUnsupportedMediaType || res.code(t) != "unsupported_type" {
			t.Errorf("%s: %d %s, want 415", name, res.status, res.body)
		}
	}
	if res := f.transfer(t, http.MethodPost, "/photos/photo-x", userA, nil); res.status != http.StatusBadRequest {
		t.Errorf("empty body: %d, want 400", res.status)
	}
	if row := f.photo(t, "photo-x"); row.BlobKey != "" || len(f.blobKeys(t)) != 2 {
		t.Fatalf("a refused upload was stored: %+v, blobs %v", row, f.blobKeys(t))
	}
}

func TestUploadSizeLimit(t *testing.T) {
	f := newPhotoFixture(t)
	f.newPhoto(t, "photo-1")
	jpegOf := func(size int) []byte {
		head := jpegBytes(t, 16, 16, 1)
		return append(head, bytes.Repeat([]byte{0}, size-len(head))...)
	}
	// One byte over: refused by its declared length, and also when the
	// length is not declared.
	over := jpegOf(MaxPhotoBytes + 1)
	if res := f.transfer(t, http.MethodPost, "/photos/photo-1", userA, over); res.status != http.StatusRequestEntityTooLarge || res.code(t) != "too_large" {
		t.Fatalf("over the cap: %d %s", res.status, res.body)
	}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/photos/photo-1", io.MultiReader(bytes.NewReader(over)))
	req.Header.Set("X-Test-User", "user-a")
	req.Header.Set("X-Test-Org", "org-a")
	res, err := srv.Client().Do(req) // chunked: no Content-Length
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the cap without a declared length: %d", res.StatusCode)
	}
	if len(f.blobKeys(t)) != 0 {
		t.Fatal("an oversized upload was stored")
	}
	// Exactly the cap passes.
	f.upload(t, "photo-1", jpegOf(MaxPhotoBytes))
	if row := f.photo(t, "photo-1"); row.BlobSize != MaxPhotoBytes {
		t.Fatalf("stored size %d", row.BlobSize)
	}
}

// Who may do what: members of the photo's workspace read, members with
// write access upload, nobody else learns that the photo exists.
func TestTransferAuthorization(t *testing.T) {
	f := newPhotoFixture(t)
	f.newPhoto(t, "photo-1")
	f.newPhoto(t, "photo-2")
	full := jpegBytes(t, 64, 64, 1)
	f.upload(t, "photo-1", full)

	demo := caller{user: "demo", org: "org-a", role: "demo"}
	readKey := caller{user: "user-a", org: "org-a", role: "user", readOnly: true}
	admin := caller{user: "root", org: "org-b", role: "admin"} // instance admin, but of another workspace
	down := caller{user: "user-a", org: "org-a", unavailable: true}
	crossSite := caller{user: "user-a", org: "org-a", role: "user", crossSite: true}

	type want struct {
		status int
		code   string
	}
	for _, c := range []struct {
		name   string
		who    caller
		get    want // of the uploaded photo-1
		upload want // to photo-2
	}{
		{"not signed in", nobody, want{401, "not_signed_in"}, want{401, "not_signed_in"}},
		{"member of another workspace", userB, want{404, "not_found"}, want{404, "unknown_photo"}},
		{"instance admin outside the workspace", admin, want{404, "not_found"}, want{404, "unknown_photo"}},
		{"demo account", demo, want{200, ""}, want{403, "demo_read_only"}},
		{"read-only API key", readKey, want{200, ""}, want{403, "read_only"}},
		{"session store down", down, want{503, "unavailable"}, want{503, "unavailable"}},
		{"cross-site request", crossSite, want{200, ""}, want{403, "cross_site_request"}},
	} {
		get := f.transfer(t, http.MethodGet, "/photos/photo-1", c.who, nil)
		if get.status != c.get.status || (c.get.code != "" && get.code(t) != c.get.code) {
			t.Errorf("%s: GET %d %s, want %d %s", c.name, get.status, get.body, c.get.status, c.get.code)
		}
		if c.get.status == 200 && !bytes.Equal(get.body, full) {
			t.Errorf("%s: GET returned other bytes", c.name)
		}
		if c.get.status != 200 && (bytes.Contains(get.body, full[:16]) || get.header.Get("Cache-Control") != "no-store") {
			t.Errorf("%s: a refusal must carry no image and must not be cached (%q)", c.name, get.header.Get("Cache-Control"))
		}
		up := f.transfer(t, http.MethodPost, "/photos/photo-2", c.who, full)
		if up.status != c.upload.status || up.code(t) != c.upload.code {
			t.Errorf("%s: POST %d %s, want %d %s", c.name, up.status, up.body, c.upload.status, c.upload.code)
		}
	}
	if row := f.photo(t, "photo-2"); row.BlobKey != "" || len(f.blobKeys(t)) != 1 {
		t.Fatalf("a refused upload was stored: %+v, blobs %v", row, f.blobKeys(t))
	}
	// A writer of the workspace who did not take the photo may upload it.
	other := caller{user: "user-a2", org: "org-a", role: "user"}
	if res := f.transfer(t, http.MethodPost, "/photos/photo-2", other, full); res.status != http.StatusCreated {
		t.Fatalf("upload by another member: %d %s", res.status, res.body)
	}
}

func TestTransferOfUnknownAndDeletedPhotos(t *testing.T) {
	f := newPhotoFixture(t)
	full := jpegBytes(t, 64, 64, 1)
	// No record yet: the device retries after its sync.
	if res := f.transfer(t, http.MethodPost, "/photos/never-synced", userA, full); res.status != http.StatusNotFound || res.code(t) != "unknown_photo" {
		t.Fatalf("upload without a record: %d %s", res.status, res.body)
	}
	f.newPhoto(t, "photo-1")
	f.upload(t, "photo-1", full)
	f.newPhoto(t, "photo-2")
	if err := push(f.sync, ctxA, photoDeletion("photo-1"), photoDeletion("photo-2")); err != nil {
		t.Fatal(err)
	}
	// Deleted: gone for readers at once, even before the blob is removed.
	if res := f.transfer(t, http.MethodGet, "/photos/photo-1", userA, nil); res.status != http.StatusNotFound {
		t.Fatalf("download of a deleted photo: %d", res.status)
	}
	// An upload that arrives after the delete is refused and leaves nothing.
	if res := f.transfer(t, http.MethodPost, "/photos/photo-2", userA, full); res.status != http.StatusGone || res.code(t) != "deleted" {
		t.Fatalf("upload to a deleted photo: %d %s", res.status, res.body)
	}
	if err := f.photos.removeDeletedBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if keys := f.blobKeys(t); len(keys) != 0 {
		t.Fatalf("blobs left after the deletes: %v", keys)
	}
}

// The id in the path only ever selects a photo row. It never becomes part of
// a file name, so no spelling of it reaches outside the blob directory.
func TestTransferPathSafety(t *testing.T) {
	f := newPhotoFixture(t)
	f.newPhoto(t, "photo-1")
	full := jpegBytes(t, 64, 64, 1)
	f.upload(t, "photo-1", full)
	outside := filepath.Dir(f.blobDir)
	before := fileCount(t, outside)

	for _, path := range []string{
		"/photos/", "/photos", "/photos/../photos/photo-1", "/photos/..%2f..%2fetc%2fpasswd", "/photos/%2e%2e/%2e%2e/x",
		"/photos/a/b", "/photos/photo-1/", "/photos/photo-1/extra", "/photos//photo-1", "/photos/..", "/photos/.",
		"/photos/" + strings.Repeat("a", 300), "/photos/org-b%2fphotos%2fx", `/photos/..\..\x`, "/photos/%00",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			res := f.transfer(t, method, path, userA, full)
			if res.status == http.StatusOK || res.status == http.StatusCreated || res.status >= 500 {
				t.Errorf("%s %s: %d %.60s", method, path, res.status, res.body)
			}
		}
	}
	if got := fileCount(t, outside); got != before {
		t.Fatalf("files below %s: %d, was %d", outside, got, before)
	}
	if keys := f.blobKeys(t); len(keys) != 1 || keys[0] != blob.PhotoKey("org-a", "photo-1") {
		t.Fatalf("blobs: %v", keys)
	}

	// A photo id may itself look like a path: it still maps to one safe key.
	odd := `..%2F..%2Fescape`
	if err := push(f.sync, ctxA, change("photo", `../../escape`, "apiary-a", photoPayload(t, ownerInspection, f.newVisit(t)))); err != nil {
		t.Fatal(err)
	}
	if res := f.transfer(t, http.MethodPost, "/photos/"+odd, userA, full); res.status != http.StatusNotFound {
		t.Fatalf("an id with a slash is not addressable: %d", res.status)
	}
	if got := fileCount(t, outside); got != before {
		t.Fatalf("files below %s: %d, was %d", outside, got, before)
	}

	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if res := f.transfer(t, method, "/photos/photo-1", userA, full); res.status != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d, want 405", method, res.status)
		}
	}
}
