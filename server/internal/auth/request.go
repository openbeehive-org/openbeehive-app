package auth

import (
	"errors"
	"net/http"
)

// RequestIdentity authenticates a plain HTTP request the way the RPC
// interceptor does: a session token or an API key, taken from the
// Authorization: Bearer header or the session cookie. It serves endpoints
// that are mounted outside the Router. status is 0 on success; otherwise it
// is the HTTP status to answer with: 401 when there is no valid credential,
// 503 when the credential could not be checked right now.
func RequestIdentity(r *http.Request, v SessionVerifier) (id Identity, status int) {
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		if c, err := r.Cookie(SessionCookie); err == nil {
			token = c.Value
		}
	}
	if token == "" {
		return Identity{}, http.StatusUnauthorized
	}
	id, err := v.Verify(r.Context(), token)
	switch {
	case errors.Is(err, errSessionUnverifiable):
		return Identity{}, http.StatusServiceUnavailable
	case err != nil:
		return Identity{}, http.StatusUnauthorized
	}
	return id, 0
}

// SameSite reports whether a state-changing request may proceed under the
// Router's cross-site rule, for endpoints mounted outside the Router that
// accept the session cookie.
func (rt *Router) SameSite(r *http.Request) bool { return rt.sameSite(r) }
