package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
)

// apiFixture is a server with every plain-HTTP auth and workspace endpoint
// mounted the way main does, on a fresh SQLite database.
type apiFixture struct {
	srv      *httptest.Server
	store    *sqlstore.Store
	sessions *SessionManager
	keys     *APIKeys
	cfg      *config.Config
}

// loginConfig is a password-auth instance with open registration.
func loginConfig() *config.Config {
	cfg := &config.Config{Profile: "selfhost", PublicBaseURL: "https://bees.example.org"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.RegistrationOpen = true
	cfg.Auth.AdminEmail = "admin@test.dev"
	cfg.Auth.AdminPassword = "password123"
	return cfg
}

func newAPIFixture(t *testing.T, cfg *config.Config) *apiFixture {
	t.Helper()
	cfg.DB.Driver = config.DriverSQLite
	cfg.DB.DSN = "file:" + filepath.Join(t.TempDir(), "api.db")
	store, err := sqlstore.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if cfg.Auth.AdminEmail != "" {
		if err := EnsureAdmin(ctx, store.Users(), cfg); err != nil {
			t.Fatalf("ensure admin: %v", err)
		}
	}
	sessions, err := NewSessionManager(testSecret, time.Hour, store.Users(), store.Members())
	if err != nil {
		t.Fatal(err)
	}
	prov := NewProvisioner(store.Users(), store.Orgs(), store.Members())
	throttle := NewThrottle(cfg.TrustProxyHeaders)
	rt := NewRouter(cfg)
	rt.Get("/auth/instance", InstanceHandler(cfg))
	NewTenantAPI(sessions, store.Users(), store.Orgs(), store.Members(), store.Invites(), prov, cfg, nil).Routes(rt)
	NewPasswordAuth(store.Users(), store.Invites(), sessions, cfg, prov, throttle).Routes(rt)
	sessions.Routes(rt)
	keys := NewAPIKeys(store.DB(), sessions, store.Users(), store.Members())
	keys.Routes(rt)
	srv := httptest.NewServer(rt)
	t.Cleanup(srv.Close)
	return &apiFixture{srv: srv, store: store, sessions: sessions, keys: keys, cfg: cfg}
}

type account struct {
	token, userID, orgID, email string
}

// signup registers an account and returns its session and personal workspace.
func (f *apiFixture) signup(t *testing.T, email string) account {
	t.Helper()
	res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": email, "password": "password123"})
	if res.status != http.StatusOK {
		t.Fatalf("signup %s: %d %v", email, res.status, res.body)
	}
	return account{token: res.str("token"), userID: res.str("user_id"), orgID: res.str("active_org"), email: email}
}

type response struct {
	status int
	body   map[string]any
	raw    string
	header http.Header
}

func (r response) str(key string) string {
	s, _ := r.body[key].(string)
	return s
}

// call sends a request the way the app does: JSON body, bearer session.
// Extra headers come as key, value pairs; "Content-Type" with an empty value
// removes the default.
func (f *apiFixture) call(t *testing.T, method, path, token string, body any, headers ...string) response {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, raw: string(raw), header: resp.Header, body: map[string]any{}}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// wantError asserts the error contract: the status, a JSON body with the
// given code and a non-empty message.
func wantError(t *testing.T, res response, status int, code string) {
	t.Helper()
	if res.status != status || res.str("code") != code || res.str("error") == "" {
		t.Fatalf("want %d %q with a message, got %d %s", status, code, res.status, res.raw)
	}
	if ct := res.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("error content type %q, want application/json", ct)
	}
}
