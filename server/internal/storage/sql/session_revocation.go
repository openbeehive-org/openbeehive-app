package sqlstore

import (
	"context"
	"time"
)

// RevokeSession records that the session whose token has the given hash was
// ended, until the token expires, and deletes the records of tokens that have
// expired since, whoever they belonged to. Recording a token again replaces
// its record.
func (s *Store) RevokeSession(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM session_revocation WHERE expires_at <= ? OR token_hash = ?`),
		time.Now().UnixMilli(), tokenHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO session_revocation (token_hash, expires_at) VALUES (?, ?)`),
		tokenHash, expiresAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokedSessions returns the ended sessions whose tokens expire after now:
// the hash of each token with the moment it expires.
func (s *Store) RevokedSessions(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	var rows []struct {
		TokenHash string `db:"token_hash"`
		ExpiresAt int64  `db:"expires_at"`
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(
		`SELECT token_hash, expires_at FROM session_revocation WHERE expires_at > ?`), now.UnixMilli()); err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.TokenHash] = time.UnixMilli(r.ExpiresAt)
	}
	return out, nil
}
