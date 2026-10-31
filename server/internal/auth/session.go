package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
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
}

// SessionManager issues and verifies compact HMAC-signed app-session tokens
// (a minimal JWS: base64url(claims).base64url(HMAC-SHA256)). Provider-
// independent: OIDC and WebAuthn both mint sessions through it.
type SessionManager struct {
	secret []byte
	ttl    time.Duration
}

func NewSessionManager(secret string, ttl time.Duration) *SessionManager {
	b := []byte(secret)
	if len(b) == 0 {
		// Ephemeral secret: tokens are valid only for this process lifetime.
		// Set BEEHIVE_SESSION_SECRET in production so sessions survive restarts.
		b = make([]byte, 32)
		_, _ = rand.Read(b)
	}
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return &SessionManager{secret: b, ttl: ttl}
}

func (s *SessionManager) sign(body string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue mints a signed token for the given identity.
func (s *SessionManager) Issue(id Identity) (string, error) {
	payload, err := json.Marshal(sessionClaims{
		UserID: id.UserID, OrgID: id.OrgID, Email: id.Email, Role: id.Role,
		Exp: time.Now().Add(s.ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body), nil
}

// Verify implements SessionVerifier.
func (s *SessionManager) Verify(_ context.Context, token string) (Identity, error) {
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
	return Identity{UserID: c.UserID, OrgID: c.OrgID, Email: c.Email, Role: c.Role}, nil
}

// IdentityFromRequest reads and verifies the session from a plain HTTP request
// (Authorization: Bearer header or the session cookie). Used by the REST-style
// onboarding/tenant handlers (the Connect interceptor covers the RPC services).
func (s *SessionManager) IdentityFromRequest(r *http.Request) (Identity, bool) {
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		if c, err := r.Cookie(SessionCookie); err == nil {
			token = c.Value
		}
	}
	if token == "" {
		return Identity{}, false
	}
	id, err := s.Verify(r.Context(), token)
	if err != nil {
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

// LogoutHandler clears the session cookie.
func (s *SessionManager) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusNoContent)
	}
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
