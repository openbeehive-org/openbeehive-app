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
	// LoginAt (Unix milliseconds) is set only on the token a sign-in hands
	// out; see Identity.LoginAt.
	LoginAt int64 `json:"lat,omitempty"`
}

// sessionStateTTL is how long a verified (account, workspace) pair is trusted
// before the database is asked again. It bounds how long a removed member or
// a session revoked by another process path keeps working.
const sessionStateTTL = 10 * time.Second

// sessionStateMax caps the cache; past it the cache starts over.
const sessionStateMax = 10_000

type sessionState struct {
	validAfter int64  // users.sessions_valid_after
	member     bool   // the account belongs to the workspace
	role       string // users.role
	fetched    time.Time
}

// RevocationStore keeps the sessions ended by signing out, by the SHA-256
// hash of their token, until the token expires.
type RevocationStore interface {
	// RevokeSession records one ended session and deletes the records of
	// tokens that have expired.
	RevokeSession(ctx context.Context, tokenHash string, expiresAt time.Time) error
	// RevokedSessions returns the records of tokens that expire after now.
	RevokedSessions(ctx context.Context, now time.Time) (map[string]time.Time, error)
}

// SessionManager issues and verifies compact HMAC-signed app-session tokens
// (a minimal JWS: base64url(claims).base64url(HMAC-SHA256)). Provider-
// independent: password, OIDC and WebAuthn sign-ins all mint sessions through
// it.
//
// A valid signature is not enough: every verification also checks that the
// session was not ended by signing out, that the account still exists, that
// the token was issued after the account's "sessions valid after" mark (see
// RevokeAll), and that the account is still a member of the workspace the
// token is for. The account lookups are cached for sessionStateTTL; the
// sessions ended by signing out are all held in memory.
type SessionManager struct {
	secret  []byte
	ttl     time.Duration
	users   storage.UserRepo
	members storage.MemberRepo
	// revocations saves the sessions ended by signing out; nil keeps them in
	// memory only (see UseRevocations).
	revocations RevocationStore

	mu    sync.Mutex
	state map[string]sessionState // "<user>\x00<org>"
	epoch int                     // bumped by Invalidate; stale lookups are not cached
	// floor is the latest revocation mark (Unix ms). Tokens are never
	// stamped before it: the mark lies a millisecond ahead of the clock, and
	// a session issued after the revocation must not count as revoked.
	floor int64
	// signedOut holds every session ended by signing out whose token has not
	// expired yet, by token hash (see hashKey), with the moment the token
	// expires. Only one server instance runs, so this copy of the table is
	// complete and a request never has to ask the database.
	signedOut map[string]time.Time
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
		state: map[string]sessionState{}, signedOut: map[string]time.Time{},
	}, nil
}

// UseRevocations makes signing out last across restarts: it loads the
// sessions that were ended and have not expired yet, and saves every
// further one in the store. Call it once at start, before the first request.
func (s *SessionManager) UseRevocations(ctx context.Context, store RevocationStore) error {
	ended, err := store.RevokedSessions(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("load the sessions ended by signing out: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, expires := range ended {
		s.signedOut[hash] = expires
	}
	s.revocations = store
	return nil
}

func (s *SessionManager) sign(body string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue mints a signed token for the given identity: a session that follows
// from an existing one (another workspace, a revocation, a recovery).
func (s *SessionManager) Issue(id Identity) (string, error) {
	return s.issueAt(id, time.Now(), false)
}

// IssueLogin mints the token for a user who has just signed in. Only such a
// token counts as proof of a recent sign-in (see RecentLogin).
func (s *SessionManager) IssueLogin(id Identity) (string, error) {
	return s.issueAt(id, time.Now(), true)
}

// RecentLoginWindow is how long after a sign-in its session confirms
// actions that need the user to prove they are present.
const RecentLoginWindow = 5 * time.Minute

// RecentLogin reports whether the session came straight out of a sign-in
// that happened within RecentLoginWindow.
func RecentLogin(id Identity, now time.Time) bool {
	return !id.LoginAt.IsZero() && !now.Before(id.LoginAt) && now.Sub(id.LoginAt) <= RecentLoginWindow
}

func (s *SessionManager) issueAt(id Identity, at time.Time, login bool) (string, error) {
	s.mu.Lock()
	if floor := time.UnixMilli(s.floor); at.Before(floor) {
		at = floor
	}
	s.mu.Unlock()
	claims := sessionClaims{
		UserID: id.UserID, OrgID: id.OrgID, Email: id.Email, Role: id.Role,
		Exp: at.Add(s.ttl).Unix(), IssuedAt: at.UnixMilli(),
	}
	if login {
		claims.LoginAt = at.UnixMilli()
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body), nil
}

var (
	errSessionRevoked = errors.New("session revoked; sign in again")
	errNotMember      = errors.New("no longer a member of this workspace; continue in another one (POST /auth/session/recover) or sign in again")
	// errSessionUnverifiable means the session could not be checked (database
	// trouble), not that it is invalid: callers answer "try again", never
	// "signed out".
	errSessionUnverifiable = errors.New("the session could not be verified right now; try again")
)

// Verify implements SessionVerifier.
func (s *SessionManager) Verify(ctx context.Context, token string) (Identity, error) {
	id, member, err := s.verifyAccount(ctx, token)
	if err != nil {
		return Identity{}, err
	}
	if !member {
		return Identity{}, errNotMember
	}
	return id, nil
}

// verifyAccount checks everything about a session except membership: the
// signature, the expiry, whether it was ended by signing out and the
// account's revocation mark. member reports whether the account still
// belongs to the workspace the token is for; a session that fails only on
// that can be moved to another workspace (see AccountAPI.recoverSession).
func (s *SessionManager) verifyAccount(ctx context.Context, token string) (id Identity, member bool, err error) {
	c, err := s.parse(token)
	if err != nil {
		return Identity{}, false, err
	}
	if s.isSignedOut(token) {
		return Identity{}, false, errSessionRevoked
	}
	st, err := s.lookupState(ctx, c.UserID, c.OrgID)
	if err != nil {
		return Identity{}, false, err
	}
	if c.IssuedAt < st.validAfter {
		return Identity{}, false, errSessionRevoked
	}
	// The account role comes from the database, not from the token, so an
	// admin who is demoted loses the rights at once. A demo session stays
	// one: that role is given at sign-in, it is not stored.
	role := st.role
	if c.Role == "demo" {
		role = "demo"
	}
	id = Identity{UserID: c.UserID, OrgID: c.OrgID, Email: c.Email, Role: role}
	if c.LoginAt > 0 {
		id.LoginAt = time.UnixMilli(c.LoginAt)
	}
	return id, st.member, nil
}

// parse checks the token's signature and expiry and returns its claims. It
// says nothing about the account or whether the session has been ended.
func (s *SessionManager) parse(token string) (sessionClaims, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return sessionClaims{}, errors.New("malformed session token")
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(body))) {
		return sessionClaims{}, errors.New("invalid session signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return sessionClaims{}, err
	}
	var c sessionClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return sessionClaims{}, err
	}
	if time.Now().Unix() > c.Exp {
		return sessionClaims{}, errors.New("session expired")
	}
	return c, nil
}

// expires returns the moment from which the token is refused as expired. The
// exp claim is in seconds, and parse accepts the token for the whole of that
// second.
func (c sessionClaims) expires() time.Time { return time.Unix(c.Exp+1, 0) }

// isSignedOut reports whether the session was ended by signing out.
func (s *SessionManager) isSignedOut(token string) bool {
	hash := hashKey(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.signedOut[hash]
	return ok
}

// signOut ends one session: from now on its token is refused, by this
// process at once and, once saved, after a restart too. Records of tokens
// that have expired are dropped on the way, here and in the store.
//
// The token is revoked in memory before it is saved, and stays revoked there
// when saving fails: the user has signed out, and refusing the token for as
// long as this process runs is better than not at all. Only a restart before
// the token expires would let it in again; the error tells the operator.
func (s *SessionManager) signOut(ctx context.Context, token string, expires time.Time) error {
	hash := hashKey(token)
	now := time.Now()
	s.mu.Lock()
	_, known := s.signedOut[hash]
	s.signedOut[hash] = expires
	for h, exp := range s.signedOut {
		if !now.Before(exp) {
			delete(s.signedOut, h)
		}
	}
	store := s.revocations
	s.mu.Unlock()
	// A session this process has ended before is saved already, or its
	// failure was logged then; writing once also keeps two sign-outs with
	// one token from racing for the same row.
	if known || store == nil {
		return nil
	}
	return store.RevokeSession(ctx, hash, expires)
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
	st = sessionState{validAfter: u.SessionsValidAfter, role: u.Role, fetched: now}
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
	mark, err := s.revoke(ctx, id.UserID)
	if err != nil {
		return "", err
	}
	return s.issueAt(id, mark, false)
}

// RevokeEvery ends every session of the account without exception (after a
// password reset: whoever asked for it holds no session worth keeping).
func (s *SessionManager) RevokeEvery(ctx context.Context, userID string) error {
	_, err := s.revoke(ctx, userID)
	return err
}

// revoke moves the account's revocation mark to now and returns it.
func (s *SessionManager) revoke(ctx context.Context, userID string) (time.Time, error) {
	// One millisecond ahead: a token issued in this very millisecond, before
	// the revocation, must not survive it. Tokens may already be stamped at
	// an earlier mark (the floor), so the new mark also lies beyond that.
	s.mu.Lock()
	mark := time.Now().Add(time.Millisecond)
	if floor := time.UnixMilli(s.floor + 1); mark.Before(floor) {
		mark = floor
	}
	s.floor = mark.UnixMilli()
	s.mu.Unlock()
	if err := s.users.SetSessionsValidAfter(ctx, userID, mark.UnixMilli()); err != nil {
		return time.Time{}, err
	}
	s.Invalidate()
	return mark, nil
}

// Authenticate reads and verifies the session of a plain HTTP request
// (Authorization: Bearer header or the session cookie). When there is no
// valid session it writes the error response and returns false. The Connect
// interceptor does the same for the RPC services.
func (s *SessionManager) Authenticate(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, member, ok := s.authenticateAccount(w, r)
	if ok && !member {
		notSignedIn(w)
		return Identity{}, false
	}
	return id, ok
}

// authenticateAccount is Authenticate without the membership requirement:
// member reports whether the account still belongs to the session's
// workspace.
func (s *SessionManager) authenticateAccount(w http.ResponseWriter, r *http.Request) (id Identity, member, ok bool) {
	token := requestToken(r)
	if token == "" {
		notSignedIn(w)
		return Identity{}, false, false
	}
	id, member, err := s.verifyAccount(r.Context(), token)
	switch {
	case errors.Is(err, errSessionUnverifiable):
		respondError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return Identity{}, false, false
	case err != nil:
		notSignedIn(w)
		return Identity{}, false, false
	}
	return id, member, true
}

// requestToken returns the session token of a plain HTTP request: the bearer
// header, else the session cookie.
func requestToken(r *http.Request) string {
	if token := bearer(r.Header.Get("Authorization")); token != "" {
		return token
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		return c.Value
	}
	return ""
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

// logout ends the caller's session, the one whose token the request carries
// (bearer header, else cookie), and clears the cookie. The account's other
// sessions stay.
//
// A demo session is not recorded, only its cookie cleared: the demo is
// read-only and the same for every visitor, so its token gives nothing away,
// and anyone can fetch any number of them (/auth/demo-login), which would let
// the list of ended sessions grow without bound.
//
// Signing out never fails for the user: the app forgets its session whatever
// the answer, so it is always 204. Without a token, or with one that is not
// valid any more, there is nothing to end. The account behind a valid token
// is not looked up: a session that has lost its workspace can still be moved
// to another one (AccountAPI.recoverSession), and one the database cannot
// check right now must end all the same.
func (s *SessionManager) logout(w http.ResponseWriter, r *http.Request) {
	if token := requestToken(r); token != "" {
		if c, err := s.parse(token); err == nil && c.Role != "demo" {
			// Saved even when the client stops waiting for the answer.
			if err := s.signOut(context.WithoutCancel(r.Context()), token, c.expires()); err != nil {
				log.Printf("auth: sign-out not saved; the session stays ended until the server restarts: %v", err)
			}
		}
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
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
			// A stream outlives the check made here; Recheck repeats it.
			h := info.RequestHeader()
			ctx = context.WithValue(ctx, recheckKey{}, func(c context.Context) error {
				_, err := verifyHeaders(c, v, h)
				return err
			})
			return next(WithIdentity(ctx, id), spec, stream)
		}
	}
}

type recheckKey struct{}

// Recheck verifies the request's credentials again, for a stream that stays
// open long after the request was authenticated: a member removed from the
// workspace, or a revoked session, must not keep receiving its updates. It
// returns nil when the request carries nothing to check (no sign-in
// required), and an error for which Unverifiable is true when the check
// could not be made right now.
func Recheck(ctx context.Context) error {
	f, ok := ctx.Value(recheckKey{}).(func(context.Context) error)
	if !ok {
		return nil
	}
	return f(ctx)
}

// Unverifiable reports a credential check that failed for lack of a working
// database, not because the credentials are invalid.
func Unverifiable(err error) bool { return errors.Is(err, errSessionUnverifiable) }

// WithRecheck makes Recheck call f, for callers that do not come in over
// SessionInterceptor (tests).
func WithRecheck(ctx context.Context, f func(context.Context) error) context.Context {
	return context.WithValue(ctx, recheckKey{}, f)
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
