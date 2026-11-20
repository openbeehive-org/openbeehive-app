package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// Account settings (entity "user_settings") are the one synced row that
// belongs to an account rather than to a workspace: the units a user reads
// and enters weights and temperatures in, and the language. They must follow
// the user to every device, whichever of the user's workspaces it is signed
// in to.
//
// Personal rows such as private tasks are filtered per workspace: their
// change-log entries carry the workspace they were written in (org_id), and
// Pull serves them only there. Settings are different:
//
//   - There is one row per account and its id is the account's id, so two
//     devices that create it offline write the same row, and the fields
//     merge by last writer wins like any other row. Nothing is duplicated.
//   - It is written in the personal scope ("user:<id>"), and only by that
//     account (checkOwnRow).
//   - Its change-log entries are filed under the account (org_id
//     "user:<id>", accountLog) instead of under a workspace, and Pull and
//     Subscribe read them in every workspace of the account. A preference set
//     while signed in to one workspace reaches the account's devices that
//     are signed in to another; Snapshot also serves the row in every
//     workspace.
//   - Switching workspace does not touch the row. Removing a member from a
//     workspace deletes personal entries of that workspace only
//     (deletePersonalData with an org), never these. Deleting the account
//     deletes the row and, with every entry of the personal scope, its log.
//
// 0.4.x apps never receive it: the entity is not in legacyCols.

// accountLog is the change-log org_id under which an account's own rows are
// filed. It cannot collide with a workspace id, which never has a colon.
func accountLog(userID string) string { return personalScope(userID) }

const (
	accountLogQuery = `
		SELECT scope_id, entity, entity_id, op, payload, hlc, author_id, seq
		FROM change_log
		WHERE org_id = ? AND seq > ?
		ORDER BY seq ASC LIMIT ?`
	latestAccountQuery = `
		SELECT seq FROM change_log WHERE org_id = ? ORDER BY seq DESC LIMIT 1`
)

// logOrg is the org_id a change is logged under: the caller's workspace, or
// the account for its own rows.
func logOrg(spec entitySpec, ch *wv1.Change, orgID string) string {
	if spec.perUser {
		return ch.ScopeId // the personal scope, checked by checkOwnRow
	}
	return orgID
}

// checkOwnRow refuses a change of an account row that is not the caller's
// own: written outside the personal scope, or naming another row.
func checkOwnRow(spec entitySpec, ch *wv1.Change, scopes *scopeSet) *changeError {
	if !spec.perUser {
		return nil
	}
	if ch.ScopeId != scopes.personal || personalScope(ch.EntityId) != scopes.personal {
		return &changeError{code: rejForbiddenScope, reason: fmt.Sprintf(
			"%s is written in the personal scope, with the account's own id as row id", ch.Entity)}
	}
	return nil
}

// settingValue is what a setting may hold: a short code such as "kg", "f"
// or "pt-BR", or "" for "not chosen". The app ignores codes it does not know,
// so a newer app may introduce more without breaking an older one.
var settingValue = regexp.MustCompile(`^[A-Za-z-]{0,16}$`)

// checkSettingValues validates the text fields of a settings change.
func checkSettingValues(fields map[string]any) *changeError {
	for col, v := range fields {
		if col == "deleted" || v == nil {
			continue
		}
		if s, ok := v.(string); !ok || !settingValue.MatchString(s) {
			return invalidChange("%s: not a setting value", col)
		}
	}
	return nil
}

// mergeLog merges two pages of log entries, each sorted by seq, into the
// first limit entries of both.
func mergeLog(a, b []logEntry, limit int) []logEntry {
	if len(b) == 0 {
		return a
	}
	out := make([]logEntry, 0, min(len(a)+len(b), limit))
	for len(out) < limit && (len(a) > 0 || len(b) > 0) {
		if len(b) == 0 || (len(a) > 0 && a[0].seq < b[0].seq) {
			out, a = append(out, a[0]), a[1:]
		} else {
			out, b = append(out, b[0]), b[1:]
		}
	}
	return out
}

// latestAccountSeq is the sequence number of the account's newest own
// change, or 0.
func (s *SyncService) latestAccountSeq(ctx context.Context, userID string) (int64, error) {
	var seq int64
	err := s.db.GetContext(ctx, &seq, s.db.Rebind(latestAccountQuery), accountLog(userID))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}
