package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
)

// settingFields are the synced fields of user_settings.
var settingFields = []string{"weight_unit", "temp_unit", "language", "deleted"}

// settingsRow is one account's settings with their field clock.
type settingsRow struct {
	values map[string]any
	clock  map[string]string
}

// UserHasSettings reports whether the account has a settings row.
func (s *Store) UserHasSettings(ctx context.Context, userID string) (bool, error) {
	var n int
	err := s.db.GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM user_settings WHERE id = ?`), userID)
	return n > 0, err
}

func loadSettings(ctx context.Context, tx *sqlx.Tx, userID string) (*settingsRow, error) {
	var r struct {
		Weight, Temp, Lang sql.NullString
		Deleted            bool
		Clock              string
	}
	err := tx.QueryRowxContext(ctx, tx.Rebind(
		`SELECT weight_unit, temp_unit, language, deleted, field_hlc FROM user_settings WHERE id = ?`), userID).
		Scan(&r.Weight, &r.Temp, &r.Lang, &r.Deleted, &r.Clock)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row := &settingsRow{
		values: map[string]any{"weight_unit": r.Weight, "temp_unit": r.Temp, "language": r.Lang, "deleted": r.Deleted},
		clock:  map[string]string{},
	}
	_ = json.Unmarshal([]byte(r.Clock), &row.clock) // an unreadable clock counts as no stamps
	return row, nil
}

// moveUserSettings gives toUser the settings fromUser has, with their
// change-log entries, and returns the number of rows it touched. Where
// toUser has settings of its own, each field keeps the value with the newer
// stamp (stamps are fixed-width text, so text order is time order).
func moveUserSettings(ctx context.Context, tx *sqlx.Tx, fromUser, toUser string) (int64, error) {
	if fromUser == "" || fromUser == toUser {
		return 0, nil
	}
	from, err := loadSettings(ctx, tx, fromUser)
	if err != nil || from == nil {
		return 0, err
	}
	to, err := loadSettings(ctx, tx, toUser)
	if err != nil {
		return 0, err
	}
	merged := from
	if to != nil {
		merged = to
		for _, f := range settingFields {
			if stamp, ok := from.clock[f]; ok && stamp > to.clock[f] {
				merged.values[f], merged.clock[f] = from.values[f], stamp
			}
		}
	}
	clock, err := json.Marshal(merged.clock)
	if err != nil {
		return 0, err
	}
	args := []any{merged.values["weight_unit"], merged.values["temp_unit"], merged.values["language"],
		merged.values["deleted"], string(clock), toUser}
	q := `UPDATE user_settings SET weight_unit = ?, temp_unit = ?, language = ?, deleted = ?, field_hlc = ? WHERE id = ?`
	if to == nil {
		q = `INSERT INTO user_settings (weight_unit, temp_unit, language, deleted, field_hlc, id) VALUES (?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(q), args...); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM user_settings WHERE id = ?`), fromUser); err != nil {
		return 0, err
	}
	// The log entries are filed under the account (see the service's
	// sync_user.go): they follow it, so the admin's devices receive them.
	fromScope, toScope := "user:"+fromUser, "user:"+toUser
	res, err := tx.ExecContext(ctx, tx.Rebind(
		`UPDATE change_log SET org_id = ?, scope_id = ?, entity_id = ?, author_id = ?
		 WHERE org_id = ? AND entity = 'user_settings'`), toScope, toScope, toUser, toUser, fromScope)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n + 1, nil
}
