package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// apiKeyPrefix marks API keys so the verifier can tell them from session
// tokens on the same Authorization header.
const apiKeyPrefix = "obhk_"

// APIKeys issues, lists and revokes long-lived keys for scripts and
// integrations, and verifies them on RPCs. A key acts as its owner inside
// the tenant it was created in. Keys are managed with a normal session only
// (a key cannot create or revoke keys), and never for the demo account.
type APIKeys struct {
	db       *sqlx.DB
	sessions *SessionManager
	users    storage.UserRepo
	members  storage.MemberRepo
}

func NewAPIKeys(db *sqlx.DB, sessions *SessionManager, users storage.UserRepo, members storage.MemberRepo) *APIKeys {
	return &APIKeys{db: db, sessions: sessions, users: users, members: members}
}

// Routes registers the management endpoints (session-authenticated).
func (k *APIKeys) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/api-keys", k.listOrCreate)
	mux.HandleFunc("/auth/api-keys/delete", k.remove)
}

var (
	errBadAPIKey     = errors.New("invalid API key")
	errExpiredAPIKey = errors.New("API key expired")
)

// Key scopes. A read key may call only Get*/List*/Pull/Subscribe RPCs.
const (
	ScopeWrite = "write"
	ScopeRead  = "read"
)

// maxKeyLifetimeDays caps expires_in_days on creation.
const maxKeyLifetimeDays = 3650

// Verify implements SessionVerifier for both token kinds: API keys by hash
// lookup, everything else through the session manager.
func (k *APIKeys) Verify(ctx context.Context, token string) (Identity, error) {
	if !strings.HasPrefix(token, apiKeyPrefix) {
		return k.sessions.Verify(ctx, token)
	}
	var row struct {
		ID        string     `db:"id"`
		UserID    string     `db:"user_id"`
		OrgID     string     `db:"organization_id"`
		Scope     string     `db:"scope"`
		ExpiresAt *time.Time `db:"expires_at"`
	}
	err := k.db.GetContext(ctx, &row, k.db.Rebind(
		`SELECT id, user_id, organization_id, scope, expires_at FROM api_key WHERE key_hash = ?`), hashKey(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, errBadAPIKey
	}
	if err != nil {
		return Identity{}, err
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now()) {
		return Identity{}, errExpiredAPIKey
	}
	u, err := k.users.GetByID(ctx, row.UserID)
	if err != nil {
		return Identity{}, errBadAPIKey
	}
	// The key dies with the membership: leaving or being removed from the
	// tenant must cut off scripts that were set up while a member.
	if _, err := k.members.Get(ctx, row.OrgID, row.UserID); err != nil {
		return Identity{}, errBadAPIKey
	}
	_, _ = k.db.ExecContext(ctx, k.db.Rebind(`UPDATE api_key SET last_used_at = ? WHERE id = ?`),
		time.Now().UTC(), row.ID)
	return Identity{UserID: u.ID, OrgID: row.OrgID, Email: u.Email, Role: u.Role, ReadOnly: row.Scope == ScopeRead}, nil
}

func hashKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newKeyToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

type apiKeyRow struct {
	ID         string     `db:"id" json:"id"`
	Name       string     `db:"name" json:"name"`
	Prefix     string     `db:"key_prefix" json:"prefix"`
	Scope      string     `db:"scope" json:"scope"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	ExpiresAt  *time.Time `db:"expires_at" json:"expires_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at"`
	// Owner, filled only in the tenant-wide listing.
	UserID    string `db:"user_id" json:"user_id,omitempty"`
	UserEmail string `db:"user_email" json:"user_email,omitempty"`
	Mine      bool   `db:"-" json:"mine"`
}

// canManageTenant reports whether the caller may see and revoke every key
// of the tenant: the tenant owner (Admin in the app) or the instance admin.
func (k *APIKeys) canManageTenant(ctx context.Context, id Identity) bool {
	if id.Role == "admin" {
		return true
	}
	m, err := k.members.Get(ctx, id.OrgID, id.UserID)
	return err == nil && m.Role == "owner"
}

// identity requires a real session (not a key) and refuses the demo account.
func (k *APIKeys) identity(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := k.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return Identity{}, false
	}
	if id.Role == "demo" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": ErrDemoReadOnly.Error()})
		return Identity{}, false
	}
	return id, true
}

func (k *APIKeys) listOrCreate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		k.list(w, r)
	case http.MethodPost:
		k.create(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// list returns the caller's keys for the active tenant, or with ?tenant=1
// every key of the tenant (tenant owner / instance admin only) with the
// owner's email. Plaintext keys are never stored, so only name, prefix and
// timestamps come back.
func (k *APIKeys) list(w http.ResponseWriter, r *http.Request) {
	id, ok := k.identity(w, r)
	if !ok {
		return
	}
	rows := []apiKeyRow{}
	if r.URL.Query().Get("tenant") == "1" {
		if !k.canManageTenant(r.Context(), id) {
			respondJSON(w, http.StatusForbidden, map[string]any{"error": "tenant admin only"})
			return
		}
		if err := k.db.SelectContext(r.Context(), &rows, k.db.Rebind(
			`SELECT k.id, k.name, k.key_prefix, k.scope, k.created_at, k.expires_at, k.last_used_at,
			        k.user_id, COALESCE(u.email, '') AS user_email
			 FROM api_key k LEFT JOIN users u ON u.id = k.user_id
			 WHERE k.organization_id = ? ORDER BY u.email, k.created_at`), id.OrgID); err != nil {
			respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		for i := range rows {
			rows[i].Mine = rows[i].UserID == id.UserID
		}
		respondJSON(w, http.StatusOK, map[string]any{"keys": rows})
		return
	}
	if err := k.db.SelectContext(r.Context(), &rows, k.db.Rebind(
		`SELECT id, name, key_prefix, scope, created_at, expires_at, last_used_at FROM api_key
		 WHERE user_id = ? AND organization_id = ? ORDER BY created_at`), id.UserID, id.OrgID); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for i := range rows {
		rows[i].Mine = true
	}
	respondJSON(w, http.StatusOK, map[string]any{"keys": rows})
}

// create mints a key for the caller in the active tenant and returns the
// plaintext once. Body: {name, scope: "write"|"read", expires_in_days: 0 = never}.
func (k *APIKeys) create(w http.ResponseWriter, r *http.Request) {
	id, ok := k.identity(w, r)
	if !ok {
		return
	}
	var req struct {
		Name          string `json:"name"`
		Scope         string `json:"scope"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	scope := ScopeWrite
	switch req.Scope {
	case "", ScopeWrite:
	case ScopeRead:
		scope = ScopeRead
	default:
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "scope must be \"write\" or \"read\""})
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxKeyLifetimeDays {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "expires_in_days out of range"})
		return
	}
	token, err := newKeyToken()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	now := time.Now().UTC()
	row := apiKeyRow{ID: uuid.NewString(), Name: strings.TrimSpace(req.Name), Prefix: token[:12], Scope: scope, CreatedAt: now}
	if req.ExpiresInDays > 0 {
		exp := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		row.ExpiresAt = &exp
	}
	if _, err := k.db.ExecContext(r.Context(), k.db.Rebind(
		`INSERT INTO api_key (id, user_id, organization_id, name, key_prefix, key_hash, scope, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		row.ID, id.UserID, id.OrgID, row.Name, row.Prefix, hashKey(token), row.Scope, row.CreatedAt, row.ExpiresAt); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id": row.ID, "name": row.Name, "prefix": row.Prefix, "scope": row.Scope,
		"created_at": row.CreatedAt, "expires_at": row.ExpiresAt, "token": token,
	})
}

// remove deletes one of the caller's keys, or any key of the active tenant
// when the caller is its owner or the instance admin; scripts using it fail
// from now on.
func (k *APIKeys) remove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, ok := k.identity(w, r)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "key id required"})
		return
	}
	q, args := `DELETE FROM api_key WHERE id = ? AND user_id = ?`, []any{req.ID, id.UserID}
	if k.canManageTenant(r.Context(), id) {
		q, args = `DELETE FROM api_key WHERE id = ? AND (user_id = ? OR organization_id = ?)`, []any{req.ID, id.UserID, id.OrgID}
	}
	res, err := k.db.ExecContext(r.Context(), k.db.Rebind(q), args...)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		respondJSON(w, http.StatusNotFound, map[string]any{"error": "no such key"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
