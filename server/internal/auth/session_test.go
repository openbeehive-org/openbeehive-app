package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

func TestSessionManagerRefusesShortSecrets(t *testing.T) {
	for _, secret := range []string{"", "short", strings.Repeat("x", config.MinSessionSecretBytes-1)} {
		if _, err := NewSessionManager(secret, time.Hour, nil, nil); err == nil {
			t.Errorf("a %d-byte secret must be refused", len(secret))
		}
	}
	if _, err := NewSessionManager(strings.Repeat("x", config.MinSessionSecretBytes), time.Hour, nil, nil); err != nil {
		t.Errorf("a %d-byte secret must be accepted: %v", config.MinSessionSecretBytes, err)
	}
}

func TestLogoutAllEndsEveryOtherSession(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	a := f.signup(t, "a@test.dev")
	signin := func() string {
		res := f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": "a@test.dev", "password": "password123"})
		if res.status != http.StatusOK {
			t.Fatalf("signin: %d %s", res.status, res.raw)
		}
		return res.str("token")
	}
	phone, laptop := signin(), signin()
	other := f.signup(t, "b@test.dev")

	// Plain logout only clears the cookie; the token stays usable.
	if res := f.call(t, http.MethodPost, "/auth/logout", phone, nil); res.status != http.StatusNoContent ||
		!strings.Contains(res.header.Get("Set-Cookie"), SessionCookie+"=;") {
		t.Fatalf("logout: %d %v", res.status, res.header)
	}

	res := f.call(t, http.MethodPost, "/auth/logout-all", laptop, nil)
	if res.status != http.StatusOK || res.str("token") == "" || res.str("token") == laptop {
		t.Fatalf("logout-all: %d %s", res.status, res.raw)
	}
	if !strings.Contains(res.header.Get("Set-Cookie"), SessionCookie+"="+res.str("token")) {
		t.Fatalf("logout-all must hand the caller a fresh cookie: %v", res.header)
	}
	fresh := res.str("token")
	for name, tok := range map[string]string{"signup session": a.token, "phone": phone, "the caller's old token": laptop} {
		wantError(t, f.call(t, http.MethodGet, "/auth/me", tok, nil), http.StatusUnauthorized, "not_signed_in")
		if _, err := f.sessions.Verify(context.Background(), tok); !errors.Is(err, errSessionRevoked) {
			t.Errorf("%s: want errSessionRevoked, got %v", name, err)
		}
	}
	if res := f.call(t, http.MethodGet, "/auth/me", fresh, nil); res.status != http.StatusOK {
		t.Fatalf("the caller's fresh session: %d %s", res.status, res.raw)
	}
	// Other accounts are untouched, and signing in again works.
	if res := f.call(t, http.MethodGet, "/auth/me", other.token, nil); res.status != http.StatusOK {
		t.Fatalf("another account's session: %d", res.status)
	}
	time.Sleep(2 * time.Millisecond) // a sign-in in the revocation's own millisecond is refused by design
	if res := f.call(t, http.MethodGet, "/auth/me", signin(), nil); res.status != http.StatusOK {
		t.Fatalf("sign-in after logout-all: %d", res.status)
	}
	wantError(t, f.call(t, http.MethodPost, "/auth/logout-all", "", nil), http.StatusUnauthorized, "not_signed_in")
}

func TestLogoutAllRefusedForTheSharedDemoAccount(t *testing.T) {
	cfg := loginConfig()
	cfg.Demo = config.DemoConfig{Enabled: true, Email: "demo@test.dev"}
	f := newAPIFixture(t, cfg)
	seedMember(t, f.store, "demo-user", "demo@test.dev", "demo-org", "owner")
	res := f.call(t, http.MethodPost, "/auth/demo-login", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("demo login: %d %s", res.status, res.raw)
	}
	wantError(t, f.call(t, http.MethodPost, "/auth/logout-all", res.str("token"), nil), http.StatusForbidden, "demo_read_only")
}

// A session is only as good as the membership behind it: removing a member
// (or the account) must cut off the HTTP endpoints and the RPCs.
func TestSessionEndsWithMembershipAndAccount(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	u := f.signup(t, "u@test.dev")
	// What the RPC interceptor does with the request's credentials.
	rpc := func(token string) error {
		_, err := verifyHeaders(ctx, f.keys, headerWith("Authorization", "Bearer "+token))
		return err
	}
	if err := rpc(u.token); err != nil {
		t.Fatalf("member RPC: %v", err)
	}

	if _, err := f.store.DB().Exec(f.store.DB().Rebind(
		`DELETE FROM member WHERE organization_id = ? AND benutzer_id = ?`), u.orgID, u.userID); err != nil {
		t.Fatal(err)
	}
	// Whoever removes a member invalidates the cached answer (see
	// TestSessionStateCacheExpires for the case where nobody does).
	f.sessions.Invalidate()
	wantError(t, f.call(t, http.MethodGet, "/auth/me", u.token, nil), http.StatusUnauthorized, "not_signed_in")
	if err := rpc(u.token); !errors.Is(err, errNotMember) {
		t.Fatalf("removed member RPC: want errNotMember, got %v", err)
	}

	// Signing in again resolves a workspace the account does belong to.
	res := f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": "u@test.dev", "password": "password123"})
	if res.status != http.StatusOK || res.str("active_org") == u.orgID {
		t.Fatalf("signin after removal: %d %s", res.status, res.raw)
	}
	again := res.str("token")
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(`DELETE FROM users WHERE id = ?`), u.userID); err != nil {
		t.Fatal(err)
	}
	f.sessions.Invalidate()
	if _, err := f.sessions.Verify(ctx, again); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("session of a deleted account: %v", err)
	}
}

func headerWith(key, value string) *connect.Header {
	h := connect.Header{}
	h.Set(key, value)
	return &h
}

// The membership answer is cached briefly; without an invalidation it must
// still be re-read once the window has passed.
func TestSessionStateCacheExpires(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	u := f.signup(t, "u@test.dev")
	if _, err := f.sessions.Verify(ctx, u.token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(
		`DELETE FROM member WHERE organization_id = ? AND benutzer_id = ?`), u.orgID, u.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.Verify(ctx, u.token); err != nil {
		t.Fatalf("inside the cache window the cached answer is used: %v", err)
	}
	// Age the cached entry instead of sleeping through the window.
	f.sessions.mu.Lock()
	for k, st := range f.sessions.state {
		st.fetched = st.fetched.Add(-sessionStateTTL - time.Second)
		f.sessions.state[k] = st
	}
	f.sessions.mu.Unlock()
	if _, err := f.sessions.Verify(ctx, u.token); !errors.Is(err, errNotMember) {
		t.Fatalf("after the cache window: want errNotMember, got %v", err)
	}
}

// Tokens issued by 0.4.x servers carry no issue time. They keep working
// (installed apps stay signed in across the upgrade) until the account's
// sessions are revoked for the first time.
func TestTokensWithoutIssueTimeWorkUntilFirstRevocation(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	ctx := context.Background()
	u := f.signup(t, "u@test.dev")
	payload, _ := json.Marshal(map[string]any{
		"uid": u.userID, "org": u.orgID, "email": u.email, "role": "user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	body := base64.RawURLEncoding.EncodeToString(payload)
	untimed := body + "." + f.sessions.sign(body)

	if id, err := f.sessions.Verify(ctx, untimed); err != nil || id.UserID != u.userID {
		t.Fatalf("token without issue time, before any revocation: %v", err)
	}
	if _, err := f.sessions.RevokeAll(ctx, Identity{UserID: u.userID, OrgID: u.orgID, Email: u.email, Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.Verify(ctx, untimed); !errors.Is(err, errSessionRevoked) {
		t.Fatalf("token without issue time, after revocation: %v", err)
	}
}

// Database trouble while checking a session is "try again", never "signed
// out": the app must not throw the session away because of a hiccup.
func TestSessionCheckFailureIsNotASignOut(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	u := f.signup(t, "u@test.dev")
	f.sessions.Invalidate()
	if _, err := f.store.DB().Exec(`ALTER TABLE users RENAME TO users_gone`); err != nil {
		t.Fatal(err)
	}
	res := f.call(t, http.MethodGet, "/auth/me", u.token, nil)
	wantError(t, res, http.StatusServiceUnavailable, "unavailable")
	if strings.Contains(strings.ToLower(res.raw), "sql") || strings.Contains(res.raw, "users") {
		t.Fatalf("database error leaked: %s", res.raw)
	}

	// The RPC side maps the same error to Unavailable
	// (TestSessionInterceptorOverHTTP).
	if _, err := f.keys.Verify(context.Background(), u.token); !errors.Is(err, errSessionUnverifiable) {
		t.Fatalf("want errSessionUnverifiable, got %v", err)
	}
}

func TestSessionOfAnotherSecretIsRejected(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	u := f.signup(t, "u@test.dev")
	forger, err := NewSessionManager(strings.Repeat("z", 32), time.Hour, f.store.Users(), f.store.Members())
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := forger.Issue(Identity{UserID: u.userID, OrgID: u.orgID, Email: u.email, Role: "admin"})
	wantError(t, f.call(t, http.MethodGet, "/auth/me", forged, nil), http.StatusUnauthorized, "not_signed_in")
}
