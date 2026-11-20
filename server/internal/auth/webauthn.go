package auth

import (
	"container/list"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// WebAuthn implements passkeys bound to registered accounts: enrollment
// requires an authenticated session and attaches the credential to users.id;
// login is usernameless (discoverable credential) and issues a session with
// the account's stored role and resolved tenant, exactly like a password
// sign-in, never more.
type WebAuthn struct {
	wa       *webauthn.WebAuthn
	db       *sqlx.DB
	users    storage.UserRepo
	prov     *Provisioner
	sessions *SessionManager
	cfg      *config.Config
	throttle *Throttle
	pending  *challengeStore
	reauth   *ReauthStore
}

type waChallenge struct {
	data  *webauthn.SessionData
	label string
}

// Passkey ceremonies finish within seconds; a challenge nobody finished is
// dropped after waChallengeTTL, and at most waChallengeMax are kept, so the
// public login/begin endpoint cannot be used to fill the server's memory.
const (
	waChallengeTTL = 5 * time.Minute
	waChallengeMax = 10_000
)

// challengeStore keeps begun ceremonies in memory until they are finished,
// expire, or are pushed out by newer ones once the store is full.
type challengeStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	now   func() time.Time
	byID  map[string]*list.Element
	order *list.List // oldest first; one TTL for all, so also expiry order
}

type challengeEntry struct {
	id      string
	c       *waChallenge
	expires time.Time
}

func newChallengeStore(ttl time.Duration, limit int) *challengeStore {
	return &challengeStore{ttl: ttl, max: limit, now: time.Now, byID: map[string]*list.Element{}, order: list.New()}
}

func (s *challengeStore) put(id string, c *waChallenge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for e := s.order.Front(); e != nil; e = s.order.Front() {
		entry := e.Value.(*challengeEntry)
		if now.Before(entry.expires) && len(s.byID) < s.max {
			break
		}
		s.order.Remove(e)
		delete(s.byID, entry.id)
	}
	s.byID[id] = s.order.PushBack(&challengeEntry{id: id, c: c, expires: now.Add(s.ttl)})
}

// take returns a pending challenge exactly once.
func (s *challengeStore) take(id string) (*waChallenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	entry := s.order.Remove(e).(*challengeEntry)
	delete(s.byID, id)
	if !s.now().Before(entry.expires) {
		return nil, false
	}
	return entry.c, true
}

func (s *challengeStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

func NewWebAuthn(cfg *config.Config, db *sqlx.DB, users storage.UserRepo, prov *Provisioner, sessions *SessionManager, throttle *Throttle, reauth *ReauthStore) (*WebAuthn, error) {
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.Auth.WebAuthn.DisplayName,
		RPID:          cfg.Auth.WebAuthn.RPID,
		RPOrigins:     cfg.Auth.WebAuthn.RPOrigins,
	})
	if err != nil {
		return nil, err
	}
	return &WebAuthn{
		wa: w, db: db, users: users, prov: prov, sessions: sessions, cfg: cfg, throttle: throttle,
		pending: newChallengeStore(waChallengeTTL, waChallengeMax), reauth: reauth,
	}, nil
}

func (a *WebAuthn) Routes(rt *Router) {
	// Session-gated: manage the signed-in account's passkeys. The finish
	// bodies are parsed by the WebAuthn library, and the ceremony itself is
	// bound to the origin, so they are not held to the JSON content type.
	rt.Post("/auth/webauthn/enroll/begin", a.enrollBegin)
	rt.Post("/auth/webauthn/enroll/finish", a.enrollFinish)
	rt.Get("/auth/webauthn/credentials", a.listCredentials)
	rt.PostJSON("/auth/webauthn/credentials/delete", a.deleteCredential)
	// Session-gated: confirm it is still the account's owner (reauth.go).
	rt.Post("/auth/webauthn/reauth/begin", a.reauthBegin)
	rt.Post("/auth/webauthn/reauth/finish", a.reauthFinish)
	// Public: usernameless login with a discoverable credential.
	rt.Post("/auth/webauthn/login/begin", a.loginBegin)
	rt.Post("/auth/webauthn/login/finish", a.loginFinish)
}

// --- webauthn.User adapter over a registered account ---

type waUser struct {
	u     *storage.User
	creds []webauthn.Credential
}

func (x *waUser) WebAuthnID() []byte   { return []byte(x.u.ID) }
func (x *waUser) WebAuthnName() string { return x.u.Email }
func (x *waUser) WebAuthnDisplayName() string {
	if x.u.Name != "" {
		return x.u.Name
	}
	return x.u.Email
}
func (x *waUser) WebAuthnCredentials() []webauthn.Credential { return x.creds }

// --- store ---

type passkeyRow struct {
	ID         string     `db:"id" json:"id"`
	Name       string     `db:"name" json:"name"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at"`
}

func (a *WebAuthn) credentials(r *http.Request, userID string) []webauthn.Credential {
	var rows []string
	_ = a.db.SelectContext(r.Context(), &rows, a.db.Rebind(
		`SELECT cred FROM user_passkey WHERE user_id = ?`), userID)
	out := make([]webauthn.Credential, 0, len(rows))
	for _, s := range rows {
		var c webauthn.Credential
		if json.Unmarshal([]byte(s), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

func credID(c *webauthn.Credential) string {
	return base64.RawURLEncoding.EncodeToString(c.ID)
}

func (a *WebAuthn) addCredential(r *http.Request, userID, label string, cred *webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(r.Context(), a.db.Rebind(
		`INSERT INTO user_passkey (id, user_id, name, cred, created_at) VALUES (?, ?, ?, ?, ?)`),
		credID(cred), userID, label, string(raw), time.Now().UTC())
	return err
}

// touchCredential persists the post-login credential state (sign counter,
// backup flags) and stamps last_used_at.
func (a *WebAuthn) touchCredential(r *http.Request, cred *webauthn.Credential) {
	raw, err := json.Marshal(cred)
	if err != nil {
		return
	}
	_, _ = a.db.ExecContext(r.Context(), a.db.Rebind(
		`UPDATE user_passkey SET cred = ?, last_used_at = ? WHERE id = ?`),
		string(raw), time.Now().UTC(), credID(cred))
}

// --- challenge state (begin -> finish), in-memory keyed by a short cookie ---

const waChallengeCookie = "obh_wa"

func (a *WebAuthn) storeChallenge(w http.ResponseWriter, c *waChallenge) {
	id := randToken()
	a.pending.put(id, c)
	http.SetCookie(w, &http.Cookie{
		Name: waChallengeCookie, Value: id, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: true, MaxAge: int(waChallengeTTL.Seconds()),
	})
}

func (a *WebAuthn) loadChallenge(r *http.Request) (*waChallenge, bool) {
	ck, err := r.Cookie(waChallengeCookie)
	if err != nil {
		return nil, false
	}
	return a.pending.take(ck.Value)
}

// --- enrollment (session required) ---

// identity authenticates the request and refuses read-only demo sessions:
// the demo account is shared, so letting visitors attach passkeys to it would
// hand out permanent demo-tenant access tokens.
func (a *WebAuthn) identity(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := a.sessions.Authenticate(w, r)
	if !ok {
		return Identity{}, false
	}
	if id.Role == "demo" {
		respondError(w, http.StatusForbidden, "demo_read_only", "not available for the demo account")
		return Identity{}, false
	}
	return id, true
}

func (a *WebAuthn) enrollBegin(w http.ResponseWriter, r *http.Request) {
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	u, err := a.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		notSignedIn(w) // the account behind the session is gone
		return
	}
	user := &waUser{u: u, creds: a.credentials(r, u.ID)}

	// Passkeys must be discoverable so login can be usernameless; exclude the
	// already-enrolled credentials so authenticators refuse duplicates.
	exclude := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for i := range user.creds {
		exclude = append(exclude, user.creds[i].Descriptor())
	}
	options, session, err := a.wa.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		internalError(w, "passkey enrollment", err)
		return
	}
	label := strings.TrimSpace(r.URL.Query().Get("name"))
	a.storeChallenge(w, &waChallenge{data: session, label: label})
	respondJSON(w, http.StatusOK, options)
}

func (a *WebAuthn) enrollFinish(w http.ResponseWriter, r *http.Request) {
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	ch, ok := a.loadChallenge(r)
	if !ok {
		respondError(w, http.StatusBadRequest, "passkey_no_ceremony", "no passkey enrollment in progress; start again")
		return
	}
	// The challenge must belong to the session that finishes it.
	if string(ch.data.UserID) != id.UserID {
		respondError(w, http.StatusForbidden, "passkey_session_mismatch", "this enrollment was started by a different account")
		return
	}
	u, err := a.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		notSignedIn(w) // the account behind the session is gone
		return
	}
	user := &waUser{u: u, creds: a.credentials(r, u.ID)}
	cred, err := a.wa.FinishRegistration(user, *ch.data, r)
	if err != nil {
		log.Printf("webauthn: enrollment rejected: %v", err)
		respondError(w, http.StatusBadRequest, "passkey_failed", "the passkey could not be registered; try again")
		return
	}
	if err := a.addCredential(r, u.ID, ch.label, cred); err != nil {
		internalError(w, "store passkey", err)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"status": "ok", "id": credID(cred), "name": ch.label})
}

func (a *WebAuthn) listCredentials(w http.ResponseWriter, r *http.Request) {
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	rows := []passkeyRow{}
	if err := a.db.SelectContext(r.Context(), &rows, a.db.Rebind(
		`SELECT id, name, created_at, last_used_at FROM user_passkey WHERE user_id = ? ORDER BY created_at`), id.UserID); err != nil {
		internalError(w, "list passkeys", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"passkeys": rows})
}

func (a *WebAuthn) deleteCredential(w http.ResponseWriter, r *http.Request) {
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID == "" {
		respondError(w, http.StatusBadRequest, "id_required", "credential id required")
		return
	}
	// Scoped to the caller: nobody deletes another account's passkeys.
	res, err := a.db.ExecContext(r.Context(), a.db.Rebind(
		`DELETE FROM user_passkey WHERE id = ? AND user_id = ?`), req.ID, id.UserID)
	if err != nil {
		internalError(w, "delete passkey", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		respondError(w, http.StatusNotFound, "passkey_not_found", "unknown passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- login (public, usernameless) ---

func (a *WebAuthn) loginBegin(w http.ResponseWriter, r *http.Request) {
	if !a.throttle.Allow(w, r, "") {
		return
	}
	options, session, err := a.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		internalError(w, "passkey login", err)
		return
	}
	a.storeChallenge(w, &waChallenge{data: session})
	respondJSON(w, http.StatusOK, options)
}

func (a *WebAuthn) loginFinish(w http.ResponseWriter, r *http.Request) {
	if !a.throttle.Allow(w, r, "") {
		return
	}
	ch, ok := a.loadChallenge(r)
	if !ok {
		respondError(w, http.StatusBadRequest, "passkey_no_ceremony", "no passkey sign-in in progress; start again")
		return
	}
	var account *storage.User
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := a.users.GetByID(r.Context(), string(userHandle))
		if err != nil {
			return nil, err
		}
		account = u
		return &waUser{u: u, creds: a.credentials(r, u.ID)}, nil
	}
	cred, err := a.wa.FinishDiscoverableLogin(handler, *ch.data, r)
	if err != nil || account == nil {
		a.throttle.Fail(r, "")
		respondError(w, http.StatusUnauthorized, "passkey_failed", "passkey sign-in failed")
		return
	}
	// Same gates and shape as a password sign-in.
	if a.cfg.Auth.EmailVerification && !account.EmailVerified {
		respondJSON(w, http.StatusForbidden, map[string]any{
			"error": "please verify your email first", "code": "email_unverified", "status": "unverified"})
		return
	}
	role := account.Role
	if isDemoAccount(a.cfg, account.Email) {
		role = "demo"
	}
	org := a.prov.ResolveActiveOrg(r.Context(), account)
	tok, err := a.sessions.Issue(Identity{UserID: account.ID, OrgID: org, Email: account.Email, Role: role})
	if err != nil {
		internalError(w, "issue session", err)
		return
	}
	a.sessions.SetCookie(w, tok)
	a.touchCredential(r, cred)
	respondJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "admin": role == "admin", "name": account.Name, "email": account.Email,
		"token": tok, "user_id": account.ID, "active_org": org,
	})
}
