package sqlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// orgScopedTables are the record tables whose rows belong to one workspace
// through organization_id. A synced table that is missing here would be left
// behind by MoveOrgData; TestOrgScopedTablesAreComplete guards the list.
var orgScopedTables = []string{
	"apiary", "hive", "queen", "inspection", "task", "treatment", "harvest", "placement", "event", "photo",
}

// OrgMove describes moving a workspace's records to another workspace and
// re-attributing what one user wrote to another user.
type OrgMove struct {
	FromOrg, ToOrg   string
	FromUser, ToUser string
}

// OrgHasData reports whether any record or change-log entry belongs to the
// workspace.
func (s *Store) OrgHasData(ctx context.Context, orgID string) (bool, error) {
	var n int
	if err := s.db.GetContext(ctx, &n, s.db.Rebind(
		`SELECT COUNT(*) FROM change_log WHERE org_id = ?`), orgID); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	for _, table := range orgScopedTables {
		if err := s.db.GetContext(ctx, &n, s.db.Rebind(
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE organization_id = ?`, table)), orgID); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// MoveOrgData moves every record and change-log entry of m.FromOrg into
// m.ToOrg in one transaction and returns the number of rows it touched. The
// personal scope, authorship and task assignments of m.FromUser become
// m.ToUser's, also inside the logged payloads, so a device that pulls the
// log afterwards receives rows that belong to the new workspace. Running it
// again is a no-op: nothing is left under m.FromOrg.
func (s *Store) MoveOrgData(ctx context.Context, m OrgMove) (int64, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	fromScope, toScope := "user:"+m.FromUser, "user:"+m.ToUser
	// Attribution first, while the rows can still be found by their old workspace.
	for _, q := range []struct {
		sql      string
		to, from string
	}{
		{`UPDATE event SET author_id = ? WHERE organization_id = ? AND author_id = ?`, m.ToUser, m.FromUser},
		{`UPDATE event SET scope_id = ? WHERE organization_id = ? AND scope_id = ?`, toScope, fromScope},
		{`UPDATE task SET assigned_to = ? WHERE organization_id = ? AND assigned_to = ?`, m.ToUser, m.FromUser},
		{`UPDATE task SET owner_id = ? WHERE organization_id = ? AND owner_id = ?`, m.ToUser, m.FromUser},
	} {
		if _, err := tx.ExecContext(ctx, tx.Rebind(q.sql), q.to, m.FromOrg, q.from); err != nil {
			return 0, err
		}
	}

	var moved int64
	for _, table := range orgScopedTables {
		res, err := tx.ExecContext(ctx, tx.Rebind(
			fmt.Sprintf(`UPDATE %s SET organization_id = ? WHERE organization_id = ?`, table)),
			m.ToOrg, m.FromOrg)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		moved += n
	}

	n, err := moveChangeLog(ctx, tx, m)
	if err != nil {
		return 0, fmt.Errorf("change_log: %w", err)
	}
	moved += n
	return moved, tx.Commit()
}

// moveChangeLog rewrites the change-log entries of m.FromOrg page by page
// (payloads can be large, and the transaction's connection cannot read and
// write at the same time).
func moveChangeLog(ctx context.Context, tx *sqlx.Tx, m OrgMove) (int64, error) {
	const page = 200
	fromScope, toScope := "user:"+m.FromUser, "user:"+m.ToUser
	rewrite := map[string][2]string{
		"organization_id": {m.FromOrg, m.ToOrg},
		"author_id":       {m.FromUser, m.ToUser},
		"assigned_to":     {m.FromUser, m.ToUser},
		"scope_id":        {fromScope, toScope},
	}
	var moved, after int64
	for {
		var rows []struct {
			ID      string `db:"id"`
			Seq     int64  `db:"seq"`
			ScopeID string `db:"scope_id"`
			Author  string `db:"author_id"`
			Payload string `db:"payload"`
		}
		if err := tx.SelectContext(ctx, &rows, tx.Rebind(
			`SELECT id, seq, scope_id, author_id, payload FROM change_log
			 WHERE org_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`), m.FromOrg, after, page); err != nil {
			return 0, err
		}
		for _, r := range rows {
			if r.ScopeID == fromScope {
				r.ScopeID = toScope
			}
			if r.Author == m.FromUser {
				r.Author = m.ToUser
			}
			if _, err := tx.ExecContext(ctx, tx.Rebind(
				`UPDATE change_log SET org_id = ?, scope_id = ?, author_id = ?, payload = ? WHERE id = ?`),
				m.ToOrg, r.ScopeID, r.Author, rewritePayload(r.Payload, rewrite), r.ID); err != nil {
				return 0, err
			}
			after = r.Seq
		}
		moved += int64(len(rows))
		if len(rows) < page {
			return moved, nil
		}
	}
}

// rewritePayload replaces string fields of a JSON object payload: for each
// key in rewrite, the value rewrite[key][0] becomes rewrite[key][1]. Every
// other field keeps its original encoding; a payload that is not a JSON
// object is returned unchanged.
func rewritePayload(payload string, rewrite map[string][2]string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &fields) != nil || fields == nil {
		return payload
	}
	changed := false
	for key, pair := range rewrite {
		var cur string
		if raw, ok := fields[key]; !ok || json.Unmarshal(raw, &cur) != nil || cur != pair[0] {
			continue
		}
		fields[key], _ = json.Marshal(pair[1])
		changed = true
	}
	if !changed {
		return payload
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(fields) != nil {
		return payload
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}
