package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type apiaryRepo struct{ s *Store }

func (r *apiaryRepo) Create(ctx context.Context, m *storage.Apiary) error {
	return r.s.exec(ctx, `
		INSERT INTO apiary (id, organization_id, name, address, lat, lng, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.OrgID, m.Name, m.Address, m.Lat, m.Lng, m.Note, m.CreatedAt, m.UpdatedAt)
}

func (r *apiaryRepo) Get(ctx context.Context, orgID, id string) (*storage.Apiary, error) {
	var m storage.Apiary
	err := r.s.db.GetContext(ctx, &m, r.s.db.Rebind(`
		SELECT id, organization_id, name, address, lat, lng, note, created_at, updated_at
		FROM apiary WHERE organization_id = ? AND id = ? AND deleted = FALSE`), orgID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return &m, err
}

func (r *apiaryRepo) List(ctx context.Context, orgID string, limit, offset int) ([]storage.Apiary, int, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []storage.Apiary
	err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(`
		SELECT id, organization_id, name, address, lat, lng, note, created_at, updated_at
		FROM apiary WHERE organization_id = ? AND deleted = FALSE
		ORDER BY name ASC LIMIT ? OFFSET ?`), orgID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	var total int
	_ = r.s.db.GetContext(ctx, &total, r.s.db.Rebind(
		`SELECT COUNT(*) FROM apiary WHERE organization_id = ? AND deleted = FALSE`), orgID)
	return out, total, nil
}

func (r *apiaryRepo) Update(ctx context.Context, m *storage.Apiary) error {
	return r.s.exec(ctx, `
		UPDATE apiary SET name = ?, address = ?, lat = ?, lng = ?, note = ?, updated_at = ?
		WHERE organization_id = ? AND id = ?`,
		m.Name, m.Address, m.Lat, m.Lng, m.Note, m.UpdatedAt, m.OrgID, m.ID)
}

func (r *apiaryRepo) Delete(ctx context.Context, orgID, id string) error {
	return r.s.exec(ctx, `DELETE FROM apiary WHERE organization_id = ? AND id = ?`, orgID, id)
}

func (r *apiaryRepo) HiveCount(ctx context.Context, orgID, apiaryID string) (int, error) {
	var n int
	err := r.s.db.GetContext(ctx, &n, r.s.db.Rebind(
		`SELECT COUNT(*) FROM hive WHERE organization_id = ? AND apiary_id = ? AND deleted = FALSE`), orgID, apiaryID)
	return n, err
}

// --- Hive repo ---

type hiveRepo struct{ s *Store }

func (r *hiveRepo) Create(ctx context.Context, m *storage.Hive) error {
	return r.s.exec(ctx, `
		INSERT INTO hive (id, organization_id, apiary_id, name, type, status, boxes, colony_origin, note, qr_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.OrgID, m.ApiaryID, m.Name, m.Type, m.Status, m.Boxes, m.ColonyOrigin, m.Note, m.QRCode, m.CreatedAt, m.UpdatedAt)
}

func (r *hiveRepo) Get(ctx context.Context, orgID, id string) (*storage.Hive, error) {
	var m storage.Hive
	err := r.s.db.GetContext(ctx, &m, r.s.db.Rebind(
		`SELECT id, organization_id, apiary_id, name, type, status, boxes, colony_origin, note, qr_code, created_at, updated_at
		 FROM hive WHERE organization_id = ? AND id = ? AND deleted = FALSE`), orgID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	return &m, err
}

func (r *hiveRepo) List(ctx context.Context, orgID, apiaryID string, limit, offset int) ([]storage.Hive, int, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, organization_id, apiary_id, name, type, status, boxes, colony_origin, note, qr_code, created_at, updated_at
	      FROM hive WHERE organization_id = ? AND deleted = FALSE`
	args := []any{orgID}
	if apiaryID != "" {
		q += ` AND apiary_id = ?`
		args = append(args, apiaryID)
	}
	q += ` ORDER BY name ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	var out []storage.Hive
	if err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(q), args...); err != nil {
		return nil, 0, err
	}
	return out, len(out), nil
}

func (r *hiveRepo) Update(ctx context.Context, m *storage.Hive) error {
	return r.s.exec(ctx, `
		UPDATE hive SET apiary_id=?, name=?, type=?, status=?, boxes=?, colony_origin=?, note=?, updated_at=?
		WHERE organization_id=? AND id=?`,
		m.ApiaryID, m.Name, m.Type, m.Status, m.Boxes, m.ColonyOrigin, m.Note, m.UpdatedAt, m.OrgID, m.ID)
}

func (r *hiveRepo) Delete(ctx context.Context, orgID, id string) error {
	return r.s.exec(ctx, `DELETE FROM hive WHERE organization_id = ? AND id = ?`, orgID, id)
}
