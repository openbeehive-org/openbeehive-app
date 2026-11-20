// Package auth implements OIDC login with multiple providers in parallel
// (e.g. Google, Microsoft Entra, GitHub via OIDC, Keycloak/Authentik for
// self-hosted setups) plus a Connect interceptor for authentication.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type Provider struct {
	Name     string
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

type Manager struct {
	providers map[string]*Provider
}

type Claims struct {
	Subject       string       `json:"sub"`
	Email         string       `json:"email"`
	EmailVerified claimBoolean `json:"email_verified"`
	Name          string       `json:"name"`
}

// claimBoolean reads a boolean claim that some providers send as the string
// "true" instead of a JSON boolean. Anything else, or a missing claim, is false.
type claimBoolean bool

func (b *claimBoolean) UnmarshalJSON(data []byte) error {
	*b = claimBoolean(string(data) == "true" || string(data) == `"true"`)
	return nil
}

// NewManager initializes all providers enabled in the config.
func NewManager(ctx context.Context, cfg *config.Config) (*Manager, error) {
	m := &Manager{providers: map[string]*Provider{}}
	for _, name := range cfg.OIDC.Enabled {
		pc := cfg.OIDC.Providers[name]
		if pc.IssuerURL == "" || pc.ClientID == "" {
			return nil, fmt.Errorf("OIDC provider %q: issuer/client id missing", name)
		}
		op, err := oidc.NewProvider(ctx, pc.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("OIDC provider %q: cannot reach the issuer %s (check BEEHIVE_OIDC_%s_ISSUER): %w",
				name, pc.IssuerURL, strings.ToUpper(name), err)
		}
		m.providers[name] = &Provider{
			Name:     name,
			provider: op,
			verifier: op.Verifier(&oidc.Config{ClientID: pc.ClientID}),
			oauth: oauth2.Config{
				ClientID:     pc.ClientID,
				ClientSecret: pc.ClientSecret,
				Endpoint:     op.Endpoint(),
				RedirectURL:  cfg.OIDC.RedirectURL + "?provider=" + name,
				Scopes:       pc.Scopes,
			},
		}
	}
	return m, nil
}

// firstName returns any configured provider (used when none is specified).
func (m *Manager) firstName() string {
	for n := range m.providers {
		return n
	}
	return ""
}

const oauthStateCookie = "obh_oauth_state"

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Routes registers the OIDC endpoints. Both are browser navigations: the
// login link on the sign-in page, and the provider's redirect back.
func (m *Manager) Routes(rt *Router, sessions *SessionManager, users storage.UserRepo, prov *Provisioner, cfg *config.Config) {
	rt.Get("/auth/login", m.login)
	rt.Get("/auth/callback", m.callback(sessions, users, prov, cfg))
}

// login starts the OIDC auth-code flow: GET /auth/login?provider=google
func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = m.firstName()
	}
	state := randToken()
	url, err := m.AuthCodeURL(provider, state)
	if err != nil {
		redirectError(w, r, "unknown_provider")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: provider + ":" + state, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: true, MaxAge: 600,
	})
	http.Redirect(w, r, url, http.StatusFound)
}

// callback completes the flow, provisions/links an in-app user, issues an
// app session and redirects home. The provider sends the browser here, so a
// failure is a redirect to the sign-in page with an error code.
func (m *Manager) callback(sessions *SessionManager, users storage.UserRepo, prov *Provisioner, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code, state := q.Get("code"), q.Get("state")
		ck, err := r.Cookie(oauthStateCookie)
		if err != nil {
			redirectError(w, r, "oidc_state_missing")
			return
		}
		provider, wantState, _ := strings.Cut(ck.Value, ":")
		if p := q.Get("provider"); p != "" {
			provider = p
		}
		if state == "" || state != wantState {
			redirectError(w, r, "oidc_state_invalid")
			return
		}
		claims, err := m.Exchange(r.Context(), provider, code)
		if err != nil {
			log.Printf("oidc: %s: code exchange failed: %v", provider, err) //nolint:gosec // G706: Exchange only succeeds or fails for a configured provider name
			redirectError(w, r, "oidc_failed")
			return
		}
		u, err := provisionOIDCUser(r.Context(), users, prov, cfg, provider, claims)
		if err != nil {
			if code := oidcErrorCode(err); code != "" {
				redirectError(w, r, code)
			} else {
				redirectInternalError(w, r, "oidc provisioning", err)
			}
			return
		}
		org := prov.ResolveActiveOrg(r.Context(), u)
		role := u.Role
		if isDemoAccount(cfg, u.Email) {
			role = "demo" // demo sessions are read-only
		}
		tok, err := sessions.IssueLogin(Identity{UserID: u.ID, OrgID: org, Email: u.Email, Role: role})
		if err != nil {
			redirectInternalError(w, r, "issue session", err)
			return
		}
		sessions.SetCookie(w, tok)
		http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

var (
	// errInviteOnly rejects new-account provisioning on invite-only instances.
	errInviteOnly = errors.New("this instance is invite-only; ask for an invite, then sign up with email and password first")
	// errLinkRefused rejects attaching a provider login to an existing
	// account when either side's email address is unproven.
	errLinkRefused = errors.New("an account with this email address already exists; sign in the way you did before (for example with your email and password)")
	// errNoEmail rejects provider logins that carry no email address.
	errNoEmail = errors.New("the sign-in provider did not share an email address; allow the email scope and try again")
)

// oidcErrorCode maps a refused provisioning to the code the sign-in page
// translates; "" for anything unexpected.
func oidcErrorCode(err error) string {
	switch {
	case errors.Is(err, errInviteOnly):
		return "invite_only"
	case errors.Is(err, errLinkRefused):
		return "account_exists"
	case errors.Is(err, errNoEmail):
		return "oidc_no_email"
	}
	return ""
}

// provisionOIDCUser finds the in-app account for an OIDC login: by provider
// subject, then by email address, otherwise it registers a new account.
//
// Linking by email attaches the provider login to an existing account, which
// hands that account to whoever controls the provider identity. It therefore
// happens only when both sides have proven the address: the provider asserts
// email_verified, and the local account's email is verified. Otherwise the
// login is refused and the user keeps signing in the way they did before.
//
// Provisioning never grants admin; the instance admin comes from
// BEEHIVE_ADMIN_EMAIL / BEEHIVE_ADMIN_PASSWORD (auth.EnsureAdmin).
func provisionOIDCUser(ctx context.Context, users storage.UserRepo, prov *Provisioner, cfg *config.Config, provider string, c *Claims) (*storage.User, error) {
	subject := provider + ":" + c.Subject
	if u, err := users.GetBySubject(ctx, subject); err == nil {
		return u, nil
	}
	email := storage.NormalizeEmail(c.Email)
	if email == "" {
		return nil, errNoEmail
	}
	switch u, err := users.GetByEmail(ctx, email); {
	case err == nil:
		if !bool(c.EmailVerified) || !u.EmailVerified {
			return nil, errLinkRefused
		}
		if err := users.LinkOIDC(ctx, u.ID, subject); err != nil {
			return nil, err
		}
		u.OIDCSubject = subject
		return u, nil
	case !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}
	// Invite-only instances provision no new accounts over OIDC (invites are a
	// password-signup flow); existing accounts linked above sign in normally.
	if !cfg.Auth.RegistrationOpen {
		return nil, errInviteOnly
	}
	u := &storage.User{
		ID: uuid.NewString(), Email: email, Name: c.Name, OIDCSubject: subject,
		Role: "user", EmailVerified: bool(c.EmailVerified), CreatedAt: time.Now().UTC(),
	}
	if err := users.Create(ctx, u); err != nil {
		return nil, err
	}
	if _, err := prov.EnsurePersonalTenant(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func (m *Manager) Names() []string {
	out := make([]string, 0, len(m.providers))
	for n := range m.providers {
		out = append(out, n)
	}
	return out
}

// AuthCodeURL returns the redirect URL to the provider login.
func (m *Manager) AuthCodeURL(provider, state string) (string, error) {
	p, ok := m.providers[provider]
	if !ok {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	return p.oauth.AuthCodeURL(state), nil
}

// Exchange swaps the auth code for tokens and verifies the ID token.
func (m *Manager) Exchange(ctx context.Context, provider, code string) (*Claims, error) {
	p, ok := m.providers[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
	tok, err := p.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("no id_token in response")
	}
	return m.verify(ctx, provider, raw)
}

// verify checks a raw ID token against the provider and returns its claims.
func (m *Manager) verify(ctx context.Context, provider, rawIDToken string) (*Claims, error) {
	p, ok := m.providers[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
	idt, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, err
	}
	var c Claims
	if err := idt.Claims(&c); err != nil {
		return nil, err
	}
	return &c, nil
}
