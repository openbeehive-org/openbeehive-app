package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// --- Organizations (tenants) ---

type orgRepo struct{ s *Store }

func (r *orgRepo) Create(ctx context.Context, o *storage.Organization) error {
	return r.s.exec(ctx, `INSERT INTO organization (id, name, created_at) VALUES (?, ?, ?)`,
		o.ID, o.Name, o.CreatedAt)
}

func (r *orgRepo) Get(ctx context.Context, id string) (*storage.Organization, error) {
	var o storage.Organization
	err := r.s.db.GetContext(ctx, &o, r.s.db.Rebind(
		`SELECT id, name, created_at FROM organization WHERE id = ?`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return &o, err
}

func (r *orgRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	stmts := []string{
		`DELETE FROM event WHERE organization_id = ?`,
		`DELETE FROM placement WHERE organization_id = ?`,
		`DELETE FROM harvest WHERE organization_id = ?`,
		`DELETE FROM treatment WHERE organization_id = ?`,
		`DELETE FROM task WHERE organization_id = ?`,
		`DELETE FROM inspection WHERE organization_id = ?`,
		`DELETE FROM queen WHERE organization_id = ?`,
		`DELETE FROM hive WHERE organization_id = ?`,
		`DELETE FROM apiary WHERE organization_id = ?`,
		`DELETE FROM change_log WHERE org_id = ?`,
		`DELETE FROM invite WHERE organization_id = ?`,
		`DELETE FROM api_key WHERE organization_id = ?`,
		`DELETE FROM member WHERE organization_id = ?`,
		`DELETE FROM organization WHERE id = ?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, tx.Rebind(q), id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (r *orgRepo) PhotoKeysByOrg(ctx context.Context, id string) ([]string, error) {
	var raws []string
	err := r.s.db.SelectContext(ctx, &raws, r.s.db.Rebind(
		`SELECT photo_keys FROM inspection WHERE organization_id = ? AND photo_keys <> ''`), id)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var keys []string
	for _, raw := range raws {
		for _, k := range photoKeyElements(raw) {
			if k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys, nil
}

// photoKeyElements extracts every element from a photo_keys column value:
// OR-Set JSON since 0003 (all elements, added or removed), with a fallback
// for the pre-0003 plain JSON array format.
func photoKeyElements(raw string) []string {
	if set := wsync.ParseORSet(raw); len(set) > 0 {
		out := make([]string, 0, len(set))
		for elem := range set {
			out = append(out, elem)
		}
		return out
	}
	var arr []string
	_ = json.Unmarshal([]byte(raw), &arr)
	return arr
}

// --- Memberships ---

type memberRepo struct{ s *Store }

func (r *memberRepo) Add(ctx context.Context, m *storage.Membership) error {
	return r.s.exec(ctx, `INSERT INTO member (organization_id, benutzer_id, role) VALUES (?, ?, ?)`,
		m.OrgID, m.UserID, m.Role)
}

func (r *memberRepo) Get(ctx context.Context, orgID, userID string) (*storage.Membership, error) {
	var m storage.Membership
	err := r.s.db.GetContext(ctx, &m, r.s.db.Rebind(
		`SELECT organization_id, benutzer_id, role FROM member WHERE organization_id = ? AND benutzer_id = ?`),
		orgID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return &m, err
}

func (r *memberRepo) ListByUser(ctx context.Context, userID string) ([]storage.TenantMembership, error) {
	var out []storage.TenantMembership
	err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(
		`SELECT m.organization_id, o.name, m.role
		 FROM member m JOIN organization o ON o.id = m.organization_id
		 WHERE m.benutzer_id = ? ORDER BY o.name ASC`), userID)
	return out, err
}

func (r *memberRepo) ListByOrg(ctx context.Context, orgID string) ([]storage.Member, error) {
	out := []storage.Member{}
	err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(
		`SELECT m.benutzer_id AS user_id, COALESCE(u.name, '') AS name, COALESCE(u.email, '') AS email, m.role
		 FROM member m LEFT JOIN users u ON u.id = m.benutzer_id
		 WHERE m.organization_id = ? ORDER BY m.role DESC, u.email ASC, m.benutzer_id ASC`), orgID)
	return out, err
}

// changeMember runs fn on one membership inside a transaction that holds the
// workspace's row lock, so two concurrent changes cannot both pass the
// last-owner check. fn gets the member's role and whether another owner
// exists.
func (r *memberRepo) changeMember(ctx context.Context, orgID, userID string, fn func(tx *sqlx.Tx, role string, otherOwner bool) error) error {
	tx, err := r.s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE organization SET name = name WHERE id = ?`), orgID); err != nil {
		return err
	}
	var role string
	err = tx.GetContext(ctx, &role, tx.Rebind(
		`SELECT role FROM member WHERE organization_id = ? AND benutzer_id = ?`), orgID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	if err != nil {
		return err
	}
	var others int
	if err := tx.GetContext(ctx, &others, tx.Rebind(
		`SELECT COUNT(*) FROM member WHERE organization_id = ? AND role = 'owner' AND benutzer_id <> ?`), orgID, userID); err != nil {
		return err
	}
	if err := fn(tx, role, others > 0); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *memberRepo) Remove(ctx context.Context, orgID, userID string) error {
	return r.changeMember(ctx, orgID, userID, func(tx *sqlx.Tx, role string, otherOwner bool) error {
		if role == "owner" && !otherOwner {
			return storage.ErrLastOwner
		}
		for _, q := range []string{
			`DELETE FROM api_key WHERE organization_id = ? AND user_id = ?`,
			`DELETE FROM member WHERE organization_id = ? AND benutzer_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, tx.Rebind(q), orgID, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *memberRepo) SetRole(ctx context.Context, orgID, userID, role string) error {
	return r.changeMember(ctx, orgID, userID, func(tx *sqlx.Tx, current string, otherOwner bool) error {
		if current == "owner" && role != "owner" && !otherOwner {
			return storage.ErrLastOwner
		}
		_, err := tx.ExecContext(ctx, tx.Rebind(
			`UPDATE member SET role = ? WHERE organization_id = ? AND benutzer_id = ?`), role, orgID, userID)
		return err
	})
}

// --- Invites ---

type inviteRepo struct{ s *Store }

func (r *inviteRepo) Create(ctx context.Context, i *storage.Invite) error {
	return r.s.exec(ctx, `INSERT INTO invite (id, organization_id, email, role, token, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.OrgID, i.Email, i.Role, i.Token, i.CreatedAt, i.ExpiresAt)
}

func (r *inviteRepo) GetByToken(ctx context.Context, token string) (*storage.Invite, error) {
	if token == "" {
		return nil, storage.ErrNotFound
	}
	var i storage.Invite
	err := r.s.db.GetContext(ctx, &i, r.s.db.Rebind(
		`SELECT id, organization_id, email, role, token, created_at, expires_at FROM invite WHERE token = ?`), token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return &i, err
}

func (r *inviteRepo) ListByOrg(ctx context.Context, orgID string) ([]storage.Invite, error) {
	out := []storage.Invite{}
	err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(
		`SELECT id, organization_id, email, role, token, created_at, expires_at FROM invite WHERE organization_id = ? ORDER BY created_at DESC`), orgID)
	return out, err
}

func (r *inviteRepo) Delete(ctx context.Context, id string) error {
	return r.s.exec(ctx, `DELETE FROM invite WHERE id = ?`, id)
}
