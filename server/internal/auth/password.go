package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
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
	sessions *SessionManager
	cfg      *config.Config
	prov     *Provisioner
}

func NewPasswordAuth(users storage.UserRepo, invites storage.InviteRepo, sessions *SessionManager, cfg *config.Config, prov *Provisioner) *PasswordAuth {
	return &PasswordAuth{users: users, invites: invites, sessions: sessions, cfg: cfg, prov: prov}
}

// Routes registers the onboarding endpoints on the mux.
func (p *PasswordAuth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/signup", p.signup)
	mux.HandleFunc("/auth/signin", p.signin)
	mux.HandleFunc("/auth/verify", p.verify)
	if p.cfg.Demo.Enabled {
		mux.HandleFunc("/auth/demo-login", p.demoLogin)
	}
}

func respondJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// demoLogin signs the visitor in as the demo account (no password needed). Only
// registered when the demo is enabled.
func (p *PasswordAuth) demoLogin(w http.ResponseWriter, r *http.Request) {
	u, err := p.users.GetByEmail(r.Context(), p.cfg.Demo.Email)
	if err != nil {
		respondJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "demo not available"})
		return
	}
	org := p.prov.ResolveActiveOrg(r.Context(), u)
	tok := p.issue(w, u, org)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": u.Name, "token": tok, "user_id": u.ID, "active_org": org})
}

type credsReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Invite   string `json:"invite"` // invite token; lets sign-up pass on invite-only instances
}

func (p *PasswordAuth) signup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req credsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if !strings.Contains(req.Email, "@") || len(req.Password) < 8 {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "a valid email and an 8+ character password are required"})
		return
	}
	// The demo account is seeded, never signed up — even if the seeder has not
	// run yet, its email must not be claimable by a visitor.
	if p.cfg.Demo.Enabled && strings.EqualFold(req.Email, p.cfg.Demo.Email) {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "this email is reserved"})
		return
	}
	if _, err := p.users.GetByEmail(r.Context(), req.Email); err == nil {
		respondJSON(w, http.StatusConflict, map[string]any{"error": "an account with this email already exists"})
		return
	}

	// Invite-only instance: sign-up requires a valid invite issued to this
	// email. (The admin account exists from boot — see EnsureAdmin — so there
	// is no first-run exception.) The invite itself is consumed later by
	// /auth/accept-invite (once the fresh account is signed in).
	if !p.cfg.Auth.RegistrationOpen {
		inv, err := p.invites.GetByToken(r.Context(), strings.TrimSpace(req.Invite))
		if req.Invite == "" || err != nil {
			respondJSON(w, http.StatusForbidden, map[string]any{"error": "this instance is invite-only", "status": "invite_only"})
			return
		}
		if !strings.EqualFold(inv.Email, req.Email) {
			respondJSON(w, http.StatusForbidden, map[string]any{"error": "this invite was issued for a different email address"})
			return
		}
	}

	// Sign-up never grants admin; the instance admin comes from EnsureAdmin.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": "hashing failed"})
		return
	}

	u := &storage.User{
		ID: uuid.NewString(), Email: req.Email, Name: req.Name, Role: "user",
		PasswordHash: string(hash), CreatedAt: time.Now().UTC(),
		EmailVerified: !p.cfg.Auth.EmailVerification, // verified immediately unless required
	}
	if p.cfg.Auth.EmailVerification {
		u.VerifyToken = randomToken()
	}
	if err := p.users.Create(r.Context(), u); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create account"})
		return
	}

	if p.cfg.Auth.EmailVerification {
		p.sendVerification(u.Email, u.VerifyToken)
		respondJSON(w, http.StatusOK, map[string]any{"status": "verify"})
		return
	}
	org, _ := p.prov.EnsurePersonalTenant(r.Context(), u)
	tok := p.issue(w, u, org)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": u.Name, "token": tok, "user_id": u.ID, "active_org": org})
}

func (p *PasswordAuth) signin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req credsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	u, err := p.users.GetByEmail(r.Context(), req.Email)
	if err != nil || u.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "wrong email or password"})
		return
	}
	if p.cfg.Auth.EmailVerification && !u.EmailVerified {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "please verify your email first", "status": "unverified"})
		return
	}
	org := p.prov.ResolveActiveOrg(r.Context(), u)
	tok := p.issue(w, u, org)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "admin": u.Role == "admin", "name": u.Name, "token": tok, "user_id": u.ID, "active_org": org})
}

func (p *PasswordAuth) verify(w http.ResponseWriter, r *http.Request) {
	u, err := p.users.GetByVerifyToken(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "invalid or expired verification link", http.StatusBadRequest)
		return
	}
	if err := p.users.MarkVerified(r.Context(), u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/login?verified=1", http.StatusFound)
}

// issue mints a session for the user with the given active tenant, sets the
// cookie, and returns the token (for clients that use Bearer auth, e.g. the SPA
// talking to a different origin).
func (p *PasswordAuth) issue(w http.ResponseWriter, u *storage.User, orgID string) string {
	role := u.Role
	// Demo sessions are read-only: the "demo" role is rejected by the RPC
	// write guard and the tenant endpoints, however the account signs in.
	if p.cfg.Demo.Enabled && strings.EqualFold(u.Email, p.cfg.Demo.Email) {
		role = "demo"
	}
	tok, err := p.sessions.Issue(Identity{UserID: u.ID, OrgID: orgID, Email: u.Email, Role: role})
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return ""
	}
	p.sessions.SetCookie(w, tok)
	return tok
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sendVerification emails the verification link, or logs it when SMTP is unset.
func (p *PasswordAuth) sendVerification(email, token string) {
	link := fmt.Sprintf("%s/auth/verify?token=%s", strings.TrimRight(p.cfg.PublicBaseURL, "/"), token)
	body := fmt.Sprintf("Welcome to Openbeehive!\r\n\r\nConfirm your email to finish signing up:\r\n%s\r\n", link)
	err := sendMail(p.cfg, email, "Verify your Openbeehive email", body)
	switch {
	case errors.Is(err, errSMTPUnset):
		log.Printf("auth: email verification link for %s: %s", email, link)
	case err != nil:
		log.Printf("auth: failed to send verification email to %s (%v); link: %s", email, err, link)
	}
}
