package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Column whitelist per entity. cols are merged via per-field LWW,
// setCols as OR-Set (add-wins). Both stay portable (no UPSERT needed).
// bools/ints/times drive payload validation and coercion (sync_payload.go):
// clients store rows in SQLite, so their JSON payloads carry 0/1 for
// booleans, floats for integers and strings (or "") for timestamps; SQLite
// accepts all of that verbatim, Postgres and MySQL do not.
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
		cols:    []string{"id", "organization_id", "hive_id", "date", "weather", "queen_seen", "eggs_seen", "temperament", "frames", "stores", "queen_cells", "varroa", "honey_kg", "note", "brood_frames", "calmness", "fed_kg", "frames_added", "frames_removed", "drone_frame_cut", "super_added", "weight_kg", "youngest_larva", "covered_larva", "temp_hive", "temp_outside", "humidity_hive", "humidity_outside", "source", "created_at", "deleted"},
		setCols: []string{"photo_keys"},
		bools:   []string{"queen_seen", "eggs_seen", "drone_frame_cut", "super_added", "covered_larva", "deleted"},
		ints:    []string{"temperament", "frames", "stores", "queen_cells", "brood_frames", "calmness", "frames_added", "frames_removed", "youngest_larva", "source"},
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
		cols:  []string{"id", "organization_id", "apiary_id", "hive_id", "queen_id", "date", "variety", "amount_kg", "water_content", "batch_number", "best_before", "note", "amended_at", "deleted"},
		bools: []string{"deleted"},
		times: []string{"date", "best_before", "amended_at"},
	},
	"treatment": {
		table: "treatment",
		cols:  []string{"id", "organization_id", "apiary_id", "hive_id", "queen_id", "date", "product", "active_ingredient", "dose", "method", "batch_number", "withdrawal_until", "reason", "note", "amended_at", "deleted"},
		bools: []string{"deleted"},
		times: []string{"date", "withdrawal_until", "amended_at"},
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

// asString reads a text column from a MapScan row (MySQL returns []byte).
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

const (
	// maxPushChanges bounds one Push request; the app sends at most 200.
	maxPushChanges = 2000
	// maxSubscribeStreams bounds the open Subscribe streams per user. Each
	// stream polls the database, so an unbounded number is a cheap way to
	// load the server.
	maxSubscribeStreams = 4
	subscribePoll       = 2 * time.Second
)

type SyncService struct {
	db  *sqlx.DB
	hlc *wsync.HLC

	poll    time.Duration // Subscribe poll interval
	mu      sync.Mutex
	streams map[string]int // open Subscribe streams per user
}

func NewSyncService(db *sqlx.DB, hlc *wsync.HLC) *SyncService {
	return &SyncService{db: db, hlc: hlc, poll: subscribePoll, streams: map[string]int{}}
}

func internalErr(err error) error {
	return connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
}

// --- Pull: returns the changes of the caller's workspace ---

// The workspace (tenant) is the only sharing unit: a device sees every change
// of the workspace it is signed in to. The exception are personal rows
// (scope "user:<id>"), which only their owner sees, and only inside the
// workspace they were written in. idx_change_org_seq serves both queries.
const (
	pullQuery = `
		SELECT scope_id, entity, entity_id, op, payload, hlc, author_id, seq
		FROM change_log
		WHERE org_id = ? AND seq > ? AND (scope_id NOT LIKE 'user:%' OR scope_id = ?)
		ORDER BY seq ASC LIMIT ?`
	latestQuery = `
		SELECT seq FROM change_log
		WHERE org_id = ? AND (scope_id NOT LIKE 'user:%' OR scope_id = ?)
		ORDER BY seq DESC LIMIT 1`
)

func (s *SyncService) Pull(
	ctx context.Context, req *wv1.PullRequest,
) (*wv1.PullResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	limit := int(req.Limit)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// limit+1 rows are requested to tell whether more pages follow.
	rows, err := s.db.QueryxContext(ctx, s.db.Rebind(pullQuery),
		id.OrgID, cursorToSeq(req.Cursor), personalScope(id.UserID), limit+1)
	if err != nil {
		return nil, internalErr(err)
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
			return nil, internalErr(err)
		}
		out = append(out, &wv1.Change{
			Entity: c.Entity, EntityId: c.EntityID, ScopeId: c.ScopeID,
			Op: wv1.ChangeOp(c.Op), PayloadJson: c.Payload, Hlc: c.HLC, AuthorId: c.Author,
		})
		seqs = append(seqs, c.Seq)
	}
	// A page cut short by a read error must not look complete: the device
	// would move its cursor past rows it never received.
	if err := rows.Err(); err != nil {
		return nil, internalErr(err)
	}

	hasMore := len(out) > limit
	if hasMore {
		out, seqs = out[:limit], seqs[:limit]
	}
	next := req.Cursor
	if len(seqs) > 0 {
		next = seqToCursor(seqs[len(seqs)-1])
	}
	return &wv1.PullResponse{
		Changes: out, NextCursor: next, HasMore: hasMore,
		ServerTimeMs: time.Now().UnixMilli(),
	}, nil
}

// --- Push: applies incoming changes one by one ---

// Push merges every change inside its own savepoint and reports the ones it
// refuses in the response; the rest of the batch is applied. Only an error
// that is not about a single change (database down, request cancelled)
// fails the request, and then nothing of it is applied.
func (s *SyncService) Push(
	ctx context.Context, req *wv1.PushRequest,
) (*wv1.PushResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	// Read-only identities (demo account, read-scoped API keys) upload
	// nothing at all; the device discards the attempt.
	if id.ReadOnly || id.Role == "demo" {
		return nil, connect.NewError(connect.CodePermissionDenied, "this identity is read-only")
	}
	if len(req.Changes) > maxPushChanges {
		return nil, connect.Errorf(connect.CodeInvalidArgument,
			"a push carries at most %d changes", maxPushChanges)
	}
	now := time.Now()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, internalErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockWrites(ctx, tx); err != nil {
		return nil, internalErr(err)
	}
	scopes, err := loadScopes(ctx, tx, id)
	if err != nil {
		return nil, internalErr(err)
	}

	var rejected []*wv1.RejectedChange
	var accepted []string
	for i, ch := range req.Changes {
		// The author is whoever is signed in, not what the device claims.
		ch.AuthorId = id.UserID
		cerr, err := pushOne(ctx, tx, scopes, id.OrgID, ch, now)
		if err != nil {
			return nil, internalErr(err)
		}
		if cerr != nil {
			rejected = append(rejected, &wv1.RejectedChange{
				Index: int32(i), Code: cerr.code, Reason: cerr.reason,
			})
			continue
		}
		accepted = append(accepted, ch.Hlc)
	}
	if err := tx.Commit(); err != nil {
		return nil, internalErr(err)
	}
	// The server clock follows only stamps that were validated and stored:
	// a refused or rolled-back change must not move it.
	for _, stamp := range accepted {
		_ = s.hlc.Recv(stamp)
	}
	return &wv1.PushResponse{Rejected: rejected, ServerTimeMs: now.UnixMilli()}, nil
}

// lockWrites serializes everything that merges rows and appends to the
// change log, by taking the row lock on the sequence counter first. Merging
// reads a row and writes back its field clock and set columns; without the
// lock two transactions on Postgres or MySQL read the same state and the
// later commit silently undoes the earlier one. Taking it before any row is
// touched also gives every writer the same lock order.
func lockWrites(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE seq_counter SET val = val WHERE name = 'change'")
	return err
}

// scopeSet holds the scopes the caller may write: the personal scope and
// the apiaries of the caller's workspace.
type scopeSet struct {
	personal string
	apiaries map[string]bool
}

func loadScopes(ctx context.Context, tx *sqlx.Tx, id auth.Identity) (*scopeSet, error) {
	var ids []string
	if err := tx.SelectContext(ctx, &ids, tx.Rebind(
		`SELECT id FROM apiary WHERE organization_id = ?`), id.OrgID); err != nil {
		return nil, err
	}
	set := &scopeSet{personal: personalScope(id.UserID), apiaries: make(map[string]bool, len(ids))}
	for _, a := range ids {
		set.apiaries[a] = true
	}
	return set, nil
}

// allows reports whether the change's scope is writable. A new apiary opens
// its own scope (scope id = apiary id); that the id is not another
// workspace's apiary is enforced on the row in applyChange.
func (s *scopeSet) allows(ch *wv1.Change) bool {
	if ch.ScopeId == s.personal || s.apiaries[ch.ScopeId] {
		return true
	}
	return ch.Entity == "apiary" && ch.ScopeId == ch.EntityId
}

// opened records the scope of an apiary that was just stored.
func (s *scopeSet) opened(ch *wv1.Change) {
	if ch.Entity == "apiary" {
		s.apiaries[ch.EntityId] = true
	}
}

// pushOne applies one change inside a savepoint. It returns a changeError
// when the change itself is refused (the savepoint is rolled back, the
// transaction stays usable) and an error when the request cannot continue.
func pushOne(ctx context.Context, tx *sqlx.Tx, scopes *scopeSet, orgID string, ch *wv1.Change, now time.Time) (*changeError, error) {
	spec, cerr := checkChange(ch, now)
	if cerr != nil {
		return cerr, nil
	}
	if !scopes.allows(ch) {
		return &changeError{code: rejForbiddenScope,
			reason: fmt.Sprintf("scope %q is not writable for this user", clip(ch.ScopeId))}, nil
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT obh_change"); err != nil {
		return nil, err
	}
	applyErr := applyLogged(ctx, tx, spec, ch, orgID)
	if applyErr == nil {
		scopes.opened(ch)
		_, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT obh_change")
		return nil, err
	}
	// Undo whatever the change wrote before it failed. If even that fails the
	// transaction is gone (deadlock victim, dropped connection): give up.
	for _, q := range []string{"ROLLBACK TO SAVEPOINT obh_change", "RELEASE SAVEPOINT obh_change"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return nil, errors.Join(applyErr, err)
		}
	}
	if cerr := asChangeError(applyErr, sqlstore.IsDataError); cerr != nil {
		return cerr, nil
	}
	return nil, applyErr
}

// applyLogged merges one change (per-field LWW + OR-Set; stale fields are
// dropped individually, newer fields from other devices are kept) and appends
// it to the change log.
func applyLogged(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, orgID string) error {
	if err := applyChange(ctx, tx, spec, ch, orgID); err != nil {
		return err
	}
	return appendChangeLog(ctx, tx, ch, orgID)
}

// --- Subscribe: optional live channel. Polls the latest change the caller
// can see and nudges the device whenever it advances, so it can pull
// immediately. ---

func (s *SyncService) Subscribe(
	ctx context.Context, req *wv1.SubscribeRequest,
	stream openbeehivev1connect.SyncServiceSubscribeServerStream,
) error {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, "")
	}
	return s.subscribe(ctx, id, cursorToSeq(req.Cursor), stream.Send)
}

func (s *SyncService) subscribe(ctx context.Context, id auth.Identity, last int64, send func(*wv1.SubscribeEvent) error) error {
	if !s.openStream(id.UserID) {
		return connect.Errorf(connect.CodeResourceExhausted,
			"at most %d live sync streams per user", maxSubscribeStreams)
	}
	defer s.closeStream(id.UserID)

	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cur, err := s.latestSeq(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return nil // the client left while the query was running
				}
				return internalErr(err)
			}
			if cur > last {
				last = cur
				if err := send(&wv1.SubscribeEvent{ServerCursor: seqToCursor(cur)}); err != nil {
					return err
				}
			}
		}
	}
}

// latestSeq is the sequence of the newest change the identity can pull, so
// activity in other workspaces neither wakes a device nor shows in the
// number it receives.
func (s *SyncService) latestSeq(ctx context.Context, id auth.Identity) (int64, error) {
	var seq int64
	err := s.db.GetContext(ctx, &seq, s.db.Rebind(latestQuery), id.OrgID, personalScope(id.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (s *SyncService) openStream(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams[userID] >= maxSubscribeStreams {
		return false
	}
	s.streams[userID]++
	return true
}

func (s *SyncService) closeStream(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams[userID]--; s.streams[userID] <= 0 {
		delete(s.streams, userID)
	}
}

// errWrongTenant rejects changes that address rows of another tenant.
var errWrongTenant = errors.New("change targets another tenant's data")

// applyChange merges an incoming change field-by-field into the row:
// scalars by LWW (field clock), set columns by OR-Set union.
// orgID is the caller's active tenant: existing rows must belong to it, new
// rows are stamped with it; a client cannot write into a foreign tenant, and
// cannot move a row across tenants, no matter what its payload claims.
//
// The payload is validated and reduced to the entity's known columns first,
// and ch.PayloadJson is replaced by that normalised form: it is what the
// change log stores and every other device receives.
func applyChange(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, orgID string) error {
	if !wsync.Valid(ch.Hlc) {
		return invalidChange("malformed hlc stamp %q", clip(ch.Hlc))
	}
	fields, cerr := wireFields(ch.Entity, spec, ch)
	if cerr != nil {
		return cerr
	}
	if v, ok := fields["organization_id"]; ok {
		if org, _ := v.(string); org == "" {
			fields["organization_id"] = orgID // unset -> stamp the caller's tenant
		} else if org != orgID {
			return errWrongTenant
		}
	}

	row, exists, rowOrg, err := loadMergeState(ctx, tx, spec, ch.EntityId)
	if err != nil {
		return err
	}
	if exists && rowOrg != "" && rowOrg != orgID {
		return errWrongTenant
	}
	if !exists {
		fields["organization_id"] = orgID // new rows always belong to the caller's tenant
	}

	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	ch.PayloadJson = string(payload)

	changed := row.Apply(fields, ch.Hlc)
	values := dbValues(ch.Entity, fields, tx.DriverName())
	for _, sc := range spec.setCols {
		values[sc] = row.Sets[sc].Marshal()
	}
	values["field_hlc"] = row.Clock.Marshal()
	if !exists {
		// A new row stores every set column, also the ones the change does
		// not mention: their column default is not valid OR-Set JSON.
		return insertRow(ctx, tx, spec, ch.EntityId, union(changed, spec.setCols), values)
	}
	if len(changed) == 0 {
		return nil // everything stale -> nothing to do
	}
	return updateRow(ctx, tx, spec, ch.EntityId, changed, values)
}

// loadMergeState reads what the merge needs from the stored row: its
// tenant, field clock and set columns.
func loadMergeState(ctx context.Context, tx *sqlx.Tx, spec entitySpec, id string) (row wsync.Row, exists bool, org string, err error) {
	row = wsync.Row{Clock: wsync.FieldClock{}, Sets: map[string]wsync.ORSet{}}
	for _, sc := range spec.setCols {
		row.Sets[sc] = wsync.ORSet{}
	}
	cols := append([]string{"organization_id", "field_hlc"}, spec.setCols...)
	rows, err := queryMaps(ctx, tx, fmt.Sprintf("SELECT %s FROM %s WHERE id = ?",
		strings.Join(cols, ", "), spec.table), id)
	if err != nil || len(rows) == 0 {
		return row, false, "", err
	}
	stored := rows[0]
	row.Clock = wsync.ParseFieldClock(asString(stored["field_hlc"]))
	for _, sc := range spec.setCols {
		row.Sets[sc] = wsync.ParseORSet(asString(stored[sc]))
	}
	return row, true, asString(stored["organization_id"]), nil
}

func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, list := range [][]string{a, b} {
		for _, c := range list {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// insertRow writes a new row with the given columns plus its field clock.
// Column names come from the entity whitelist, never from the payload.
func insertRow(ctx context.Context, tx *sqlx.Tx, spec entitySpec, id string, cols []string, values map[string]any) error {
	names := []string{"id"}
	args := []any{id}
	for _, c := range append(cols, "field_hlc") {
		names = append(names, c)
		args = append(args, values[c])
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", spec.table,
		strings.Join(names, ", "), strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", "))
	_, err := tx.ExecContext(ctx, tx.Rebind(q), args...)
	return err
}

// updateRow writes the merged columns of an existing row and its field clock.
func updateRow(ctx context.Context, tx *sqlx.Tx, spec entitySpec, id string, cols []string, values map[string]any) error {
	var set []string
	var args []any
	for _, c := range append(cols, "field_hlc") {
		set = append(set, c+" = ?")
		args = append(args, values[c])
	}
	args = append(args, id)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", spec.table, strings.Join(set, ", "))
	_, err := tx.ExecContext(ctx, tx.Rebind(q), args...)
	return err
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

func cursorToSeq(c string) int64 {
	n, _ := strconv.ParseInt(c, 10, 64)
	return n
}

func seqToCursor(seq int64) string { return strconv.FormatInt(seq, 10) }
