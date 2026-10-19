package auth

import (
	"encoding/base64"
	"encoding/json"
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
	pending  sync.Map // challenge id -> *waChallenge
}

type waChallenge struct {
	data  *webauthn.SessionData
	label string
}

func NewWebAuthn(cfg *config.Config, db *sqlx.DB, users storage.UserRepo, prov *Provisioner, sessions *SessionManager) (*WebAuthn, error) {
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.Auth.WebAuthn.DisplayName,
		RPID:          cfg.Auth.WebAuthn.RPID,
		RPOrigins:     cfg.Auth.WebAuthn.RPOrigins,
	})
	if err != nil {
		return nil, err
	}
	return &WebAuthn{wa: w, db: db, users: users, prov: prov, sessions: sessions, cfg: cfg}, nil
}

func (a *WebAuthn) Routes(mux *http.ServeMux) {
	// Session-gated: manage the signed-in account's passkeys.
	mux.HandleFunc("/auth/webauthn/enroll/begin", a.enrollBegin)
	mux.HandleFunc("/auth/webauthn/enroll/finish", a.enrollFinish)
	mux.HandleFunc("/auth/webauthn/credentials", a.listCredentials)
	mux.HandleFunc("/auth/webauthn/credentials/delete", a.deleteCredential)
	// Public: usernameless login with a discoverable credential.
	mux.HandleFunc("/auth/webauthn/login/begin", a.loginBegin)
	mux.HandleFunc("/auth/webauthn/login/finish", a.loginFinish)
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
	a.pending.Store(id, c)
	http.SetCookie(w, &http.Cookie{
		Name: waChallengeCookie, Value: id, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: true, MaxAge: 300,
	})
}

func (a *WebAuthn) loadChallenge(r *http.Request) (*waChallenge, bool) {
	ck, err := r.Cookie(waChallengeCookie)
	if err != nil {
		return nil, false
	}
	v, ok := a.pending.LoadAndDelete(ck.Value)
	if !ok {
		return nil, false
	}
	return v.(*waChallenge), true
}

// --- enrollment (session required) ---

// identity authenticates the request and refuses read-only demo sessions:
// the demo account is shared, so letting visitors attach passkeys to it would
// hand out permanent demo-tenant access tokens.
func (a *WebAuthn) identity(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := a.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign in first"})
		return Identity{}, false
	}
	if id.Role == "demo" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "not available for the demo account"})
		return Identity{}, false
	}
	return id, true
}

func (a *WebAuthn) enrollBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	u, err := a.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "unknown account"})
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
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	label := strings.TrimSpace(r.URL.Query().Get("name"))
	a.storeChallenge(w, &waChallenge{data: session, label: label})
	respondJSON(w, http.StatusOK, options)
}

func (a *WebAuthn) enrollFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	ch, ok := a.loadChallenge(r)
	if !ok {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "no enrollment in progress"})
		return
	}
	// The challenge must belong to the session that finishes it.
	if string(ch.data.UserID) != id.UserID {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "enrollment session mismatch"})
		return
	}
	u, err := a.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "unknown account"})
		return
	}
	user := &waUser{u: u, creds: a.credentials(r, u.ID)}
	cred, err := a.wa.FinishRegistration(user, *ch.data, r)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := a.addCredential(r, u.ID, ch.label, cred); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"status": "ok", "id": credID(cred), "name": ch.label})
}

func (a *WebAuthn) listCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	rows := []passkeyRow{}
	if err := a.db.SelectContext(r.Context(), &rows, a.db.Rebind(
		`SELECT id, name, created_at, last_used_at FROM user_passkey WHERE user_id = ? ORDER BY created_at`), id.UserID); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"passkeys": rows})
}

func (a *WebAuthn) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "credential id required"})
		return
	}
	// Scoped to the caller: nobody deletes another account's passkeys.
	res, err := a.db.ExecContext(r.Context(), a.db.Rebind(
		`DELETE FROM user_passkey WHERE id = ? AND user_id = ?`), req.ID, id.UserID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		respondJSON(w, http.StatusNotFound, map[string]any{"error": "unknown passkey"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- login (public, usernameless) ---

func (a *WebAuthn) loginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	options, session, err := a.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.storeChallenge(w, &waChallenge{data: session})
	respondJSON(w, http.StatusOK, options)
}

func (a *WebAuthn) loginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ch, ok := a.loadChallenge(r)
	if !ok {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "no login in progress"})
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
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "passkey login failed"})
		return
	}
	// Same gates and shape as a password sign-in.
	if a.cfg.Auth.EmailVerification && !account.EmailVerified {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "please verify your email first", "status": "unverified"})
		return
	}
	role := account.Role
	if a.cfg.Demo.Enabled && strings.EqualFold(account.Email, a.cfg.Demo.Email) {
		role = "demo"
	}
	org := a.prov.ResolveActiveOrg(r.Context(), account)
	tok, err := a.sessions.Issue(Identity{UserID: account.ID, OrgID: org, Email: account.Email, Role: role})
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.sessions.SetCookie(w, tok)
	a.touchCredential(r, cred)
	respondJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "admin": role == "admin", "name": account.Name, "email": account.Email,
		"token": tok, "user_id": account.ID, "active_org": org,
	})
}
