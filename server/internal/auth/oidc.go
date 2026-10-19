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
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
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
			return nil, fmt.Errorf("OIDC %q: %w", name, err)
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

// LoginHandler starts the OIDC auth-code flow: GET /auth/login?provider=google
func (m *Manager) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.URL.Query().Get("provider")
		if provider == "" {
			provider = m.firstName()
		}
		state := randToken()
		http.SetCookie(w, &http.Cookie{
			Name: oauthStateCookie, Value: provider + ":" + state, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: true, MaxAge: 600,
		})
		url, err := m.AuthCodeURL(provider, state)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	}
}

// CallbackHandler completes the flow, provisions/links an in-app user, issues an
// app session and redirects home.
func (m *Manager) CallbackHandler(sessions *SessionManager, users storage.UserRepo, prov *Provisioner, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code, state := q.Get("code"), q.Get("state")
		ck, err := r.Cookie(oauthStateCookie)
		if err != nil {
			http.Error(w, "missing state", http.StatusBadRequest)
			return
		}
		provider, wantState, _ := strings.Cut(ck.Value, ":")
		if p := q.Get("provider"); p != "" {
			provider = p
		}
		if state == "" || state != wantState {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		claims, err := m.Exchange(r.Context(), provider, code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		u, err := provisionOIDCUser(r.Context(), users, prov, cfg, provider, claims)
		if err != nil {
			if errors.Is(err, errInviteOnly) {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		org := prov.ResolveActiveOrg(r.Context(), u)
		role := u.Role
		if cfg.Demo.Enabled && strings.EqualFold(u.Email, cfg.Demo.Email) {
			role = "demo" // demo sessions are read-only
		}
		tok, err := sessions.Issue(Identity{UserID: u.ID, OrgID: org, Email: u.Email, Role: role})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sessions.SetCookie(w, tok)
		http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// errInviteOnly rejects new-account provisioning on invite-only instances.
var errInviteOnly = errors.New("this instance is invite-only; ask for an invite, then sign up with email and password first")

// provisionOIDCUser finds the in-app account for an OIDC login: by provider
// subject, then by matching email (linking an existing email/password profile),
// otherwise it registers a new account. Provisioning never grants admin; the
// instance admin comes from BEEHIVE_ADMIN_EMAIL / BEEHIVE_ADMIN_PASSWORD
// (auth.EnsureAdmin).
func provisionOIDCUser(ctx context.Context, users storage.UserRepo, prov *Provisioner, cfg *config.Config, provider string, c *Claims) (*storage.User, error) {
	subject := provider + ":" + c.Subject
	if u, err := users.GetBySubject(ctx, subject); err == nil {
		return u, nil
	}
	if c.Email != "" {
		if u, err := users.GetByEmail(ctx, c.Email); err == nil {
			_ = users.LinkOIDC(ctx, u.ID, subject) // connect existing profile
			u.OIDCSubject = subject
			return u, nil
		}
	}
	// Invite-only instances provision no new accounts over OIDC (invites are a
	// password-signup flow); existing accounts linked above sign in normally.
	if !cfg.Auth.RegistrationOpen {
		return nil, errInviteOnly
	}
	u := &storage.User{
		ID: uuid.NewString(), Email: c.Email, Name: c.Name, OIDCSubject: subject,
		Role: "user", EmailVerified: true, CreatedAt: time.Now().UTC(),
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
