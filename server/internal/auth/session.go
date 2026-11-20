package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// SessionCookie is the name of the app-session cookie (set after OIDC/WebAuthn
// login). For same-origin deployments the browser sends it automatically; the
// SPA may also store the token and send it as a Bearer header (cross-origin).
const SessionCookie = "obh_session"

type sessionClaims struct {
	UserID string `json:"uid"`
	OrgID  string `json:"org"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Exp    int64  `json:"exp"`
	// IssuedAt is in Unix milliseconds. Tokens minted by 0.4.x servers have
	// none (0): they stay valid until their account's first revocation.
	IssuedAt int64 `json:"iat,omitempty"`
}

// sessionStateTTL is how long a verified (account, workspace) pair is trusted
// before the database is asked again. It bounds how long a removed member or
// a session revoked by another process path keeps working.
const sessionStateTTL = 10 * time.Second

// sessionStateMax caps the cache; past it the cache starts over.
const sessionStateMax = 10_000

type sessionState struct {
	validAfter int64 // users.sessions_valid_after
	member     bool  // the account belongs to the workspace
	fetched    time.Time
}

// SessionManager issues and verifies compact HMAC-signed app-session tokens
// (a minimal JWS: base64url(claims).base64url(HMAC-SHA256)). Provider-
// independent: password, OIDC and WebAuthn sign-ins all mint sessions through
// it.
//
// A valid signature is not enough: every verification also checks that the
// account still exists, that the token was issued after the account's
// "sessions valid after" mark (see RevokeAll), and that the account is still
// a member of the workspace the token is for. Those lookups are cached for
// sessionStateTTL.
type SessionManager struct {
	secret  []byte
	ttl     time.Duration
	users   storage.UserRepo
	members storage.MemberRepo

	mu    sync.Mutex
	state map[string]sessionState // "<user>\x00<org>"
	epoch int                     // bumped by Invalidate; stale lookups are not cached
}

// NewSessionManager refuses secrets shorter than config.MinSessionSecretBytes:
// the secret is the only thing that stands between an attacker and forging a
// session for any account.
func NewSessionManager(secret string, ttl time.Duration, users storage.UserRepo, members storage.MemberRepo) (*SessionManager, error) {
	if len(secret) < config.MinSessionSecretBytes {
		return nil, fmt.Errorf("session secret must be at least %d bytes", config.MinSessionSecretBytes)
	}
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return &SessionManager{
		secret: []byte(secret), ttl: ttl, users: users, members: members,
		state: map[string]sessionState{},
	}, nil
}

func (s *SessionManager) sign(body string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue mints a signed token for the given identity.
func (s *SessionManager) Issue(id Identity) (string, error) {
	return s.issueAt(id, time.Now())
}

func (s *SessionManager) issueAt(id Identity, at time.Time) (string, error) {
	payload, err := json.Marshal(sessionClaims{
		UserID: id.UserID, OrgID: id.OrgID, Email: id.Email, Role: id.Role,
		Exp: at.Add(s.ttl).Unix(), IssuedAt: at.UnixMilli(),
	})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body), nil
}

var (
	errSessionRevoked = errors.New("session revoked; sign in again")
	errNotMember      = errors.New("no longer a member of this workspace; sign in again")
	// errSessionUnverifiable means the session could not be checked (database
	// trouble), not that it is invalid: callers answer "try again", never
	// "signed out".
	errSessionUnverifiable = errors.New("the session could not be verified right now; try again")
)

// Verify implements SessionVerifier.
func (s *SessionManager) Verify(ctx context.Context, token string) (Identity, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Identity{}, errors.New("malformed session token")
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(body))) {
		return Identity{}, errors.New("invalid session signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Identity{}, err
	}
	var c sessionClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return Identity{}, err
	}
	if time.Now().Unix() > c.Exp {
		return Identity{}, errors.New("session expired")
	}
	st, err := s.lookupState(ctx, c.UserID, c.OrgID)
	if err != nil {
		return Identity{}, err
	}
	if c.IssuedAt < st.validAfter {
		return Identity{}, errSessionRevoked
	}
	if !st.member {
		return Identity{}, errNotMember
	}
	return Identity{UserID: c.UserID, OrgID: c.OrgID, Email: c.Email, Role: c.Role}, nil
}

// lookupState returns the account's revocation mark and its membership in the
// workspace, from the cache when fresh.
func (s *SessionManager) lookupState(ctx context.Context, userID, orgID string) (sessionState, error) {
	key := userID + "\x00" + orgID
	now := time.Now()
	s.mu.Lock()
	st, ok := s.state[key]
	epoch := s.epoch
	s.mu.Unlock()
	if ok && now.Sub(st.fetched) < sessionStateTTL {
		return st, nil
	}
	u, err := s.users.GetByID(ctx, userID)
	if errors.Is(err, storage.ErrNotFound) {
		return sessionState{}, errSessionRevoked // the account is gone
	}
	if err != nil {
		log.Printf("auth: session check: %v", err)
		return sessionState{}, errSessionUnverifiable
	}
	st = sessionState{validAfter: u.SessionsValidAfter, fetched: now}
	switch _, err := s.members.Get(ctx, orgID, userID); {
	case err == nil:
		st.member = true
	case !errors.Is(err, storage.ErrNotFound):
		log.Printf("auth: session check: %v", err)
		return sessionState{}, errSessionUnverifiable
	}
	s.mu.Lock()
	if epoch == s.epoch { // otherwise Invalidate ran while we were reading
		if len(s.state) >= sessionStateMax {
			s.state = map[string]sessionState{}
		}
		s.state[key] = st
	}
	s.mu.Unlock()
	return st, nil
}

// Invalidate drops the cached session state so the next request of every
// session re-checks the database. Call it after anything that must take
// effect at once: revoking sessions, removing a member, deleting a workspace.
func (s *SessionManager) Invalidate() {
	s.mu.Lock()
	s.state = map[string]sessionState{}
	s.epoch++
	s.mu.Unlock()
}

// RevokeAll ends every session of the account ("log out everywhere", and the
// step to take after a password change) and returns a fresh token for the
// caller, the only session that survives.
func (s *SessionManager) RevokeAll(ctx context.Context, id Identity) (string, error) {
	// One millisecond ahead: a token issued in this very millisecond, before
	// the revocation, must not survive it.
	mark := time.Now().Add(time.Millisecond)
	if err := s.users.SetSessionsValidAfter(ctx, id.UserID, mark.UnixMilli()); err != nil {
		return "", err
	}
	s.Invalidate()
	return s.issueAt(id, mark)
}

// Authenticate reads and verifies the session of a plain HTTP request
// (Authorization: Bearer header or the session cookie). When there is no
// valid session it writes the error response and returns false. The Connect
// interceptor does the same for the RPC services.
func (s *SessionManager) Authenticate(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		if c, err := r.Cookie(SessionCookie); err == nil {
			token = c.Value
		}
	}
	if token == "" {
		notSignedIn(w)
		return Identity{}, false
	}
	id, err := s.Verify(r.Context(), token)
	switch {
	case errors.Is(err, errSessionUnverifiable):
		respondError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return Identity{}, false
	case err != nil:
		notSignedIn(w)
		return Identity{}, false
	}
	return id, true
}

// SetCookie writes the session as a secure, http-only cookie.
func (s *SessionManager) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: true, MaxAge: int(s.ttl.Seconds()),
	})
}

// Routes registers the session endpoints.
func (s *SessionManager) Routes(rt *Router) {
	rt.Post("/auth/logout", s.logout)
	rt.Post("/auth/logout-all", s.logoutAll)
}

// logout clears the session cookie. The token itself stays valid until it
// expires or the account's sessions are revoked; clients discard it.
func (s *SessionManager) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

// logoutAll ends every other session of the caller's account and hands the
// caller a fresh one.
func (s *SessionManager) logoutAll(w http.ResponseWriter, r *http.Request) {
	id, ok := s.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w) // the demo account is shared by all its visitors
		return
	}
	tok, err := s.RevokeAll(r.Context(), id)
	if err != nil {
		internalError(w, "revoke sessions", err)
		return
	}
	s.SetCookie(w, tok)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "token": tok, "active_org": id.OrgID})
}

// SessionInterceptor authenticates Connect requests from the session token,
// taken from the Authorization: Bearer header or the session cookie. It covers
// unary calls and handler streams (Subscribe).
func SessionInterceptor(v SessionVerifier) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			info, ok := connect.CallInfoForServerContext(ctx)
			if !ok {
				return connect.NewError(connect.CodeUnauthenticated, errMissingToken.Error()).WithCause(errMissingToken)
			}
			id, err := verifyHeaders(ctx, v, info.RequestHeader())
			if errors.Is(err, errSessionUnverifiable) {
				// Not a sign-out: the client keeps its session and retries.
				return connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
			}
			if err != nil {
				return connect.NewError(connect.CodeUnauthenticated, err.Error()).WithCause(err)
			}
			return next(WithIdentity(ctx, id), spec, stream)
		}
	}
}

func verifyHeaders(ctx context.Context, v SessionVerifier, h *connect.Header) (Identity, error) {
	token := bearer(h.Get("Authorization"))
	if token == "" {
		token = cookieToken(h.Get("Cookie"))
	}
	if token == "" {
		return Identity{}, errMissingToken
	}
	return v.Verify(ctx, token)
}

func bearer(authz string) string {
	const p = "Bearer "
	if strings.HasPrefix(authz, p) {
		return strings.TrimSpace(authz[len(p):])
	}
	return ""
}

func cookieToken(cookieHeader string) string {
	for _, part := range strings.Split(cookieHeader, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k == SessionCookie {
			return val
		}
	}
	return ""
}
