package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// DeleteAccount removes an account and everything personal that hangs on it,
// in one transaction: passkeys, API keys, reset links, memberships, open
// invites to its address, and its personal tasks with their history in every
// workspace. Records the account wrote in shared workspaces stay; their
// author id then points at no account. Workspaces are not touched: the caller
// deletes those the account was the only member of beforehand.
func (s *Store) DeleteAccount(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var email string
	err = tx.GetContext(ctx, &email, tx.Rebind(`SELECT email FROM users WHERE id = ?`), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := deletePersonalData(ctx, tx, userID, ""); err != nil {
		return err
	}
	for _, q := range []struct {
		sql string
		arg string
	}{
		{`DELETE FROM user_passkey WHERE user_id = ?`, userID},
		{`DELETE FROM api_key WHERE user_id = ?`, userID},
		{`DELETE FROM password_reset WHERE user_id = ?`, userID},
		{`DELETE FROM member WHERE benutzer_id = ?`, userID},
		{`DELETE FROM invite WHERE LOWER(email) = ?`, storage.NormalizeEmail(email)},
		{`DELETE FROM users WHERE id = ?`, userID},
	} {
		if _, err := tx.ExecContext(ctx, tx.Rebind(q.sql), q.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// personalScope is the sync scope only one account's devices pull.
func personalScope(userID string) string { return "user:" + userID }

// personalTasksOf returns the condition that selects the personal tasks of
// one account in a WHERE clause on task, with its argument. A personal task
// has no owner column: it is the task without an apiary whose changes were
// logged in the account's personal scope (the rule of visibleTasks in the
// service package). This is the one place the storage layer spells that
// rule out.
func personalTasksOf(userID string) (cond string, arg any) {
	return `COALESCE(apiary_id, '') = '' AND id IN
		(SELECT entity_id FROM change_log WHERE scope_id = ? AND entity = 'task')`, personalScope(userID)
}

// deletePersonalData removes what only one account can see: its personal
// tasks, their timeline entries and their change-log entries, in one
// workspace or, with orgID "", in all of them.
//
// The rows are deleted outright instead of being tombstoned through the
// change log. It runs when a membership or the account itself ends, and
// from then on the account's devices are refused by that workspace: a
// tombstone in a scope nobody may pull would never be delivered, and would
// keep the task's title on the server for good.
func deletePersonalData(ctx context.Context, tx *sqlx.Tx, userID, orgID string) error {
	tasks, taskArg := personalTasksOf(userID)
	for _, q := range []struct {
		sql, orgCol string
		arg         any
	}{
		// Tasks first: finding them needs the change log.
		{`DELETE FROM task WHERE ` + tasks, "organization_id", taskArg},
		{`DELETE FROM event WHERE scope_id = ?`, "organization_id", personalScope(userID)},
		{`DELETE FROM change_log WHERE scope_id = ?`, "org_id", personalScope(userID)},
	} {
		args := []any{q.arg}
		if orgID != "" {
			q.sql += ` AND ` + q.orgCol + ` = ?`
			args = append(args, orgID)
		}
		if _, err := tx.ExecContext(ctx, tx.Rebind(q.sql), args...); err != nil {
			return err
		}
	}
	return nil
}

// PasskeyCount returns how many passkeys the account has enrolled.
func (s *Store) PasskeyCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM user_passkey WHERE user_id = ?`), userID)
	return n, err
}

// CreatePasswordReset stores the hash of a reset token for the account and
// drops links that have expired, whoever they belonged to.
func (s *Store) CreatePasswordReset(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	now := time.Now().UTC()
	if err := s.exec(ctx, `DELETE FROM password_reset WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	return s.exec(ctx, `INSERT INTO password_reset (id, user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		uuid.NewString(), userID, tokenHash, now, expiresAt.UTC())
}

// ConsumePasswordReset redeems a reset token: it returns the account the
// token was issued for and deletes every reset link of that account, so the
// token works once. storage.ErrNotFound for an unknown, used or expired token.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash string, now time.Time) (string, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var row struct {
		UserID    string    `db:"user_id"`
		ExpiresAt time.Time `db:"expires_at"`
	}
	err = tx.GetContext(ctx, &row, tx.Rebind(
		`SELECT user_id, expires_at FROM password_reset WHERE token_hash = ?`), tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	// Deleting by hash decides a race between two requests with one token:
	// only one of them removes the row.
	res, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM password_reset WHERE token_hash = ?`), tokenHash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 || !now.Before(row.ExpiresAt) {
		// An expired link is removed all the same.
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", storage.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM password_reset WHERE user_id = ?`), row.UserID); err != nil {
		return "", err
	}
	return row.UserID, tx.Commit()
}

// EmailConflict is an account whose email address another account already
// holds when capitalisation is ignored.
type EmailConflict struct {
	UserID, Email     string // the account without a claim on the address
	HolderID, HolderE string // the account sign-in by email reaches
}

// claimEmailKeys gives every account that has none its email_key, the
// lower-cased address the unique index guards. Accounts stored by earlier
// releases have none, and two of those may differ only by case. They are
// never merged: the one whose address is already in normal form (then the
// oldest) takes the key and is the account that sign-in by email, invites
// and password resets reach; the other keeps working through its passkeys or
// provider login and is returned for the operator to decide about.
func (s *Store) claimEmailKeys(ctx context.Context) ([]EmailConflict, error) {
	type row struct {
		ID        string    `db:"id"`
		Email     string    `db:"email"`
		CreatedAt time.Time `db:"created_at"`
	}
	var pending []row
	if err := s.db.SelectContext(ctx, &pending,
		`SELECT id, email, created_at FROM users WHERE email_key IS NULL`); err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		an, bn := a.Email == storage.NormalizeEmail(a.Email), b.Email == storage.NormalizeEmail(b.Email)
		switch {
		case an != bn:
			return an
		case !a.CreatedAt.Equal(b.CreatedAt):
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	var conflicts []EmailConflict
	for _, u := range pending {
		key := storage.NormalizeEmail(u.Email)
		var holder row
		err := s.db.GetContext(ctx, &holder, s.db.Rebind(
			`SELECT id, email, created_at FROM users WHERE email_key = ?`), key)
		switch {
		case err == nil:
			conflicts = append(conflicts, EmailConflict{UserID: u.ID, Email: u.Email, HolderID: holder.ID, HolderE: holder.Email})
		case errors.Is(err, sql.ErrNoRows):
			if err := s.exec(ctx, `UPDATE users SET email_key = ? WHERE id = ? AND email_key IS NULL`, key, u.ID); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	return conflicts, nil
}

// reportEmailConflicts runs claimEmailKeys and logs what is left for the
// operator, at every start until it is resolved.
func (s *Store) reportEmailConflicts(ctx context.Context) error {
	conflicts, err := s.claimEmailKeys(ctx)
	if err != nil {
		return err
	}
	for _, c := range conflicts {
		log.Printf("accounts: %s (%s) has the same email address as %s (%s) apart from upper and lower case. "+
			"Sign-in by email, invites and password resets reach %s only; the other account still works with its passkeys or provider login. "+
			"Nothing was merged or deleted: decide which account keeps the address, then delete the other in the app or give it a different address in the users table.",
			c.UserID, c.Email, c.HolderID, c.HolderE, c.HolderID)
	}
	return nil
}
