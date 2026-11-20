package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

const (
	// snapshotPageBytes is the default size at which a snapshot page is
	// closed. A page ends after the row that crosses it, so pages stay well
	// below the 16 MiB message cap unless a single row is larger than that.
	snapshotPageBytes = 4 << 20
	// snapshotBatch is how many rows one query reads at most.
	snapshotBatch = 200
)

// snapshotToken is the position inside a snapshot: the cursor fixed on the
// first page, the entity being read and the last row id already delivered.
type snapshotToken struct {
	Cursor string `json:"c"`
	Entity string `json:"e"`
	After  string `json:"a"`
}

func (t snapshotToken) encode() string {
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseSnapshotToken(s string) (snapshotToken, bool) {
	var t snapshotToken
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &t) != nil {
		return t, false
	}
	_, known := entityCols[t.Entity]
	return t, known
}

// snapshotEntities is the order in which a snapshot delivers the tables.
func snapshotEntities() []string {
	names := make([]string, 0, len(entityCols))
	for name := range entityCols {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Snapshot returns the current rows of the caller's workspace, one page per
// call (see SnapshotRequest). The cursor is fixed before the first row is
// read: whatever changes while the pages are fetched has a higher sequence
// number and is delivered again by Pull, where it merges without effect.
func (s *SyncService) Snapshot(ctx context.Context, req *wv1.SnapshotRequest) (*wv1.SnapshotResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	client, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	if client.legacy() {
		return nil, upgradeRequired(protocolSnapshots, "snapshots need the "+HeaderSyncProtocol+" request header")
	}
	tok, err := s.snapshotStart(ctx, req.PageToken)
	if err != nil {
		return nil, err
	}
	res := &wv1.SnapshotResponse{Cursor: tok.Cursor, ServerTimeMs: time.Now().UnixMilli()}
	entities := snapshotEntities()
	size := 0
	for i := sort.SearchStrings(entities, tok.Entity); i < len(entities); i++ {
		tok.Entity = entities[i]
		for more := true; more && size < s.pageBytes; {
			var rows []*wv1.SnapshotRow
			if rows, more, err = s.snapshotRows(ctx, id, tok, s.pageBytes-size); err != nil {
				return nil, internalErr(err)
			}
			for _, r := range rows {
				size += snapshotRowSize(r)
				tok.After = r.EntityId
			}
			res.Rows = append(res.Rows, rows...)
		}
		if size >= s.pageBytes {
			res.NextPageToken = tok.encode()
			break
		}
		tok.After = ""
	}
	return res, nil
}

// snapshotStart returns where a snapshot call begins: at the first table
// with a fresh cursor, or where the page token of the previous page points.
func (s *SyncService) snapshotStart(ctx context.Context, pageToken string) (snapshotToken, error) {
	st, err := s.state(ctx)
	if err != nil {
		return snapshotToken{}, internalErr(err)
	}
	if pageToken == "" {
		tok := snapshotToken{Entity: snapshotEntities()[0]}
		if tok.Cursor, err = s.cursorAt(ctx, st, st.head); err != nil {
			return tok, internalErr(err)
		}
		return tok, nil
	}
	tok, valid := parseSnapshotToken(pageToken)
	if c, parsed := parseCursor(tok.Cursor); !valid || !parsed || c.epoch != st.epoch || c.seq > st.head {
		return tok, connect.NewError(connect.CodeAborted,
			"this snapshot cannot be continued; start again from the first page")
	}
	return tok, nil
}

// snapshotRowSize is what a row counts towards the page size.
func snapshotRowSize(r *wv1.SnapshotRow) int {
	return len(r.PayloadJson) + len(r.FieldHlcJson) + len(r.EntityId) + 64
}

// visibleRows restricts a snapshot query to the rows the caller may see
// inside the workspace: personal tasks only for their owner, events of a
// personal scope only for that user. Everything else belongs to the
// workspace as a whole.
func visibleRows(entity string, id auth.Identity) (string, []any) {
	switch entity {
	case "task":
		return visibleTasks, []any{id.UserID}
	case "event":
		return ` AND (COALESCE(scope_id, '') NOT LIKE 'user:%' OR scope_id = ?)`, []any{personalScope(id.UserID)}
	}
	return "", nil
}

// snapshotRows reads the next rows of one table after tok.After, stopping
// once budget bytes are used. more reports whether the table has rows left.
func (s *SyncService) snapshotRows(ctx context.Context, id auth.Identity, tok snapshotToken, budget int) (out []*wv1.SnapshotRow, more bool, err error) {
	spec := entityCols[tok.Entity]
	kinds := kindsOf(tok.Entity)
	cols := append(append([]string{}, spec.cols...), spec.setCols...)
	selected := selectList(s.db.DriverName(), kinds, cols)
	visible, args := visibleRows(tok.Entity, id)
	owner, ownerArg := "organization_id", id.OrgID
	if spec.perUser {
		owner, ownerArg = "id", id.UserID // the account's own row, in every workspace
	}
	q := fmt.Sprintf(`SELECT %s, field_hlc FROM %s WHERE %s = ? AND id > ?%s ORDER BY id ASC LIMIT ?`,
		strings.Join(selected, ", "), spec.table, owner, visible)
	args = append(append([]any{ownerArg, tok.After}, args...), snapshotBatch)
	rows, err := s.db.QueryxContext(ctx, s.db.Rebind(q), args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	read := 0
	for rows.Next() {
		m := map[string]any{}
		if err := rows.MapScan(m); err != nil {
			return nil, false, err
		}
		read++
		payload := make(map[string]any, len(cols))
		for _, c := range spec.cols {
			if c != "id" {
				payload[c] = wireValue(kinds[c], m[c])
			}
		}
		for _, c := range spec.setCols {
			payload[c] = wsync.ParseORSet(asString(m[c]))
		}
		withoutInlinePhotos(tok.Entity, payload)
		pj, err := json.Marshal(payload)
		if err != nil {
			return nil, false, err
		}
		row := &wv1.SnapshotRow{
			Entity: tok.Entity, EntityId: rs(m["id"]),
			PayloadJson: string(pj), FieldHlcJson: wsync.ParseFieldClock(asString(m["field_hlc"])).Marshal(),
		}
		out = append(out, row)
		if budget -= snapshotRowSize(row); budget <= 0 {
			return out, true, rows.Err()
		}
	}
	return out, read == snapshotBatch, rows.Err()
}

// selectList is the SELECT list that reads cols as sync payloads carry
// them. SQLite holds timestamps as the text the devices sent. Read as text
// they come back unchanged; read as a timestamp column the driver would
// parse and reformat them.
func selectList(driver string, kinds map[string]colKind, cols []string) []string {
	selected := make([]string, len(cols))
	for i, c := range cols {
		selected[i] = c
		if driver == "sqlite" && kinds[c] == kindTime {
			selected[i] = "CAST(" + c + " AS TEXT) AS " + c
		}
	}
	return selected
}

// wireValue turns a stored column value into the form sync payloads carry
// (see sync_payload.go): booleans as 0/1, timestamps as ISO strings, NULL as
// null. It is the reverse of dbValue. Postgres and MySQL store instants,
// which are written the way JavaScript's toISOString does.
func wireValue(kind colKind, v any) any {
	if v == nil {
		return nil
	}
	switch kind {
	case kindBool:
		return b2i(rb(v))
	case kindInt:
		return ri(v)
	case kindFloat:
		return rf(v)
	default:
		return rs(v) // text, and timestamps in either stored form
	}
}
