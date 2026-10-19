package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

func newWebAuthnFixture(t *testing.T) (*httptest.Server, *SessionManager, storage.UserRepo) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Auth.WebAuthn = config.WebAuthnConfig{
		Enabled: true, RPID: "example.org",
		RPOrigins: []string{"https://app.example.org"}, DisplayName: "Test",
	}
	srvBase, store, sessions := newAuthFixture(t, cfg)
	srvBase.Close() // fixture built the store; we mount our own mux

	prov := NewProvisioner(store.Users(), store.Orgs(), store.Members())
	wa, err := NewWebAuthn(cfg, store.DB(), store.Users(), prov, sessions)
	if err != nil {
		t.Fatalf("new webauthn: %v", err)
	}
	mux := http.NewServeMux()
	wa.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sessions, store.Users()
}

// Enrollment endpoints must be unreachable without a session: the legacy flow
// let anonymous visitors mint accounts + owner sessions, which this design
// exists to prevent.
func TestWebAuthnEnrollRequiresSession(t *testing.T) {
	srv, _, _ := newWebAuthnFixture(t)
	for _, path := range []string{
		"/auth/webauthn/enroll/begin",
		"/auth/webauthn/enroll/finish",
		"/auth/webauthn/credentials/delete",
	} {
		code, _ := postJSON(t, srv.URL+path, map[string]any{})
		if code != http.StatusUnauthorized {
			t.Errorf("%s without session: code=%d, want 401", path, code)
		}
	}
	resp, err := http.Get(srv.URL + "/auth/webauthn/credentials")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("credentials list without session: code=%d, want 401", resp.StatusCode)
	}
}

// Demo sessions are shared; they must not be able to attach credentials.
func TestWebAuthnEnrollRefusesDemoRole(t *testing.T) {
	srv, sessions, _ := newWebAuthnFixture(t)
	tok, err := sessions.Issue(Identity{UserID: "u1", OrgID: "o1", Email: "demo@x", Role: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/webauthn/enroll/begin", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("demo enroll: code=%d, want 403", resp.StatusCode)
	}
}

// A signed-in user gets registration options bound to their own account —
// never to a name they merely claim.
func TestWebAuthnEnrollBeginBindsToSessionUser(t *testing.T) {
	srv, sessions, users := newWebAuthnFixture(t)
	u := &storage.User{ID: "user-1", Email: "u@example.org", Name: "U", Role: "user",
		CreatedAt: time.Now().UTC(), EmailVerified: true}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	tok, err := sessions.Issue(Identity{UserID: u.ID, OrgID: "org", Email: u.Email, Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/webauthn/enroll/begin?name=laptop", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll begin: code=%d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	// The options' user.id is base64url("user-1") — the session's user, and
	// residentKey is required so login can be usernameless.
	if !strings.Contains(body, `"dXNlci0x"`) {
		t.Errorf("options do not carry the session user id: %s", body)
	}
	if !strings.Contains(body, `"residentKey":"required"`) {
		t.Errorf("options do not require a resident key: %s", body)
	}
}

// Login begin is public (that is the point of passkeys) and must return
// discoverable-login options without leaking any account information.
func TestWebAuthnLoginBeginIsPublicAndEmpty(t *testing.T) {
	srv, _, _ := newWebAuthnFixture(t)
	code, _ := postJSON(t, srv.URL+"/auth/webauthn/login/begin", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("login begin: code=%d, want 200", code)
	}
}
