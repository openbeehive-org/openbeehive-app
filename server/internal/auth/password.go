package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// PasswordAuth implements email + password onboarding:
//   - sign-up always creates a regular user; the instance admin is the
//     dedicated account from BEEHIVE_ADMIN_* (see EnsureAdmin),
//   - open registration or invite-only is a per-instance switch
//     (BEEHIVE_REGISTRATION), independent of the demo,
//   - email verification is optional, gated by BEEHIVE_EMAIL_VERIFICATION.
type PasswordAuth struct {
	users    storage.UserRepo
	invites  storage.InviteRepo
	resets   ResetStore
	sessions *SessionManager
	cfg      *config.Config
	prov     *Provisioner
	throttle *Throttle
	// mail delivers one message; replaced in tests.
	mail func(to, subject, body string) error
	// background counts the goroutines that send mail after the response.
	background sync.WaitGroup
}

func NewPasswordAuth(users storage.UserRepo, invites storage.InviteRepo, resets ResetStore, sessions *SessionManager, cfg *config.Config, prov *Provisioner, throttle *Throttle) *PasswordAuth {
	p := &PasswordAuth{users: users, invites: invites, resets: resets, sessions: sessions, cfg: cfg, prov: prov, throttle: throttle}
	p.mail = func(to, subject, body string) error { return sendMail(cfg, to, subject, body) }
	return p
}

// Routes registers the onboarding endpoints.
func (p *PasswordAuth) Routes(rt *Router) {
	rt.PostJSON("/auth/signup", p.signup)
	rt.PostJSON("/auth/signin", p.signin)
	// Opened from the verification email, so it is a GET from another site.
	rt.Get("/auth/verify", p.verify)
	rt.PostJSON("/auth/password/change", p.changePassword)
	rt.PostJSON("/auth/password/reset-request", p.requestReset)
	rt.PostJSON("/auth/password/reset", p.completeReset)
	rt.PostJSON("/auth/admin/reset-link", p.adminResetLink)
	if p.cfg.Demo.Enabled {
		rt.Post("/auth/demo-login", p.demoLogin)
	}
}

// demoLogin signs the visitor in as the demo account (no password needed). Only
// registered when the demo is enabled.
func (p *PasswordAuth) demoLogin(w http.ResponseWriter, r *http.Request) {
	u, err := p.users.GetByEmail(r.Context(), p.cfg.Demo.Email)
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, "demo_unavailable", "the demo is not available right now")
		return
	}
	org := p.prov.ResolveActiveOrg(r.Context(), u)
	tok, ok := p.issue(w, u, org)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": u.Name, "token": tok, "user_id": u.ID, "active_org": org})
}

type credsReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Invite   string `json:"invite"` // invite token; lets sign-up pass on invite-only instances
}

func (p *PasswordAuth) signup(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Email = storage.NormalizeEmail(req.Email)
	if !p.throttle.Allow(w, r, req.Email) {
		return
	}
	// Every refusal counts as a failed attempt: probing which emails have
	// accounts, or guessing invite tokens, is throttled like sign-in.
	if status, code, message := p.signupRefusal(r, &req); status != 0 {
		p.throttle.Fail(r, req.Email)
		body := map[string]any{"error": message, "code": code}
		if code == "invite_only" {
			body["status"] = code // installed 0.4.x apps read this field
		}
		respondJSON(w, status, body)
		return
	}

	// Sign-up never grants admin; the instance admin comes from EnsureAdmin.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), passwordHashCost)
	if err != nil {
		internalError(w, "hash password", err)
		return
	}

	u := &storage.User{
		ID: uuid.NewString(), Email: req.Email, Name: strings.TrimSpace(req.Name), Role: "user",
		PasswordHash: string(hash), CreatedAt: time.Now().UTC(),
		EmailVerified: !p.cfg.Auth.EmailVerification, // verified immediately unless required
	}
	if p.cfg.Auth.EmailVerification {
		u.VerifyToken = randomToken()
	}
	if err := p.users.Create(r.Context(), u); err != nil {
		// Two sign-ups for one address at the same moment: the database let
		// only one through.
		if _, lookupErr := p.users.GetByEmail(r.Context(), u.Email); lookupErr == nil {
			p.throttle.Fail(r, req.Email)
			respondError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
			return
		}
		internalError(w, "create account", err)
		return
	}
	p.throttle.Count(r)

	if p.cfg.Auth.EmailVerification {
		go p.sendVerification(u.Email, u.VerifyToken)
		respondJSON(w, http.StatusOK, map[string]any{"status": "verify"})
		return
	}
	org, err := p.prov.EnsurePersonalTenant(r.Context(), u)
	if err != nil {
		internalError(w, "create personal workspace", err)
		return
	}
	tok, ok := p.issue(w, u, org)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": u.Name, "token": tok, "user_id": u.ID, "active_org": org})
}

// signupRefusal checks a sign-up request against the instance's rules and
// returns the error to answer with; status 0 means the account may be created.
func (p *PasswordAuth) signupRefusal(r *http.Request, req *credsReq) (status int, code, message string) {
	if !strings.Contains(req.Email, "@") || strings.ContainsAny(req.Email, " \t\r\n") || len(req.Password) < minPasswordLen {
		return http.StatusBadRequest, "invalid_signup", "a valid email and an 8+ character password are required"
	}
	if len(req.Password) > maxPasswordLen {
		return http.StatusBadRequest, "password_too_long", "the password may be at most 72 bytes long"
	}
	// The demo account is seeded, never signed up, even if the seeder has not
	// run yet, its email must not be claimable by a visitor.
	if isDemoAccount(p.cfg, req.Email) {
		return http.StatusForbidden, "email_reserved", "this email is reserved"
	}
	if _, err := p.users.GetByEmail(r.Context(), req.Email); err == nil {
		return http.StatusConflict, "email_taken", "an account with this email already exists"
	}
	if p.cfg.Auth.RegistrationOpen {
		return 0, "", ""
	}
	// Invite-only instance: sign-up requires a valid invite issued to this
	// email. (The admin account exists from boot, see EnsureAdmin, so there
	// is no first-run exception.) The invite itself is consumed later by
	// /auth/accept-invite (once the fresh account is signed in).
	inv, err := p.invites.GetByToken(r.Context(), strings.TrimSpace(req.Invite))
	switch {
	case err != nil:
		return http.StatusForbidden, "invite_only", "this instance is invite-only"
	case inv.Expired(time.Now()):
		return http.StatusForbidden, "invite_expired", "this invite has expired; ask for a new one"
	case !strings.EqualFold(inv.Email, req.Email):
		return http.StatusForbidden, "invite_email_mismatch", "this invite was issued for a different email address"
	}
	return 0, "", ""
}

// dummyHash is compared against when the account does not exist or has no
// password, so that answering takes as long as for a real account and the
// response time does not reveal which emails are registered.
var dummyHash = sync.OnceValue(newDummyHash)

func newDummyHash() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte(randomToken()), passwordHashCost)
	return h
}

// Password length limits. bcrypt reads at most 72 bytes; longer input is
// refused instead of silently ignoring the rest.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
)

// isDemoAccount reports whether the address is the shared, read-only demo
// account of this instance.
func isDemoAccount(cfg *config.Config, email string) bool {
	return cfg.Demo.Enabled && storage.NormalizeEmail(email) == storage.NormalizeEmail(cfg.Demo.Email)
}

// isEnvAdmin reports whether the address is the instance admin whose
// credentials come from BEEHIVE_ADMIN_EMAIL / BEEHIVE_ADMIN_PASSWORD and are
// written back at every start.
func isEnvAdmin(cfg *config.Config, email string) bool {
	return cfg.Auth.PasswordEnabled && cfg.Auth.AdminEmail != "" &&
		storage.NormalizeEmail(email) == storage.NormalizeEmail(cfg.Auth.AdminEmail)
}

// passwordHashCost is the bcrypt work factor for every stored password. A
// variable only so the tests, which hash hundreds of passwords, can lower it.
var passwordHashCost = bcrypt.DefaultCost

func (p *PasswordAuth) signin(w http.ResponseWriter, r *http.Request) {
	var req credsReq
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Email = storage.NormalizeEmail(req.Email)
	if !p.throttle.Allow(w, r, req.Email) {
		return
	}
	u, err := p.users.GetByEmail(r.Context(), req.Email)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		internalError(w, "look up account", err)
		return
	}
	hash := dummyHash()
	known := err == nil && u.PasswordHash != ""
	if known {
		hash = []byte(u.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil || !known {
		p.throttle.Fail(r, req.Email)
		respondError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
		return
	}
	p.throttle.Succeed(req.Email)
	if p.cfg.Auth.EmailVerification && !u.EmailVerified {
		// The password was right, so this is the account's owner: send the
		// link again in case the first mail never arrived.
		if u.VerifyToken != "" {
			go p.sendVerification(u.Email, u.VerifyToken)
		}
		respondJSON(w, http.StatusForbidden, map[string]any{
			"error": "please verify your email first; we have sent the link again", "code": "email_unverified", "status": "unverified"})
		return
	}
	org := p.prov.ResolveActiveOrg(r.Context(), u)
	tok, ok := p.issue(w, u, org)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "admin": u.Role == "admin", "name": u.Name, "token": tok, "user_id": u.ID, "active_org": org})
}

// verify confirms an email address. The link is opened from the mail, so
// every outcome is a redirect to the sign-in page.
func (p *PasswordAuth) verify(w http.ResponseWriter, r *http.Request) {
	u, err := p.users.GetByVerifyToken(r.Context(), r.URL.Query().Get("token"))
	if errors.Is(err, storage.ErrNotFound) {
		redirectError(w, r, "verification_invalid")
		return
	}
	if err != nil {
		redirectInternalError(w, r, "look up verification", err)
		return
	}
	if err := p.users.MarkVerified(r.Context(), u.ID); err != nil {
		redirectInternalError(w, r, "mark verified", err)
		return
	}
	http.Redirect(w, r, "/login?verified=1", http.StatusFound)
}

// issue mints a session for the user with the given active tenant, sets the
// cookie, and returns the token (for clients that use Bearer auth, e.g. the SPA
// talking to a different origin). On failure it writes the error response and
// returns false.
func (p *PasswordAuth) issue(w http.ResponseWriter, u *storage.User, orgID string) (string, bool) {
	role := u.Role
	// Demo sessions are read-only: the "demo" role is rejected by the RPC
	// write guard and the tenant endpoints, however the account signs in.
	if isDemoAccount(p.cfg, u.Email) {
		role = "demo"
	}
	tok, err := p.sessions.Issue(Identity{UserID: u.ID, OrgID: orgID, Email: u.Email, Role: role})
	if err != nil {
		internalError(w, "issue session", err)
		return "", false
	}
	p.sessions.SetCookie(w, tok)
	return tok, true
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sendVerification emails the verification link. The link is a credential
// and is never logged; configuration requires a mail server whenever
// verification is on.
func (p *PasswordAuth) sendVerification(email, token string) {
	link := fmt.Sprintf("%s/auth/verify?token=%s", strings.TrimRight(p.cfg.PublicBaseURL, "/"), token)
	body := fmt.Sprintf("Welcome to Openbeehive!\r\n\r\nConfirm your email to finish signing up:\r\n%s\r\n", link)
	if err := p.mail(email, "Verify your Openbeehive email", body); err != nil {
		log.Printf("auth: verification email not sent: %v", err)
	}
}
