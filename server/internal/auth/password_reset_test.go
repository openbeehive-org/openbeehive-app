package auth

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// signIn signs in with a password and returns the response.
func (f *apiFixture) signIn(t *testing.T, email, password string) response {
	t.Helper()
	return f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": email, "password": password})
}

func TestChangePassword(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	a := f.signup(t, "a@test.dev")
	phone := f.signIn(t, "a@test.dev", "password123").str("token")
	change := func(token, current, next string) response {
		return f.call(t, http.MethodPost, "/auth/password/change", token, map[string]string{"current_password": current, "new_password": next})
	}

	wantError(t, change("", "password123", "new-password-1"), http.StatusUnauthorized, "not_signed_in")
	wantError(t, change(a.token, "not-my-password", "new-password-1"), http.StatusForbidden, "wrong_password")
	wantError(t, change(a.token, "", "new-password-1"), http.StatusForbidden, "wrong_password")
	wantError(t, change(a.token, "password123", "short"), http.StatusBadRequest, "invalid_password")
	wantError(t, change(a.token, "password123", strings.Repeat("x", 73)), http.StatusBadRequest, "password_too_long")
	if res := f.signIn(t, "a@test.dev", "password123"); res.status != http.StatusOK {
		t.Fatalf("a refused change must leave the password alone: %d %s", res.status, res.raw)
	}

	res := change(a.token, "password123", "new-password-1")
	if res.status != http.StatusOK || res.str("token") == "" || res.str("active_org") != a.orgID {
		t.Fatalf("change: %d %s", res.status, res.raw)
	}
	// The caller continues with the fresh token; every other session is over.
	if me := f.call(t, http.MethodGet, "/auth/me", res.str("token"), nil); me.status != http.StatusOK {
		t.Fatalf("the caller's new session: %d %s", me.status, me.raw)
	}
	for name, tok := range map[string]string{"the caller's old token": a.token, "another device": phone} {
		if me := f.call(t, http.MethodGet, "/auth/me", tok, nil); me.status != http.StatusUnauthorized {
			t.Errorf("%s still works after the password change: %d", name, me.status)
		}
	}
	wantError(t, f.signIn(t, "a@test.dev", "password123"), http.StatusUnauthorized, "invalid_credentials")
	if res := f.signIn(t, "a@test.dev", "new-password-1"); res.status != http.StatusOK {
		t.Fatalf("sign-in with the new password: %d %s", res.status, res.raw)
	}
}

func TestChangePasswordRefusals(t *testing.T) {
	cfg := loginConfig()
	cfg.Demo.Enabled, cfg.Demo.Email = true, "demo@test.dev"
	f := newAPIFixture(t, cfg)
	ctx := context.Background()
	body := map[string]string{"current_password": "password123", "new_password": "new-password-1"}

	// The instance admin's password comes from the configuration.
	admin := f.adminLogin(t)
	wantError(t, f.call(t, http.MethodPost, "/auth/password/change", admin.token, body), http.StatusForbidden, "password_managed")

	// An account that signs in with a provider or passkey has no password to confirm with.
	seedMember(t, f.store, "sso", "sso@test.dev", "sso-org", "owner")
	tok, err := f.sessions.Issue(Identity{UserID: "sso", OrgID: "sso-org", Email: "sso@test.dev", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Users().SetPassword(ctx, "sso", ""); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.call(t, http.MethodPost, "/auth/password/change", tok, body), http.StatusConflict, "no_password")

	// The shared demo account is read-only.
	seedMember(t, f.store, "demo", "demo@test.dev", "demo-org", "owner")
	demoTok, _ := f.sessions.Issue(Identity{UserID: "demo", OrgID: "demo-org", Email: "demo@test.dev", Role: "demo"})
	wantError(t, f.call(t, http.MethodPost, "/auth/password/change", demoTok, body), http.StatusForbidden, "demo_read_only")
}

// Guessing the current password with a stolen session runs into the same
// limit as guessing it on the sign-in page.
func TestChangePasswordIsThrottled(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	a := f.signup(t, "a@test.dev")
	for i := range throttlePerAccount {
		res := f.call(t, http.MethodPost, "/auth/password/change", a.token, map[string]string{"current_password": "guess", "new_password": "new-password-1"})
		if res.status != http.StatusForbidden {
			t.Fatalf("guess %d: %d %s", i, res.status, res.raw)
		}
	}
	res := f.call(t, http.MethodPost, "/auth/password/change", a.token, map[string]string{"current_password": "password123", "new_password": "new-password-1"})
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
}

var resetLinkPattern = regexp.MustCompile(`https://bees\.example\.org/reset-password\?token=([0-9a-f]{64})`)

// resetToken extracts the token from a reset link.
func resetToken(t *testing.T, text string) string {
	t.Helper()
	m := resetLinkPattern.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no reset link in %q", text)
	}
	return m[1]
}

func TestPasswordResetByEmail(t *testing.T) {
	var buf syncBuffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	f := newAPIFixture(t, mailConfig())
	a := f.signup(t, "a@test.dev")
	request := func(email string) response {
		return f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": email})
	}
	complete := func(token, password string) response {
		return f.call(t, http.MethodPost, "/auth/password/reset", "", map[string]string{"token": token, "new_password": password})
	}

	// Known and unknown addresses get the same answer.
	known, unknown := request(" A@Test.dev "), request("nobody@test.dev")
	if known.status != http.StatusOK || known.raw != unknown.raw || unknown.status != known.status {
		t.Fatalf("the answer must not depend on the account: %d %q vs %d %q", known.status, known.raw, unknown.status, unknown.raw)
	}
	mails := f.mails()
	if len(mails) != 1 || mails[0].to != "a@test.dev" {
		t.Fatalf("want one mail to the account, got %+v", mails)
	}
	token := resetToken(t, mails[0].body)

	// Only the hash is stored, with a one-hour expiry.
	var stored struct {
		Hash    string    `db:"token_hash"`
		Expires time.Time `db:"expires_at"`
	}
	if err := f.store.DB().Get(&stored, `SELECT token_hash, expires_at FROM password_reset`); err != nil {
		t.Fatal(err)
	}
	if stored.Hash == token || stored.Hash != hashKey(token) {
		t.Fatalf("the token must be stored hashed, got %q", stored.Hash)
	}
	if left := time.Until(stored.Expires); left < 59*time.Minute || left > 61*time.Minute {
		t.Fatalf("a reset link is valid for one hour, got %v", left)
	}

	// A password that is refused does not use the link up.
	wantError(t, complete(token, "short"), http.StatusBadRequest, "invalid_password")
	wantError(t, complete("not-a-token", "new-password-1"), http.StatusBadRequest, "reset_invalid")

	// A second link asked for meanwhile dies with the first one's use.
	request("a@test.dev")
	second := resetToken(t, f.mails()[1].body)

	if res := complete(token, "new-password-1"); res.status != http.StatusOK {
		t.Fatalf("reset: %d %s", res.status, res.raw)
	}
	wantError(t, complete(token, "another-password"), http.StatusBadRequest, "reset_invalid")
	wantError(t, complete(second, "another-password"), http.StatusBadRequest, "reset_invalid")

	// Every session from before the reset is over.
	if me := f.call(t, http.MethodGet, "/auth/me", a.token, nil); me.status != http.StatusUnauthorized {
		t.Fatalf("a session survived the reset: %d", me.status)
	}
	wantError(t, f.signIn(t, "a@test.dev", "password123"), http.StatusUnauthorized, "invalid_credentials")
	if res := f.signIn(t, "a@test.dev", "new-password-1"); res.status != http.StatusOK {
		t.Fatalf("sign-in with the new password: %d %s", res.status, res.raw)
	}

	for _, secret := range []string{token, second} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("a reset token was written to the log:\n%s", buf.String())
		}
	}
}

func TestPasswordResetLinkExpires(t *testing.T) {
	f := newAPIFixture(t, mailConfig())
	f.signup(t, "a@test.dev")
	f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": "a@test.dev"})
	token := resetToken(t, f.mails()[0].body)
	if _, err := f.store.DB().Exec(f.store.DB().Rebind(`UPDATE password_reset SET expires_at = ?`), time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	res := f.call(t, http.MethodPost, "/auth/password/reset", "", map[string]string{"token": token, "new_password": "new-password-1"})
	wantError(t, res, http.StatusBadRequest, "reset_invalid")
	if res := f.signIn(t, "a@test.dev", "password123"); res.status != http.StatusOK {
		t.Fatalf("an expired link must not change the password: %d", res.status)
	}
}

// Accounts whose password is not theirs to set get no link: the instance
// admin (configuration) and the demo account (shared).
func TestPasswordResetSkipsManagedAccounts(t *testing.T) {
	cfg := mailConfig()
	cfg.Demo.Enabled, cfg.Demo.Email = true, "demo@test.dev"
	f := newAPIFixture(t, cfg)
	seedMember(t, f.store, "demo", "demo@test.dev", "demo-org", "owner")
	for _, email := range []string{cfg.Auth.AdminEmail, "demo@test.dev"} {
		res := f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": email})
		if res.status != http.StatusOK {
			t.Fatalf("%s: %d %s", email, res.status, res.raw)
		}
	}
	if mails := f.mails(); len(mails) != 0 {
		t.Fatalf("no reset mail for managed accounts, got %+v", mails)
	}
}

func TestPasswordResetRequestsAreThrottled(t *testing.T) {
	f := newAPIFixture(t, mailConfig())
	f.signup(t, "a@test.dev")
	for i := range throttlePerAccount {
		if res := f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": "a@test.dev"}); res.status != http.StatusOK {
			t.Fatalf("request %d: %d %s", i, res.status, res.raw)
		}
	}
	res := f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": "A@test.dev"})
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
	if res.header.Get("Retry-After") == "" {
		t.Fatal("a throttled request must say when to retry")
	}
	if n := len(f.mails()); n != throttlePerAccount {
		t.Fatalf("want %d mails, got %d", throttlePerAccount, n)
	}
	// Asking for links does not lock the owner out of signing in.
	if res := f.signIn(t, "a@test.dev", "password123"); res.status != http.StatusOK {
		t.Fatalf("sign-in after many reset requests: %d %s", res.status, res.raw)
	}
}

func TestPasswordResetWithoutMailNeedsTheInstanceAdmin(t *testing.T) {
	f := newAPIFixture(t, loginConfig()) // no mail server
	owner := f.signup(t, "owner@test.dev")
	member := f.signup(t, "member@test.dev")
	f.join(t, owner.orgID, member, "member")
	admin := f.adminLogin(t)

	// Nobody can request a link by email, and the answer does not tell
	// accounts apart.
	known := f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": "member@test.dev"})
	unknown := f.call(t, http.MethodPost, "/auth/password/reset-request", "", map[string]string{"email": "nobody@test.dev"})
	wantError(t, known, http.StatusConflict, "reset_needs_admin")
	if known.raw != unknown.raw {
		t.Fatalf("the answer must not depend on the account: %q vs %q", known.raw, unknown.raw)
	}

	link := func(token, email string) response {
		return f.call(t, http.MethodPost, "/auth/admin/reset-link", token, map[string]string{"email": email})
	}
	wantError(t, link("", "member@test.dev"), http.StatusUnauthorized, "not_signed_in")
	// Owning the member's workspace is not enough.
	wantError(t, link(owner.token, "member@test.dev"), http.StatusForbidden, "admin_only")
	wantError(t, link(admin.token, "nobody@test.dev"), http.StatusNotFound, "account_not_found")
	wantError(t, link(admin.token, f.cfg.Auth.AdminEmail), http.StatusForbidden, "password_managed")

	res := link(admin.token, "Member@test.dev")
	if res.status != http.StatusOK {
		t.Fatalf("admin reset link: %d %s", res.status, res.raw)
	}
	token := resetToken(t, res.str("link"))
	if exp, err := time.Parse(time.RFC3339, res.str("expires_at")); err != nil || time.Until(exp) > 61*time.Minute || time.Until(exp) < 59*time.Minute {
		t.Fatalf("expires_at: %q %v", res.str("expires_at"), err)
	}
	if res := f.call(t, http.MethodPost, "/auth/password/reset", "", map[string]string{"token": token, "new_password": "from-the-admin"}); res.status != http.StatusOK {
		t.Fatalf("reset with the admin's link: %d %s", res.status, res.raw)
	}
	if me := f.call(t, http.MethodGet, "/auth/me", member.token, nil); me.status != http.StatusUnauthorized {
		t.Fatalf("a session survived the reset: %d", me.status)
	}
	if res := f.signIn(t, "member@test.dev", "from-the-admin"); res.status != http.StatusOK {
		t.Fatalf("sign-in with the new password: %d %s", res.status, res.raw)
	}
}

func TestInstanceSaysHowPasswordsAreReset(t *testing.T) {
	for name, tc := range map[string]struct {
		mail bool
		want string
	}{"with mail": {true, "email"}, "without mail": {false, "admin"}} {
		t.Run(name, func(t *testing.T) {
			cfg := loginConfig()
			if tc.mail {
				cfg = mailConfig()
			}
			f := newAPIFixture(t, cfg)
			if got := f.call(t, http.MethodGet, "/auth/instance", "", nil).str("password_reset"); got != tc.want {
				t.Fatalf("password_reset = %q, want %q", got, tc.want)
			}
		})
	}
	cfg := loginConfig()
	cfg.Auth.PasswordEnabled = false
	if got := passwordResetMode(cfg); got != "off" {
		t.Fatalf("no password sign-in: password_reset = %q, want off", got)
	}
}
