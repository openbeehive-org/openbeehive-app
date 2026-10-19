package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
)

func newAuthFixture(t *testing.T, cfg *config.Config) (*httptest.Server, *sqlstore.Store, *SessionManager) {
	t.Helper()
	cfg.DB.Driver = config.DriverSQLite
	cfg.DB.DSN = "file:" + filepath.Join(t.TempDir(), "auth.db")
	store, err := sqlstore.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Mirror the boot sequence: the dedicated admin exists before any request.
	if cfg.Auth.AdminEmail != "" {
		if err := EnsureAdmin(context.Background(), store.Users(), cfg); err != nil {
			t.Fatalf("ensure admin: %v", err)
		}
	}
	sessions := NewSessionManager("test-secret", time.Hour)
	prov := NewProvisioner(store.Users(), store.Orgs(), store.Members())
	mux := http.NewServeMux()
	NewPasswordAuth(store.Users(), store.Invites(), sessions, cfg, prov).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store, sessions
}

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSignupInviteOnly(t *testing.T) {
	cfg := &config.Config{Profile: "selfhost"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.RegistrationOpen = false
	cfg.Auth.AdminEmail = "admin@test.dev"
	cfg.Auth.AdminPassword = "password123"
	srv, store, _ := newAuthFixture(t, cfg)

	// No first-run exception: the admin exists from boot, so sign-up without an
	// invite is always rejected on an invite-only instance.
	code, j := postJSON(t, srv.URL+"/auth/signup",
		map[string]string{"email": "stranger@test.dev", "password": "password123"})
	if code != http.StatusForbidden || j["status"] != "invite_only" {
		t.Fatalf("stranger signup: code=%d resp=%v", code, j)
	}

	// A valid invite for the matching email passes.
	inv := &storage.Invite{ID: "inv1", OrgID: "org1", Email: "friend@test.dev",
		Role: "member", Token: "tok-friend", CreatedAt: time.Now().UTC()}
	if err := store.Invites().Create(context.Background(), inv); err != nil {
		t.Fatalf("invite create: %v", err)
	}
	code, j = postJSON(t, srv.URL+"/auth/signup", map[string]string{
		"email": "other@test.dev", "password": "password123", "invite": "tok-friend"})
	if code != http.StatusForbidden {
		t.Fatalf("invite with wrong email must be rejected: code=%d resp=%v", code, j)
	}
	code, j = postJSON(t, srv.URL+"/auth/signup", map[string]string{
		"email": "friend@test.dev", "password": "password123", "invite": "tok-friend"})
	if code != http.StatusOK {
		t.Fatalf("invited signup: code=%d resp=%v", code, j)
	}
	if j["admin"] == true {
		t.Fatalf("sign-up must never grant the admin role: resp=%v", j)
	}

	// The bootstrapped admin signs in with the configured password.
	code, j = postJSON(t, srv.URL+"/auth/signin",
		map[string]string{"email": "admin@test.dev", "password": "password123"})
	if code != http.StatusOK || j["admin"] != true {
		t.Fatalf("admin signin: code=%d resp=%v", code, j)
	}
}

// TestDemoCoexistsWithRegistration: a demo instance with open registration
// accepts normal sign-ups; only the demo email itself is reserved.
func TestDemoCoexistsWithRegistration(t *testing.T) {
	cfg := &config.Config{Profile: "selfhost"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.RegistrationOpen = true
	cfg.Auth.AdminEmail = "admin@test.dev"
	cfg.Auth.AdminPassword = "password123"
	cfg.Demo = config.DemoConfig{Enabled: true, Email: "demo@test.dev", Password: "demo"}
	srv, store, _ := newAuthFixture(t, cfg)

	code, j := postJSON(t, srv.URL+"/auth/signup",
		map[string]string{"email": "beekeeper@test.dev", "password": "password123"})
	if code != http.StatusOK {
		t.Fatalf("signup with demo enabled: code=%d resp=%v", code, j)
	}
	u, err := store.Users().GetByEmail(context.Background(), "beekeeper@test.dev")
	if err != nil || u.Role != "user" {
		t.Fatalf("signed-up account: err=%v role=%q, want user", err, u.Role)
	}

	// The demo email is seeded, never claimable via sign-up.
	code, j = postJSON(t, srv.URL+"/auth/signup",
		map[string]string{"email": "demo@test.dev", "password": "password123"})
	if code != http.StatusForbidden {
		t.Fatalf("demo email signup must be forbidden: code=%d resp=%v", code, j)
	}
}

// TestDemoCoexistsWithInvites: demo on, registration closed; invites still
// work (the combination used by a production instance with a public demo).
func TestDemoCoexistsWithInvites(t *testing.T) {
	cfg := &config.Config{Profile: "selfhost"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.RegistrationOpen = false
	cfg.Auth.AdminEmail = "admin@test.dev"
	cfg.Auth.AdminPassword = "password123"
	cfg.Demo = config.DemoConfig{Enabled: true, Email: "demo@test.dev", Password: "demo"}
	srv, store, _ := newAuthFixture(t, cfg)

	code, j := postJSON(t, srv.URL+"/auth/signup",
		map[string]string{"email": "stranger@test.dev", "password": "password123"})
	if code != http.StatusForbidden || j["status"] != "invite_only" {
		t.Fatalf("uninvited signup: code=%d resp=%v", code, j)
	}

	inv := &storage.Invite{ID: "inv1", OrgID: "org1", Email: "friend@test.dev",
		Role: "member", Token: "tok-friend", CreatedAt: time.Now().UTC()}
	if err := store.Invites().Create(context.Background(), inv); err != nil {
		t.Fatalf("invite create: %v", err)
	}
	code, j = postJSON(t, srv.URL+"/auth/signup", map[string]string{
		"email": "friend@test.dev", "password": "password123", "invite": "tok-friend"})
	if code != http.StatusOK {
		t.Fatalf("invited signup with demo enabled: code=%d resp=%v", code, j)
	}
}

// TestEnsureAdmin: the env-defined admin is created at boot, repaired when
// tampered with, and its password follows the env value (recovery path).
func TestEnsureAdmin(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Profile: "selfhost"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.AdminEmail = "admin@test.dev"
	cfg.Auth.AdminPassword = "password123"
	srv, store, _ := newAuthFixture(t, cfg) // fixture runs EnsureAdmin once

	u, err := store.Users().GetByEmail(ctx, "admin@test.dev")
	if err != nil || u.Role != "admin" || !u.EmailVerified {
		t.Fatalf("bootstrapped admin: err=%v role=%q verified=%v", err, u.Role, u.EmailVerified)
	}

	// Re-running is idempotent (same account, no duplicate).
	if err := EnsureAdmin(ctx, store.Users(), cfg); err != nil {
		t.Fatalf("ensure admin (2nd): %v", err)
	}
	if again, err := store.Users().GetByEmail(ctx, "admin@test.dev"); err != nil || again.ID != u.ID {
		t.Fatalf("admin not stable across boots: err=%v id=%q want %q", err, again.ID, u.ID)
	}

	// A changed env password wins on the next boot.
	cfg.Auth.AdminPassword = "rotated-secret"
	if err := EnsureAdmin(ctx, store.Users(), cfg); err != nil {
		t.Fatalf("ensure admin (rotate): %v", err)
	}
	code, _ := postJSON(t, srv.URL+"/auth/signin",
		map[string]string{"email": "admin@test.dev", "password": "password123"})
	if code != http.StatusUnauthorized {
		t.Fatalf("old password must stop working: code=%d", code)
	}
	code, j := postJSON(t, srv.URL+"/auth/signin",
		map[string]string{"email": "admin@test.dev", "password": "rotated-secret"})
	if code != http.StatusOK || j["admin"] != true {
		t.Fatalf("rotated password signin: code=%d resp=%v", code, j)
	}

	// A demoted role is forced back to admin.
	if err := store.Users().SetCredentials(ctx, u.ID, u.PasswordHash, "user"); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if err := EnsureAdmin(ctx, store.Users(), cfg); err != nil {
		t.Fatalf("ensure admin (repair): %v", err)
	}
	if u, _ = store.Users().GetByEmail(ctx, "admin@test.dev"); u.Role != "admin" {
		t.Fatalf("role not repaired: %q", u.Role)
	}
}

func TestDemoSessionIsMarkedReadOnly(t *testing.T) {
	cfg := &config.Config{Profile: "selfhost"}
	cfg.Auth.PasswordEnabled = true
	cfg.Auth.RegistrationOpen = true
	cfg.Demo = config.DemoConfig{Enabled: true, Email: "demo@test.dev", Password: "demo"}
	srv, store, sessions := newAuthFixture(t, cfg)

	hash, _ := bcrypt.GenerateFromPassword([]byte("demo1234"), bcrypt.DefaultCost)
	u := &storage.User{ID: "demo-user", Email: "demo@test.dev", Name: "Demo",
		Role: "user", PasswordHash: string(hash), EmailVerified: true, CreatedAt: time.Now().UTC()}
	if err := store.Users().Create(context.Background(), u); err != nil {
		t.Fatalf("user create: %v", err)
	}

	for path, body := range map[string]map[string]string{
		"/auth/demo-login": {},
		"/auth/signin":     {"email": "demo@test.dev", "password": "demo1234"},
	} {
		code, j := postJSON(t, srv.URL+path, body)
		if code != http.StatusOK {
			t.Fatalf("%s: code=%d resp=%v", path, code, j)
		}
		tok, _ := j["token"].(string)
		id, err := sessions.Verify(context.Background(), tok)
		if err != nil {
			t.Fatalf("%s: verify: %v", path, err)
		}
		if id.Role != "demo" {
			t.Fatalf("%s: session role = %q, want demo", path, id.Role)
		}
	}
}

func TestReadOnlyMethodAllowList(t *testing.T) {
	reads := []string{
		"/openbeehive.v1.SyncService/Pull",
		"/openbeehive.v1.SyncService/Subscribe",
		"/openbeehive.v1.ApiaryService/ListApiaries",
		"/openbeehive.v1.ApiaryService/GetApiary",
	}
	writes := []string{
		"/openbeehive.v1.SyncService/Push",
		"/openbeehive.v1.ApiaryService/CreateApiary",
		"/openbeehive.v1.ApiaryService/UpdateApiary",
		"/openbeehive.v1.ApiaryService/DeleteApiary",
	}
	for _, p := range reads {
		if !readOnlyMethod(p) {
			t.Errorf("%s should be read-only", p)
		}
	}
	for _, p := range writes {
		if readOnlyMethod(p) {
			t.Errorf("%s must count as a write", p)
		}
	}
}
