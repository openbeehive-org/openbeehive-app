// Package storage defines backend-independent repository interfaces.
// The SQL implementation (Postgres/MySQL/SQLite) lives in storage/sql,
// the blob implementations (MinIO/FS) in storage/blob.
package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrNotFound = errors.New("not found")

// ErrLastOwner is returned when a change would leave a workspace that has
// members without an owner.
var ErrLastOwner = errors.New("a workspace needs at least one owner")

// NormalizeEmail returns the form in which email addresses are stored and
// compared: trimmed and lower case.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// --- Domain models (DB view, decoupled from protobuf) ---

type Apiary struct {
	ID        string    `db:"id"`
	OrgID     string    `db:"organization_id"`
	Name      string    `db:"name"`
	Address   string    `db:"address"`
	Lat       float64   `db:"lat"`
	Lng       float64   `db:"lng"`
	Note      string    `db:"note"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type Hive struct {
	ID           string    `db:"id"`
	OrgID        string    `db:"organization_id"`
	ApiaryID     string    `db:"apiary_id"`
	Name         string    `db:"name"`
	Type         int32     `db:"type"`
	Status       int32     `db:"status"`
	Boxes        int32     `db:"boxes"`
	ColonyOrigin string    `db:"colony_origin"`
	Note         string    `db:"note"`
	QRCode       string    `db:"qr_code"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// User is an account on the instance (email/password onboarding). The
// instance admin is a dedicated account defined by BEEHIVE_ADMIN_EMAIL /
// BEEHIVE_ADMIN_PASSWORD and enforced at boot (auth.EnsureAdmin).
type User struct {
	ID            string    `db:"id"`
	Email         string    `db:"email"`
	Name          string    `db:"name"`
	OIDCSubject   string    `db:"oidc_subject"`
	PasswordHash  string    `db:"password_hash"`
	Role          string    `db:"role"` // admin | user
	EmailVerified bool      `db:"email_verified"`
	VerifyToken   string    `db:"verification_token"`
	CreatedAt     time.Time `db:"created_at"`
	// SessionsValidAfter (Unix milliseconds) invalidates every session token
	// issued before it; 0 means no session has been revoked.
	SessionsValidAfter int64 `db:"sessions_valid_after"`
}

// Organization is a tenant: a collection of apiaries/hives that members share.
// A user has a personal tenant and may belong to others (e.g. a club).
type Organization struct {
	ID        string    `db:"id"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

// Membership links a user to a tenant with a tenant role (owner = tenant admin).
type Membership struct {
	OrgID  string `db:"organization_id"`
	UserID string `db:"benutzer_id"`
	Role   string `db:"role"` // owner | member
}

// TenantMembership is a user's membership joined with the tenant name (for UI).
type TenantMembership struct {
	OrgID string `db:"organization_id"`
	Name  string `db:"name"`
	Role  string `db:"role"`
}

// Member is one member of a tenant with the account details the member list
// shows.
type Member struct {
	UserID string `db:"user_id"`
	Name   string `db:"name"`
	Email  string `db:"email"`
	Role   string `db:"role"`
}

// Invite lets a tenant admin invite someone to their tenant by email.
type Invite struct {
	ID        string    `db:"id"`
	OrgID     string    `db:"organization_id"`
	Email     string    `db:"email"`
	Role      string    `db:"role"`
	Token     string    `db:"token"`
	CreatedAt time.Time `db:"created_at"`
	// ExpiresAt is nil for invites stored by 0.4.x servers; Expiry applies
	// InviteTTL from CreatedAt for those.
	ExpiresAt *time.Time `db:"expires_at"`
}

// InviteTTL is how long an invite stays usable.
const InviteTTL = 7 * 24 * time.Hour

// Expiry returns the instant after which the invite can no longer be used.
func (i *Invite) Expiry() time.Time {
	if i.ExpiresAt != nil {
		return *i.ExpiresAt
	}
	return i.CreatedAt.Add(InviteTTL)
}

// Expired reports whether the invite can no longer be used at the given time.
func (i *Invite) Expired(now time.Time) bool { return !now.Before(i.Expiry()) }

// --- Repository interfaces ---

type UserRepo interface {
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id string) (*User, error)
	GetBySubject(ctx context.Context, oidcSubject string) (*User, error)
	GetByVerifyToken(ctx context.Context, token string) (*User, error)
	Create(ctx context.Context, u *User) error
	MarkVerified(ctx context.Context, id string) error
	LinkOIDC(ctx context.Context, id, oidcSubject string) error
	// SetCredentials overwrites password hash and role and marks the account
	// verified. Used by the instance-admin bootstrap (env is authoritative).
	SetCredentials(ctx context.Context, id, passwordHash, role string) error
	// SetSessionsValidAfter revokes every session token of the account that
	// was issued before the given instant (Unix milliseconds).
	SetSessionsValidAfter(ctx context.Context, id string, unixMilli int64) error
	// SetPassword replaces the password hash of the account.
	SetPassword(ctx context.Context, id, passwordHash string) error
}

type OrgRepo interface {
	Create(ctx context.Context, o *Organization) error
	Get(ctx context.Context, id string) (*Organization, error)
	// Delete removes the tenant and every row scoped to it (memberships,
	// invites, API keys, apiaries, hives and all records) in one transaction.
	Delete(ctx context.Context, id string) error
	// PhotoKeysByOrg returns every blob key the tenant's inspections have ever
	// referenced, including OR-Set-removed ones, whose blobs also linger.
	// Used to purge the blob store when the tenant is deleted.
	PhotoKeysByOrg(ctx context.Context, id string) ([]string, error)
}

type MemberRepo interface {
	Add(ctx context.Context, m *Membership) error
	Get(ctx context.Context, orgID, userID string) (*Membership, error)
	ListByUser(ctx context.Context, userID string) ([]TenantMembership, error)
	// ListByOrg returns the tenant's members, owners first.
	ListByOrg(ctx context.Context, orgID string) ([]Member, error)
	// Remove ends a membership and deletes the member's API keys for the
	// tenant. ErrLastOwner when the member is the tenant's only owner.
	Remove(ctx context.Context, orgID, userID string) error
	// SetRole changes a member's role (owner | member). ErrLastOwner when it
	// would demote the tenant's only owner.
	SetRole(ctx context.Context, orgID, userID, role string) error
}

type InviteRepo interface {
	Create(ctx context.Context, i *Invite) error
	GetByToken(ctx context.Context, token string) (*Invite, error)
	ListByOrg(ctx context.Context, orgID string) ([]Invite, error)
	Delete(ctx context.Context, id string) error
}

// ApiaryRepo and HiveRepo are read-only: every write to a synced entity goes
// through the service writer, which merges it and appends it to the change
// log. List returns one page and the total number of matching rows.
type ApiaryRepo interface {
	Get(ctx context.Context, orgID, id string) (*Apiary, error)
	List(ctx context.Context, orgID string, limit, offset int) ([]Apiary, int, error)
	HiveCount(ctx context.Context, orgID, apiaryID string) (int, error)
}

type HiveRepo interface {
	Get(ctx context.Context, orgID, id string) (*Hive, error)
	List(ctx context.Context, orgID, apiaryID string, limit, offset int) ([]Hive, int, error)
}

// Store bundles all repositories plus migrations/lifecycle.
type Store interface {
	Apiaries() ApiaryRepo
	Hives() HiveRepo
	Users() UserRepo
	Orgs() OrgRepo
	Members() MemberRepo
	Invites() InviteRepo
	Migrate(ctx context.Context) error
	Close() error
}
