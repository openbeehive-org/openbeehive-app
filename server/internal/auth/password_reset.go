package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// ResetTTL is how long a password reset link stays usable.
const ResetTTL = time.Hour

// ResetStore keeps password reset tokens by their hash; a token can be
// redeemed once.
type ResetStore interface {
	CreatePasswordReset(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	ConsumePasswordReset(ctx context.Context, tokenHash string, now time.Time) (userID string, err error)
}

// mailConfigured reports whether this server can send email.
func (p *PasswordAuth) mailConfigured() bool { return p.cfg.Auth.SMTP.Host != "" }

// resetLink is the page of the app that completes a reset. The link is a
// credential: it goes to the account's mailbox or to the instance admin who
// asked for it, never into the log.
func (p *PasswordAuth) resetLink(token string) string {
	return fmt.Sprintf("%s/reset-password?token=%s", strings.TrimRight(p.cfg.PublicBaseURL, "/"), token)
}

// newReset stores a fresh single-use token for the account and returns the
// link that carries it.
func (p *PasswordAuth) newReset(ctx context.Context, userID string) (link string, expires time.Time, err error) {
	token := randomToken()
	expires = time.Now().Add(ResetTTL).UTC()
	if err := p.resets.CreatePasswordReset(ctx, userID, hashKey(token), expires); err != nil {
		return "", time.Time{}, err
	}
	return p.resetLink(token), expires, nil
}

// passwordProblem checks a new password against the length limits and
// returns the error to answer with, or "" when it is acceptable.
func passwordProblem(password string) (code, message string) {
	if len(password) < minPasswordLen {
		return "invalid_password", "the password must be at least 8 characters long"
	}
	if len(password) > maxPasswordLen {
		return "password_too_long", "the password may be at most 72 bytes long"
	}
	return "", ""
}

// resettable reports whether the account's password may be set through a
// reset link: not the demo account, and not the instance admin, whose
// password the server configuration writes back at every start.
func (p *PasswordAuth) resettable(u *storage.User) bool {
	return !isDemoAccount(p.cfg, u.Email) && !isEnvAdmin(p.cfg, u.Email)
}

type changePasswordReq struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// changePassword sets a new password for the signed-in account. It needs the
// current one, and it ends every other session of the account: the caller
// gets a fresh token, the only one that still works.
func (p *PasswordAuth) changePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := p.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return
	}
	var req changePasswordReq
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := p.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		notSignedIn(w) // the account behind the session is gone
		return
	}
	switch {
	case isEnvAdmin(p.cfg, u.Email):
		respondError(w, http.StatusForbidden, "password_managed", "the instance admin's password is set with BEEHIVE_ADMIN_PASSWORD in the server configuration")
		return
	case u.PasswordHash == "":
		respondError(w, http.StatusConflict, "no_password", "this account has no password yet; set one with the \"Forgot password?\" link on the sign-in page")
		return
	}
	// Guessing the current password with a stolen session is throttled like
	// guessing it on the sign-in page, against the same counters.
	account := storage.NormalizeEmail(u.Email)
	if !p.throttle.Allow(w, r, account) {
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Current)) != nil {
		p.throttle.Fail(r, account)
		respondError(w, http.StatusForbidden, "wrong_password", "the current password is not correct")
		return
	}
	if code, message := passwordProblem(req.New); code != "" {
		respondError(w, http.StatusBadRequest, code, message)
		return
	}
	if !p.storePassword(w, r, u.ID, req.New) {
		return
	}
	tok, err := p.sessions.RevokeAll(r.Context(), id)
	if err != nil {
		internalError(w, "revoke sessions", err)
		return
	}
	p.sessions.SetCookie(w, tok)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "token": tok, "active_org": id.OrgID})
}

// storePassword hashes and saves a password; on failure it writes the error
// response and returns false.
func (p *PasswordAuth) storePassword(w http.ResponseWriter, r *http.Request, userID, password string) bool {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), passwordHashCost)
	if err != nil {
		internalError(w, "hash password", err)
		return false
	}
	if err := p.users.SetPassword(r.Context(), userID, string(hash)); err != nil {
		internalError(w, "store password", err)
		return false
	}
	return true
}

type resetRequestReq struct {
	Email string `json:"email"`
}

// requestReset emails a reset link to the account with the given address.
// The answer is the same, and takes the same time, whether or not such an
// account exists: the lookup and the mail happen after the response. Requests
// are throttled per address and per client like sign-in attempts.
//
// A server without a mail server cannot do this; it says so for every
// address, and its instance admin issues links instead (adminResetLink).
func (p *PasswordAuth) requestReset(w http.ResponseWriter, r *http.Request) {
	var req resetRequestReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !p.mailConfigured() {
		respondError(w, http.StatusConflict, "reset_needs_admin", "this server sends no email; ask its administrator for a reset link")
		return
	}
	email := storage.NormalizeEmail(req.Email)
	// A counter of its own per address: asking for links must not lock the
	// account's owner out of signing in.
	account := "reset:" + email
	if !p.throttle.Allow(w, r, account) {
		return
	}
	p.throttle.Fail(r, account)
	p.background.Add(1)
	// After the response on purpose: how long the lookup and the mail take
	// must not show in the answer.
	go func() {
		defer p.background.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		p.mailReset(ctx, email)
	}()
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// mailReset sends the reset link if the address belongs to an account whose
// password can be reset. Failures are logged without the address's link.
func (p *PasswordAuth) mailReset(ctx context.Context, email string) {
	u, err := p.users.GetByEmail(ctx, email)
	if errors.Is(err, storage.ErrNotFound) {
		return
	}
	if err != nil {
		log.Printf("auth: password reset: look up account: %v", err)
		return
	}
	if !p.resettable(u) {
		return
	}
	link, _, err := p.newReset(ctx, u.ID)
	if err != nil {
		log.Printf("auth: password reset: store token: %v", err)
		return
	}
	body := fmt.Sprintf("Someone asked to reset the password of your Openbeehive account.\r\n\r\n"+
		"Open this link to choose a new password (valid for one hour, usable once):\r\n%s\r\n\r\n"+
		"If that was not you, ignore this message: your password stays as it is.\r\n", link)
	if err := p.mail(u.Email, "Reset your Openbeehive password", body); err != nil {
		log.Printf("auth: password reset email for account %s not sent: %v", u.ID, err)
	}
}

type completeResetReq struct {
	Token string `json:"token"`
	New   string `json:"new_password"`
}

// completeReset sets a new password with a reset token. The token works
// once; using it ends every session of the account, so whoever held one
// before has to sign in with the new password.
func (p *PasswordAuth) completeReset(w http.ResponseWriter, r *http.Request) {
	var req completeResetReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !p.throttle.Allow(w, r, "") {
		return
	}
	// Checked before the token is spent: a password that is too short must
	// not cost the user their link.
	if code, message := passwordProblem(req.New); code != "" {
		respondError(w, http.StatusBadRequest, code, message)
		return
	}
	userID, err := p.resets.ConsumePasswordReset(r.Context(), hashKey(strings.TrimSpace(req.Token)), time.Now())
	if errors.Is(err, storage.ErrNotFound) {
		p.throttle.Fail(r, "")
		respondError(w, http.StatusBadRequest, "reset_invalid", "this reset link is not valid, was already used or has expired; ask for a new one")
		return
	}
	if err != nil {
		internalError(w, "redeem reset token", err)
		return
	}
	u, err := p.users.GetByID(r.Context(), userID)
	if err != nil || !p.resettable(u) {
		respondError(w, http.StatusBadRequest, "reset_invalid", "this reset link is not valid, was already used or has expired; ask for a new one")
		return
	}
	if !p.storePassword(w, r, u.ID, req.New) {
		return
	}
	if err := p.sessions.RevokeEvery(r.Context(), u.ID); err != nil {
		internalError(w, "revoke sessions", err)
		return
	}
	p.throttle.Succeed(storage.NormalizeEmail(u.Email))
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// adminResetLink creates a reset link for the account with the given email
// address and returns it to the caller, who hands it over. Only the instance
// admin may do this; a workspace owner may not, since a workspace does not
// own the accounts of its members.
func (p *PasswordAuth) adminResetLink(w http.ResponseWriter, r *http.Request) {
	id, ok := p.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role != "admin" {
		respondError(w, http.StatusForbidden, "admin_only", "only the instance admin can create reset links")
		return
	}
	var req resetRequestReq
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := p.users.GetByEmail(r.Context(), req.Email)
	if errors.Is(err, storage.ErrNotFound) {
		respondError(w, http.StatusNotFound, "account_not_found", "no account has this email address")
		return
	}
	if err != nil {
		internalError(w, "look up account", err)
		return
	}
	if !p.resettable(u) {
		respondError(w, http.StatusForbidden, "password_managed", "this account's password is set in the server configuration")
		return
	}
	link, expires, err := p.newReset(r.Context(), u.ID)
	if err != nil {
		internalError(w, "store reset token", err)
		return
	}
	log.Printf("auth: instance admin %s created a password reset link for account %s", id.UserID, u.ID)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "link": link, "expires_at": expires})
}
