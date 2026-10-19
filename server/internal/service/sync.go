package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Column whitelist per entity. cols are merged via per-field LWW,
// setCols as OR-Set (add-wins). Both stay portable (no UPSERT needed).
// bools/ints/times drive payload coercion (see coerceFields): clients store
// rows in SQLite, so their JSON payloads carry 0/1 for booleans, floats for
// integers and strings (or "") for timestamps — SQLite accepts all of that
// verbatim, Postgres and MySQL do not.
type entitySpec struct {
	table   string
	cols    []string
	setCols []string
	bools   []string
	ints    []string
	times   []string
}

var entityCols = map[string]entitySpec{
	"apiary": {
		table: "apiary",
		cols:  []string{"id", "organization_id", "name", "address", "lat", "lng", "note", "created_at", "updated_at", "deleted"},
		bools: []string{"deleted"},
		times: []string{"created_at", "updated_at"},
	},
	"hive": {
		table: "hive",
		cols:  []string{"id", "organization_id", "apiary_id", "name", "type", "status", "boxes", "colony_origin", "note", "qr_code", "photo", "created_at", "updated_at", "deleted"},
		bools: []string{"deleted"},
		ints:  []string{"type", "status", "boxes"},
		times: []string{"created_at", "updated_at"},
	},
	"queen": {
		table: "queen",
		cols:  []string{"id", "organization_id", "hive_id", "year", "marking", "origin", "breeder_number", "introduced_at", "replaced_at", "active", "note", "created_at", "updated_at", "deleted"},
		bools: []string{"active", "deleted"},
		ints:  []string{"year", "marking"},
		times: []string{"introduced_at", "replaced_at", "created_at", "updated_at"},
	},
	"inspection": {
		table:   "inspection",
		cols:    []string{"id", "organization_id", "hive_id", "date", "weather", "queen_seen", "eggs_seen", "temperament", "frames", "stores", "queen_cells", "varroa", "honey_kg", "note", "brood_frames", "calmness", "fed_kg", "frames_added", "frames_removed", "drone_frame_cut", "super_added", "weight_kg", "youngest_larva", "covered_larva", "temp_hive", "temp_outside", "humidity_hive", "humidity_outside", "created_at", "deleted"},
		setCols: []string{"photo_keys"},
		bools:   []string{"queen_seen", "eggs_seen", "drone_frame_cut", "super_added", "covered_larva", "deleted"},
		ints:    []string{"temperament", "frames", "stores", "queen_cells", "brood_frames", "calmness", "frames_added", "frames_removed", "youngest_larva"},
		times:   []string{"date", "created_at"},
	},
	"task": {
		table: "task",
		cols:  []string{"id", "organization_id", "title", "hive_id", "apiary_id", "due_at", "done", "priority", "note", "recurrence", "assigned_to", "created_at", "deleted"},
		bools: []string{"done", "deleted"},
		ints:  []string{"priority"},
		times: []string{"due_at", "created_at"},
	},
	"placement": {
		table: "placement",
		cols:  []string{"id", "organization_id", "hive_id", "apiary_id", "start_at", "end_at", "deleted"},
		bools: []string{"deleted"},
		times: []string{"start_at", "end_at"},
	},
	"harvest": {
		table: "harvest",
		cols:  []string{"id", "organization_id", "apiary_id", "hive_id", "queen_id", "date", "variety", "amount_kg", "water_content", "batch_number", "best_before", "note", "deleted"},
		bools: []string{"deleted"},
		times: []string{"date", "best_before"},
	},
	"treatment": {
		table: "treatment",
		cols:  []string{"id", "organization_id", "apiary_id", "hive_id", "queen_id", "date", "product", "active_ingredient", "dose", "method", "batch_number", "withdrawal_until", "reason", "note", "deleted"},
		bools: []string{"deleted"},
		times: []string{"date", "withdrawal_until"},
	},
	"event": {
		table: "event",
		cols:  []string{"id", "organization_id", "scope_id", "type", "date", "apiary_id", "hive_id", "queen_id", "ref_entity", "ref_id", "title", "amount_kg", "detail", "author_id", "deleted"},
		bools: []string{"deleted"},
		ints:  []string{"type"},
		times: []string{"date"},
	},
}

func isSetCol(spec entitySpec, col string) bool {
	for _, c := range spec.setCols {
		if c == col {
			return true
		}
	}
	return false
}

func inCols(spec entitySpec, col string) bool {
	for _, c := range spec.cols {
		if c == col {
			return true
		}
	}
	return false
}

// --- small conversion helpers for MapScan / JSON ---

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

func toStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// coerceFields normalizes JSON payload values to what the target dialect's
// column types accept. SQLite stores whatever it is given, but Postgres
// rejects integers in BOOLEAN columns and MySQL cannot parse RFC3339 into
// DATETIME, so: 0/1 -> bool, integral floats -> int64, "" -> NULL for
// timestamps, and (outside SQLite) RFC3339 strings -> time.Time.
func coerceFields(spec entitySpec, fields map[string]any, driver string) {
	for _, c := range spec.bools {
		switch v := fields[c].(type) {
		case float64:
			fields[c] = v != 0
		case string:
			fields[c] = v == "1" || v == "true"
		}
	}
	for _, c := range spec.ints {
		if v, ok := fields[c].(float64); ok {
			fields[c] = int64(v)
		}
	}
	for _, c := range spec.times {
		v, ok := fields[c].(string)
		if !ok {
			continue
		}
		if v == "" {
			fields[c] = nil // nullable timestamp: "" means "not set"
			continue
		}
		if driver == "sqlite" {
			continue // stored verbatim, as the clients themselves do
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			fields[c] = t
		}
		// other layouts (e.g. date-only) stay strings; Postgres and MySQL
		// parse those natively.
	}
}

type SyncService struct {
	db  *sqlx.DB
	hlc *wsync.HLC
}

func NewSyncService(db *sqlx.DB, hlc *wsync.HLC) *SyncService { return &SyncService{db: db, hlc: hlc} }

// --- Pull: returns changes within the scopes visible to the user ---

func (s *SyncService) Pull(
	ctx context.Context, req *connect.Request[wv1.PullRequest],
) (*connect.Response[wv1.PullResponse], error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	scopes := s.accessibleScopes(ctx, id.UserID, id.OrgID)
	if len(scopes) == 0 {
		return connect.NewResponse(&wv1.PullResponse{NextCursor: req.Msg.Cursor}), nil
	}
	limit := int(req.Msg.Limit)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q, args, _ := sqlx.In(`
		SELECT scope_id, entity, entity_id, op, payload, hlc, author_id, seq
		FROM change_log
		WHERE scope_id IN (?) AND seq > ?
		ORDER BY seq ASC LIMIT ?`, scopes, cursorToSeq(req.Msg.Cursor), limit+1)
	rows, err := s.db.QueryxContext(ctx, s.db.Rebind(q), args...)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*wv1.Change
	var seqs []int64
	for rows.Next() {
		var c struct {
			ScopeID, Entity, EntityID, Payload, HLC, Author string
			Op                                              int32
			Seq                                             int64
		}
		if err := rows.Scan(&c.ScopeID, &c.Entity, &c.EntityID, &c.Op, &c.Payload, &c.HLC, &c.Author, &c.Seq); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		out = append(out, &wv1.Change{
			Entity: c.Entity, EntityId: c.EntityID, ScopeId: c.ScopeID,
			Op: wv1.ChangeOp(c.Op), PayloadJson: c.Payload, Hlc: c.HLC, AuthorId: c.Author,
		})
		seqs = append(seqs, c.Seq)
	}

	// limit+1 rows were requested so we can tell whether more pages follow.
	hasMore := len(out) > limit
	if hasMore {
		out, seqs = out[:limit], seqs[:limit]
	}
	next := req.Msg.Cursor
	if len(seqs) > 0 {
		next = seqToCursor(seqs[len(seqs)-1])
	}
	return connect.NewResponse(&wv1.PullResponse{
		Changes: out, NextCursor: next, HasMore: hasMore,
	}), nil
}

// --- Push: applies incoming changes via LWW ---

func (s *SyncService) Push(
	ctx context.Context, req *connect.Request[wv1.PushRequest],
) (*connect.Response[wv1.PushResponse], error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	// Writable scopes for this user: same set Pull reads from. Changes outside
	// it are rejected — otherwise any authenticated user could inject rows into
	// a foreign tenant's change feed by picking its scope id.
	scopes := s.accessibleScopes(ctx, id.UserID, id.OrgID)
	scopeSet := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		scopeSet[sc] = true
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, ch := range req.Msg.Changes {
		s.hlc.Recv(ch.Hlc) // sync clock with origin time

		spec, ok := entityCols[ch.Entity]
		if !ok {
			continue
		}
		if err := allowScope(scopeSet, ch); err != nil {
			return nil, err
		}
		if err := applyLogged(ctx, tx, spec, ch, id.OrgID); err != nil {
			return nil, err
		}
	}
	var cursor int64
	_ = tx.GetContext(ctx, &cursor, "SELECT val FROM seq_counter WHERE name = 'change'")
	if err := tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&wv1.PushResponse{
		ServerCursor: seqToCursor(cursor), Conflicts: nil,
	}), nil
}

// allowScope rejects a change outside the user's writable scopes. A new apiary
// opens its own scope (scope id = apiary id); its tenant is still enforced
// against the row in applyChange.
func allowScope(scopeSet map[string]bool, ch *wv1.Change) error {
	if scopeSet[ch.ScopeId] {
		return nil
	}
	if ch.Entity == "apiary" && ch.ScopeId == ch.EntityId {
		scopeSet[ch.ScopeId] = true
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied,
		fmt.Errorf("scope %q is not writable for this user", ch.ScopeId))
}

// applyLogged merges one change (per-field LWW + OR-Set; stale fields are
// dropped individually, newer fields from other devices are kept) and appends
// it to the change log.
func applyLogged(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, orgID string) error {
	if err := applyChange(ctx, tx, spec, ch, orgID); err != nil {
		if errors.Is(err, errWrongTenant) {
			return connect.NewError(connect.CodePermissionDenied, err)
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	if err := appendChangeLog(ctx, tx, ch, orgID); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

// --- Subscribe: optional live channel. Polls the global change cursor and
// nudges the device whenever it advances, so it can pull immediately. ---

func (s *SyncService) Subscribe(
	ctx context.Context, req *connect.Request[wv1.SubscribeRequest],
	stream *connect.ServerStream[wv1.SubscribeEvent],
) error {
	if _, ok := auth.FromContext(ctx); !ok {
		return connect.NewError(connect.CodeUnauthenticated, nil)
	}
	last := cursorToSeq(req.Msg.Cursor)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			var cur int64
			if err := s.db.GetContext(ctx, &cur,
				"SELECT val FROM seq_counter WHERE name = 'change'"); err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
			if cur > last {
				last = cur
				if err := stream.Send(&wv1.SubscribeEvent{ServerCursor: seqToCursor(cur)}); err != nil {
					return err
				}
			}
		}
	}
}

// errWrongTenant rejects changes that address rows of another tenant.
var errWrongTenant = errors.New("change targets another tenant's data")

// applyChange merges an incoming change field-by-field into the row:
// scalars by LWW (field clock), set columns by OR-Set union.
// orgID is the caller's active tenant: existing rows must belong to it, new
// rows are stamped with it — a client cannot write into a foreign tenant, and
// cannot move a row across tenants, no matter what its payload claims.
func applyChange(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, orgID string) error {
	fields := map[string]any{}
	if ch.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
		fields["deleted"] = true
	} else if err := json.Unmarshal([]byte(ch.PayloadJson), &fields); err != nil {
		return err
	}
	coerceFields(spec, fields, tx.DriverName())
	if v, ok := fields["organization_id"]; ok {
		if s := asString(v); s == "" {
			fields["organization_id"] = orgID // unset -> stamp the caller's tenant
		} else if s != orgID {
			return errWrongTenant
		}
	}

	// Load the current row state (for field clock and set columns).
	row := map[string]any{}
	rows, err := tx.QueryxContext(ctx, tx.Rebind(
		fmt.Sprintf("SELECT * FROM %s WHERE id = ?", spec.table)), ch.EntityId)
	if err != nil {
		return err
	}
	exists := rows.Next()
	if exists {
		_ = rows.MapScan(row)
	}
	_ = rows.Close()

	if exists {
		if rowOrg := asString(row["organization_id"]); rowOrg != "" && rowOrg != orgID {
			return errWrongTenant
		}
		return updateMerge(ctx, tx, spec, ch, fields, row)
	}
	fields["organization_id"] = orgID // new rows always belong to the caller's tenant
	return insertNew(ctx, tx, spec, ch, fields)
}

// insertNew: new row. All provided fields are stamped with ch.Hlc.
func insertNew(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, fields map[string]any) error {
	fc := wsync.FieldClock{}
	cols := []string{"id"}
	args := []any{ch.EntityId}

	for _, c := range spec.cols {
		if c == "id" {
			continue
		}
		if v, ok := fields[c]; ok {
			cols = append(cols, c)
			args = append(args, v)
			fc[c] = ch.Hlc
		}
	}
	for _, sc := range spec.setCols {
		os := wsync.ORSet{}
		if d, ok := fields[sc].(map[string]any); ok {
			for _, e := range toStrings(d["add"]) {
				os.Add(e, ch.Hlc)
			}
		}
		cols = append(cols, sc)
		args = append(args, os.Marshal())
	}
	cols = append(cols, "field_hlc")
	args = append(args, fc.Marshal())

	ph := make([]string, len(cols))
	for i := range ph {
		ph[i] = "?"
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", spec.table,
		strings.Join(cols, ", "), strings.Join(ph, ", "))
	_, err := tx.ExecContext(ctx, tx.Rebind(q), args...)
	return err
}

// updateMerge: merge an existing row field-by-field.
func updateMerge(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, fields, row map[string]any) error {
	fc := wsync.ParseFieldClock(asString(row["field_hlc"]))
	var set []string
	var args []any

	for c, v := range fields {
		if c == "id" || c == "field_hlc" {
			continue
		}
		switch {
		case isSetCol(spec, c):
			// OR-Set: union adds/removes from the delta (merge, not LWW).
			os := wsync.ParseORSet(asString(row[c]))
			if d, ok := v.(map[string]any); ok {
				mergeORSetDelta(os, d, ch.Hlc)
			}
			set = append(set, c+" = ?")
			args = append(args, os.Marshal())
		case inCols(spec, c) && fc.Accept(c, ch.Hlc):
			// Scalar: apply only if ch.Hlc is newer than the field clock.
			set = append(set, c+" = ?")
			args = append(args, v)
		}
	}
	if len(set) == 0 {
		return nil // everything stale -> nothing to do
	}
	set = append(set, "field_hlc = ?")
	args = append(args, fc.Marshal())
	args = append(args, ch.EntityId)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", spec.table, strings.Join(set, ", "))
	_, err := tx.ExecContext(ctx, tx.Rebind(q), args...)
	return err
}

// mergeORSetDelta applies an add/remove delta to an OR-Set. Newer deltas carry
// the origin's observed tags; only those are tombstoned (add-wins). Old deltas
// remove all present.
func mergeORSetDelta(os wsync.ORSet, d map[string]any, hlc string) {
	for _, e := range toStrings(d["add"]) {
		os.Add(e, hlc)
	}
	removedTags, _ := d["removed_tags"].(map[string]any)
	for _, e := range toStrings(d["remove"]) {
		if tags := toStrings(removedTags[e]); len(tags) > 0 {
			os.RemoveTags(e, tags)
		} else {
			os.Remove(e)
		}
	}
}

func appendChangeLog(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change, orgID string) error {
	// Increment the receive sequence atomically (portable, one writer per tx).
	if _, err := tx.ExecContext(ctx, "UPDATE seq_counter SET val = val + 1 WHERE name = 'change'"); err != nil {
		return err
	}
	var seq int64
	if err := tx.GetContext(ctx, &seq, "SELECT val FROM seq_counter WHERE name = 'change'"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, tx.Rebind(`
		INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		uuid.NewString(), seq, ch.ScopeId, ch.Entity, ch.EntityId, int32(ch.Op), ch.PayloadJson, ch.Hlc, ch.AuthorId, orgID)
	return err
}

// accessibleScopes = personal scope + own apiaries + shared apiaries.
func (s *SyncService) accessibleScopes(ctx context.Context, userID, orgID string) []string {
	scopes := []string{"user:" + userID}
	var owned []string
	_ = s.db.SelectContext(ctx, &owned, s.db.Rebind(
		`SELECT id FROM apiary WHERE organization_id = ?`), orgID)
	scopes = append(scopes, owned...)
	var shared []string
	_ = s.db.SelectContext(ctx, &shared, s.db.Rebind(
		`SELECT apiary_id FROM apiary_share WHERE benutzer_id = ?`), userID)
	return append(scopes, shared...)
}

func cursorToSeq(c string) int64 {
	n, _ := strconv.ParseInt(c, 10, 64)
	return n
}

func seqToCursor(seq int64) string { return strconv.FormatInt(seq, 10) }
