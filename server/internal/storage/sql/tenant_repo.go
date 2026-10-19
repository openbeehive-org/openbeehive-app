package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

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
		// Shares reference apiaries, not the org; resolve them via the
		// apiaries being deleted.
		`DELETE FROM apiary_share WHERE apiary_id IN (SELECT id FROM apiary WHERE organization_id = ?)`,
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

// --- Invites ---

type inviteRepo struct{ s *Store }

func (r *inviteRepo) Create(ctx context.Context, i *storage.Invite) error {
	return r.s.exec(ctx, `INSERT INTO invite (id, organization_id, email, role, token, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		i.ID, i.OrgID, i.Email, i.Role, i.Token, i.CreatedAt)
}

func (r *inviteRepo) GetByToken(ctx context.Context, token string) (*storage.Invite, error) {
	if token == "" {
		return nil, storage.ErrNotFound
	}
	var i storage.Invite
	err := r.s.db.GetContext(ctx, &i, r.s.db.Rebind(
		`SELECT id, organization_id, email, role, token, created_at FROM invite WHERE token = ?`), token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return &i, err
}

func (r *inviteRepo) ListByOrg(ctx context.Context, orgID string) ([]storage.Invite, error) {
	out := []storage.Invite{}
	err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(
		`SELECT id, organization_id, email, role, token, created_at FROM invite WHERE organization_id = ? ORDER BY created_at DESC`), orgID)
	return out, err
}

func (r *inviteRepo) Delete(ctx context.Context, id string) error {
	return r.s.exec(ctx, `DELETE FROM invite WHERE id = ?`, id)
}
