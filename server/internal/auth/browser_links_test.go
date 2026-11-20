package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// wantLoginRedirect asserts that a link opened in the browser ends on the
// sign-in page with the given error code, not on a JSON document.
func wantLoginRedirect(t *testing.T, res response, code string) {
	t.Helper()
	if res.status != http.StatusFound || res.header.Get("Location") != "/login?error="+code {
		t.Fatalf("want a redirect to /login?error=%s, got %d %q %s", code, res.status, res.header.Get("Location"), res.raw)
	}
	if strings.Contains(res.header.Get("Content-Type"), "json") {
		t.Fatalf("a browser link must not answer with JSON: %s", res.raw)
	}
}

func TestVerificationLinkErrorsLandOnTheSignInPage(t *testing.T) {
	cfg := mailConfig()
	cfg.Auth.EmailVerification = true
	f := newAPIFixture(t, cfg)

	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/verify?token=nope", "", nil), "verification_invalid")
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/verify", "", nil), "verification_invalid")

	if res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "new@test.dev", "password": "password123"}); res.str("status") != "verify" {
		t.Fatalf("signup: %d %s", res.status, res.raw)
	}
	u, err := f.store.Users().GetByEmail(context.Background(), "new@test.dev")
	if err != nil {
		t.Fatal(err)
	}
	res := f.call(t, http.MethodGet, "/auth/verify?token="+u.VerifyToken, "", nil)
	if res.status != http.StatusFound || res.header.Get("Location") != "/login?verified=1" {
		t.Fatalf("a good link: %d %q", res.status, res.header.Get("Location"))
	}
	// The link works once.
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/verify?token="+u.VerifyToken, "", nil), "verification_invalid")

	// A failing database is not shown to the visitor either.
	f.exec(t, `DROP TABLE users`)
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/verify?token=x", "", nil), "internal")
}

func TestProviderCallbackErrorsLandOnTheSignInPage(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
	rt := NewRouter(f.cfg)
	(&Manager{providers: map[string]*Provider{}}).Routes(rt, f.sessions, f.store.Users(), prov, f.cfg)
	srv := httptest.NewServer(rt)
	t.Cleanup(srv.Close)
	f.srv = srv

	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/login?provider=nope", "", nil), "unknown_provider")
	// The state cookie expired, or the callback was opened out of the blue.
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/callback?code=c&state=s", "", nil), "oidc_state_missing")
	cookie := oauthStateCookie + "=idp:expected"
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/callback?code=c&state=forged", "", nil, "Cookie", cookie), "oidc_state_invalid")
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/callback?code=c", "", nil, "Cookie", cookie), "oidc_state_invalid")
	// The provider did not confirm the sign-in (here: it is not configured).
	wantLoginRedirect(t, f.call(t, http.MethodGet, "/auth/callback?code=c&state=expected", "", nil, "Cookie", cookie), "oidc_failed")
}

func TestProvisioningRefusalsHaveCodes(t *testing.T) {
	for err, want := range map[error]string{
		errInviteOnly:                  "invite_only",
		errLinkRefused:                 "account_exists",
		errNoEmail:                     "oidc_no_email",
		errors.New("database is down"): "",
	} {
		if got := oidcErrorCode(err); got != want {
			t.Errorf("oidcErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
}

// One account per address, however it is capitalised and whichever way the
// account is created.
func TestEmailsAreStoredInNormalForm(t *testing.T) {
	cfg := loginConfig()
	f := newAPIFixture(t, cfg)
	ctx := context.Background()

	res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "  Mixed.Case@Test.DEV ", "password": "password123"})
	if res.status != http.StatusOK {
		t.Fatalf("signup: %d %s", res.status, res.raw)
	}
	u, err := f.store.Users().GetByID(ctx, res.str("user_id"))
	if err != nil || u.Email != "mixed.case@test.dev" {
		t.Fatalf("a sign-up is stored lower-case: %v %+v", err, u)
	}
	wantError(t, f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "MIXED.case@test.dev", "password": "password123"}), http.StatusConflict, "email_taken")
	if res := f.signIn(t, "MIXED.CASE@TEST.DEV", "password123"); res.status != http.StatusOK {
		t.Fatalf("sign-in ignores capitalisation: %d %s", res.status, res.raw)
	}

	// Whatever a caller hands the repository, it stores the normal form, and
	// the database refuses a second account for the address.
	direct := &storage.User{ID: "direct", Email: " Direct@Test.dev", Role: "user", CreatedAt: time.Now().UTC()}
	if err := f.store.Users().Create(ctx, direct); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Users().GetByEmail(ctx, "DIRECT@test.dev"); err != nil || got.ID != "direct" || got.Email != "direct@test.dev" {
		t.Fatalf("stored account: %v %+v", err, got)
	}
	twin := &storage.User{ID: "twin", Email: "DIRECT@TEST.DEV", Role: "user", CreatedAt: time.Now().UTC()}
	if err := f.store.Users().Create(ctx, twin); err == nil {
		t.Fatal("the database must refuse a second account that differs only by case")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM users WHERE email_key = ?`, "direct@test.dev"); n != 1 {
		t.Fatalf("accounts for the address: %d", n)
	}

	// The instance admin from the configuration and provider sign-ins follow
	// the same rule.
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
	sso, err := provisionOIDCUser(ctx, f.store.Users(), prov, cfg, "idp", &Claims{Subject: "s1", Email: "SSO@Test.dev", EmailVerified: true})
	if err != nil || sso.Email != "sso@test.dev" {
		t.Fatalf("provider account: %v %+v", err, sso)
	}
	cfg.Auth.AdminEmail = "Admin@Test.dev"
	if err := EnsureAdmin(ctx, f.store.Users(), cfg); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM users WHERE role = 'admin'`); n != 1 {
		t.Fatalf("a differently capitalised admin address must not create a second admin: %d", n)
	}
}
