package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

type userRepo struct{ s *Store }

// userRow mirrors the user table. email_verified is scanned as bool: the
// database/sql bool converter accepts Postgres' native boolean as well as the
// 0/1 integers SQLite and MySQL return.
type userRow struct {
	ID            string    `db:"id"`
	Email         string    `db:"email"`
	Name          string    `db:"name"`
	OIDCSubject   string    `db:"oidc_subject"`
	PasswordHash  string    `db:"password_hash"`
	Role          string    `db:"role"`
	EmailVerified bool      `db:"email_verified"`
	VerifyToken   string    `db:"verification_token"`
	CreatedAt     time.Time `db:"created_at"`
}

func (r userRow) model() *storage.User {
	return &storage.User{
		ID: r.ID, Email: r.Email, Name: r.Name, OIDCSubject: r.OIDCSubject,
		PasswordHash: r.PasswordHash, Role: r.Role, EmailVerified: r.EmailVerified,
		VerifyToken: r.VerifyToken, CreatedAt: r.CreatedAt,
	}
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (*storage.User, error) {
	return r.getBy(ctx, `LOWER(email) = LOWER(?)`, email)
}

func (r *userRepo) GetByID(ctx context.Context, id string) (*storage.User, error) {
	return r.getBy(ctx, `id = ?`, id)
}

func (r *userRepo) GetBySubject(ctx context.Context, subject string) (*storage.User, error) {
	if subject == "" {
		return nil, storage.ErrNotFound
	}
	return r.getBy(ctx, `oidc_subject = ?`, subject)
}

func (r *userRepo) GetByVerifyToken(ctx context.Context, token string) (*storage.User, error) {
	if token == "" {
		return nil, storage.ErrNotFound
	}
	return r.getBy(ctx, `verification_token = ?`, token)
}

func (r *userRepo) LinkOIDC(ctx context.Context, id, subject string) error {
	return r.s.exec(ctx, `UPDATE users SET oidc_subject = ? WHERE id = ?`, subject, id)
}

func (r *userRepo) getBy(ctx context.Context, where, arg string) (*storage.User, error) {
	var row userRow
	err := r.s.db.GetContext(ctx, &row, r.s.db.Rebind(
		`SELECT id, email, name, oidc_subject, password_hash, role, email_verified, verification_token, created_at
		 FROM users WHERE `+where), arg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row.model(), nil
}

func (r *userRepo) Create(ctx context.Context, u *storage.User) error {
	return r.s.exec(ctx, `
		INSERT INTO users (id, email, name, oidc_subject, password_hash, role, email_verified, verification_token, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, u.OIDCSubject, u.PasswordHash, u.Role, u.EmailVerified, u.VerifyToken, u.CreatedAt)
}

func (r *userRepo) MarkVerified(ctx context.Context, id string) error {
	return r.s.exec(ctx, `UPDATE users SET email_verified = TRUE, verification_token = '' WHERE id = ?`, id)
}

func (r *userRepo) SetCredentials(ctx context.Context, id, passwordHash, role string) error {
	return r.s.exec(ctx, `
		UPDATE users SET password_hash = ?, role = ?, email_verified = TRUE, verification_token = ''
		WHERE id = ?`, passwordHash, role, id)
}
