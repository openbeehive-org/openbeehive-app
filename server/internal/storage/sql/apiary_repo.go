package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type apiaryRepo struct{ s *Store }

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
		ORDER BY name ASC, id ASC LIMIT ? OFFSET ?`), orgID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	var total int
	err = r.s.db.GetContext(ctx, &total, r.s.db.Rebind(
		`SELECT COUNT(*) FROM apiary WHERE organization_id = ? AND deleted = FALSE`), orgID)
	return out, total, err
}

func (r *apiaryRepo) HiveCount(ctx context.Context, orgID, apiaryID string) (int, error) {
	var n int
	err := r.s.db.GetContext(ctx, &n, r.s.db.Rebind(
		`SELECT COUNT(*) FROM hive WHERE organization_id = ? AND apiary_id = ? AND deleted = FALSE`), orgID, apiaryID)
	return n, err
}

// --- Hive repo ---

type hiveRepo struct{ s *Store }

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
	where := ` FROM hive WHERE organization_id = ? AND deleted = FALSE`
	args := []any{orgID}
	if apiaryID != "" {
		where += ` AND apiary_id = ?`
		args = append(args, apiaryID)
	}
	// The total counts every matching row, not just this page: the caller
	// derives from it whether another page follows.
	var total int
	if err := r.s.db.GetContext(ctx, &total, r.s.db.Rebind(`SELECT COUNT(*)`+where), args...); err != nil {
		return nil, 0, err
	}
	var out []storage.Hive
	err := r.s.db.SelectContext(ctx, &out, r.s.db.Rebind(
		`SELECT id, organization_id, apiary_id, name, type, status, boxes, colony_origin, note, qr_code, created_at, updated_at`+
			where+` ORDER BY name ASC, id ASC LIMIT ? OFFSET ?`), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
