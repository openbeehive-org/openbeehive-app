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
	// perUser marks the one entity whose rows belong to an account instead
	// of a workspace (see sync_user.go).
	perUser bool
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
	// A photo carries only its thumbnail; the full image is transferred
	// through /photos/ (see photo.go).
	"photo": {
		table: "photo",
		cols:  []string{"id", "organization_id", "owner_entity", "owner_id", "thumb", "content_type", "size_bytes", "created_at", "deleted"},
		bools: []string{"deleted"},
		ints:  []string{"size_bytes"},
		times: []string{"created_at"},
	},
	"user_settings": {
		table:   "user_settings",
		cols:    []string{"id", "weight_unit", "temp_unit", "language", "deleted"},
		bools:   []string{"deleted"},
		perUser: true,
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
	db     *sqlx.DB
	hlc    *wsync.HLC
	photos *Photos

	// MinProtocol is the lowest sync protocol version a client may speak;
	// 0 also admits 0.4.x apps, which announce none.
	MinProtocol int
	refused     sync.Map // protocol version -> true: refusal already logged

	poll time.Duration // Subscribe poll interval
	// pageBytes is the size at which a snapshot page is closed.
	pageBytes int
	mu        sync.Mutex
	streams   map[string]int // open Subscribe streams per user
}

func NewSyncService(db *sqlx.DB, hlc *wsync.HLC) *SyncService {
	return &SyncService{db: db, hlc: hlc, photos: NewPhotos(db, hlc, nil), poll: subscribePoll, pageBytes: snapshotPageBytes, streams: map[string]int{}}
}

// UsePhotos attaches the photo store with its blob backend; without it the
// service converts no inline photos of 0.4.x apps.
func (s *SyncService) UsePhotos(p *Photos) { s.photos = p }

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

// logEntry is one change-log row as Pull reads it.
type logEntry struct {
	seq    int64
	change *wv1.Change
}

// Pull returns the changes after the caller's cursor. What happens around
// that depends on who is asking (see sync.proto, "Protocol version"):
//
// A client that announces a protocol version has its cursor checked against
// this database (checkCursor) and, instead of changes, may get the status
// that sends it to Snapshot.
//
// A legacy 0.4.x app gets the changes reduced to what it can store
// (forLegacy), and a failure it cannot miss when the log no longer reaches
// back to its cursor.
func (s *SyncService) Pull(
	ctx context.Context, req *wv1.PullRequest,
) (*wv1.PullResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	client, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	st, err := s.state(ctx)
	if err != nil {
		return nil, internalErr(err)
	}
	res := &wv1.PullResponse{NextCursor: req.Cursor, ServerTimeMs: time.Now().UnixMilli()}
	after, err := s.pullStart(ctx, client, st, req.Cursor, res)
	if err != nil {
		return nil, internalErr(err)
	}
	if res.Status != wv1.PullStatus_PULL_STATUS_UNSPECIFIED {
		return res, nil
	}

	// limit+1 rows are requested to tell whether more pages follow.
	page, err := s.readLog(ctx, id, after, limit+1)
	if err != nil {
		return nil, internalErr(err)
	}
	// Compaction may have removed entries while the page was read. It stores
	// the horizon first, so looking again now tells whether this page can
	// have a hole.
	now, err := s.state(ctx)
	if err != nil {
		return nil, internalErr(err)
	}
	if after < now.horizon {
		if client.legacy() {
			return nil, upgradeRequired(protocolSnapshots,
				"the server no longer keeps the changes made since this device last synced")
		}
		res.Status = wv1.PullStatus_PULL_STATUS_SNAPSHOT_REQUIRED
		return res, nil
	}

	res.HasMore = len(page) > limit
	if res.HasMore {
		page = page[:limit]
	}
	for _, e := range page {
		// Each kind of client gets the form of the change it can use.
		if (client.legacy() && forLegacy(e.change)) || (!client.legacy() && forCurrent(e.change)) {
			res.Changes = append(res.Changes, e.change)
		}
	}
	if err := s.pullEnd(ctx, client, st, after, page, res); err != nil {
		return nil, internalErr(err)
	}
	return res, nil
}

// pullStart turns the request's cursor into the sequence number to read
// after. For a client that announced a protocol version it may set
// res.Status instead, and then nothing is read.
func (s *SyncService) pullStart(ctx context.Context, client syncClient, st syncState, raw string, res *wv1.PullResponse) (int64, error) {
	if !client.legacy() {
		after, status, err := s.checkCursor(ctx, st, raw)
		res.Status = status
		return after, err
	}
	// A position beyond the end of the log comes from before a restore: such
	// a device reads the log again from the start, which merges without
	// effect where it already has the rows.
	after := cursorToSeq(raw)
	if after > st.head {
		after = 0
		res.NextCursor = seqToCursor(0)
	}
	return after, nil
}

// pullEnd sets res.NextCursor, where the device continues: after the last
// entry of a full page, and otherwise at the end of the log as it was when
// the call began. Moving on without changes of its own keeps a quiet
// workspace ahead of the compaction horizon. A device that is already there
// keeps the cursor it sent.
func (s *SyncService) pullEnd(ctx context.Context, client syncClient, st syncState, after int64, page []logEntry, res *wv1.PullResponse) error {
	pos := after
	if len(page) > 0 {
		pos = page[len(page)-1].seq
	}
	if !res.HasMore && st.head > pos {
		pos = st.head
	}
	if pos == after && (client.legacy() || res.NextCursor != "") {
		return nil
	}
	if client.legacy() {
		res.NextCursor = seqToCursor(pos)
		return nil
	}
	var err error
	res.NextCursor, err = s.cursorAt(ctx, st, pos)
	return err
}

// readLog returns up to limit changes the identity may see after seq.
func (s *SyncService) readLog(ctx context.Context, id auth.Identity, after int64, limit int) ([]logEntry, error) {
	page, err := s.queryLog(ctx, pullQuery, id.OrgID, after, personalScope(id.UserID), limit)
	if err != nil {
		return nil, err
	}
	own, err := s.queryLog(ctx, accountLogQuery, accountLog(id.UserID), after, limit)
	if err != nil {
		return nil, err
	}
	return mergeLog(page, own, limit), nil
}

// queryLog runs a change-log query that selects the columns of pullQuery.
func (s *SyncService) queryLog(ctx context.Context, query string, args ...any) ([]logEntry, error) {
	rows, err := s.db.QueryxContext(ctx, s.db.Rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []logEntry
	for rows.Next() {
		var c struct {
			ScopeID, Entity, EntityID, Payload, HLC, Author string
			Op                                              int32
			Seq                                             int64
		}
		if err := rows.Scan(&c.ScopeID, &c.Entity, &c.EntityID, &c.Op, &c.Payload, &c.HLC, &c.Author, &c.Seq); err != nil {
			return nil, err
		}
		out = append(out, logEntry{seq: c.Seq, change: &wv1.Change{
			Entity: c.Entity, EntityId: c.EntityID, ScopeId: c.ScopeID,
			Op: wv1.ChangeOp(c.Op), PayloadJson: c.Payload, Hlc: c.HLC, AuthorId: c.Author,
		}})
	}
	// A page cut short by a read error must not look complete: the device
	// would move its cursor past rows it never received.
	return out, rows.Err()
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
	client, err := s.admit(ctx)
	if err != nil {
		return nil, err
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
	ctx = withPhotos(ctx, s.photos)

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
			if cerr.code == rejClockAhead && client.legacy() {
				return nil, legacyClockAhead(cerr)
			}
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

// legacyClockAhead fails the Push of a 0.4.x app that sent a change stamped
// too far ahead. Such an app ignores the per-change rejections and empties
// its outbox after every successful Push, so a refused change would be lost
// without a trace. A failed request it retries instead, with the batch kept,
// until its clock is right again or it has updated itself; an updated app
// re-stamps such changes. Nothing of the batch is applied.
func legacyClockAhead(cerr *changeError) error {
	return upgradeRequired(protocolSnapshots, "the device clock is ahead of the server: "+cerr.reason)
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
		if !isPersonal(a) { // an apiary stored under such an id opens no scope
			set.apiaries[a] = true
		}
	}
	return set, nil
}

// isPersonal reports whether a scope or id has the form of a personal scope.
func isPersonal(id string) bool { return strings.HasPrefix(id, "user:") }

// allows reports whether the change's scope is writable. A new apiary opens
// its own scope (scope id = apiary id); that the id is not another
// workspace's apiary is enforced on the row in applyChange. What may be
// written in the personal scope is checked by checkPersonal.
func (s *scopeSet) allows(ch *wv1.Change) bool {
	if ch.ScopeId == s.personal || s.apiaries[ch.ScopeId] {
		return true
	}
	return ch.Entity == "apiary" && ch.ScopeId == ch.EntityId && !isPersonal(ch.EntityId)
}

// checkPersonal refuses what does not belong in the personal scope. Only
// the caller pulls changes logged there, so a change to a row the workspace
// shares would update the server's row while the other members never
// receive it. The personal scope holds the account's settings, tasks
// without an apiary and events that name the personal scope, as rows of
// the caller's own; everything else is written in its apiary's scope.
//
// No row id may have the form of a personal scope either: an apiary with
// the id "user:<someone>" would open that member's personal scope to
// whoever created it.
func checkPersonal(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, scopes *scopeSet) (*changeError, error) {
	if isPersonal(ch.EntityId) {
		return invalidChange("a row id must not start with \"user:\""), nil
	}
	if ch.ScopeId != scopes.personal || spec.perUser {
		return nil, nil // perUser rows: checkOwnRow
	}
	refuse := &changeError{code: rejForbiddenScope, reason: fmt.Sprintf(
		"%s %q is shared with the workspace and is written in its apiary's scope, not the personal one",
		ch.Entity, clip(ch.EntityId))}
	if ch.Entity != "task" && ch.Entity != "event" {
		return refuse, nil
	}
	if ch.Op == wv1.ChangeOp_CHANGE_OP_UPSERT {
		var p struct {
			ApiaryID *string `json:"apiary_id"`
			ScopeID  *string `json:"scope_id"`
		}
		_ = json.Unmarshal([]byte(ch.PayloadJson), &p) // a malformed payload is refused later
		if (p.ApiaryID != nil && *p.ApiaryID != "") || (p.ScopeID != nil && *p.ScopeID != scopes.personal) {
			return refuse, nil
		}
	}
	// The stored row, if any, must already be the caller's personal one.
	q := `SELECT apiary_id, owner_id AS mine FROM task WHERE id = ?`
	if ch.Entity == "event" {
		q = `SELECT apiary_id, scope_id AS mine FROM event WHERE id = ?`
	}
	rows, err := queryMaps(ctx, tx, q, ch.EntityId)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	stored, mine := rs(rows[0]["apiary_id"]), rs(rows[0]["mine"])
	owned := mine == scopes.personal // event.scope_id
	if ch.Entity == "task" {
		// owner_id is the bare user id; '' on a task never written personally.
		owned = mine != "" && personalScope(mine) == scopes.personal
	}
	if stored != "" || !owned {
		return refuse, nil
	}
	return nil, nil
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
	if cerr := checkOwnRow(spec, ch, scopes); cerr != nil {
		return cerr, nil
	}
	if cerr, err := checkPersonal(ctx, tx, spec, ch, scopes); cerr != nil || err != nil {
		return cerr, err
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
// it to the change log. Photos get their own rules before and after that
// (photo_sync.go).
func applyLogged(ctx context.Context, tx *sqlx.Tx, spec entitySpec, ch *wv1.Change, orgID string) error {
	photos := photosFrom(ctx)
	if skip, err := photos.beforeChange(ctx, tx, ch, orgID); err != nil || skip {
		return err
	}
	if err := applyChange(ctx, tx, spec, ch, orgID); err != nil {
		return err
	}
	if err := appendChangeLog(ctx, tx, ch, logOrg(spec, ch, orgID)); err != nil {
		return err
	}
	return photos.afterChange(ctx, tx, ch, orgID)
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
	if _, err := s.admit(ctx); err != nil {
		return err
	}
	return s.subscribe(ctx, id, cursorSeq(req.Cursor), stream.Send)
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	own, err := s.latestAccountSeq(ctx, id.UserID)
	return max(seq, own), err
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
	if cerr == nil && spec.perUser {
		cerr = checkSettingValues(fields)
	}
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
	if !exists && !spec.perUser {
		fields["organization_id"] = orgID // new rows always belong to the caller's tenant
	}
	stampAuthor(ch, fields, exists)

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
		err = insertRow(ctx, tx, spec, ch.EntityId, union(changed, spec.setCols), values)
	} else if len(changed) > 0 { // nothing to store when every field was stale
		err = updateRow(ctx, tx, spec, ch.EntityId, changed, values)
	}
	if err != nil {
		return err
	}
	return claimOwner(ctx, tx, ch)
}

// stampAuthor sets the author column of a record (event.author_id). Who
// wrote a record is whoever was signed in when it was created, never what a
// payload claims: the change's author, which the caller sets from the
// identity. Later changes leave it as it is.
func stampAuthor(ch *wv1.Change, fields map[string]any, exists bool) {
	if _, authored := kindsOf(ch.Entity)["author_id"]; !authored {
		return
	}
	delete(fields, "author_id")
	if !exists {
		fields["author_id"] = ch.AuthorId
	}
}

// claimOwner records who owns a personal task: the user in whose personal
// scope it is first written. Personal tasks are shown to their owner only
// (visibleTasks). owner_id lives on the server alone; it is not a synced
// column, so no payload can set or change it.
func claimOwner(ctx context.Context, tx *sqlx.Tx, ch *wv1.Change) error {
	owner, personal := strings.CutPrefix(ch.ScopeId, "user:")
	if ch.Entity != "task" || !personal || owner == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, tx.Rebind(
		`UPDATE task SET owner_id = ? WHERE id = ? AND owner_id = ''`), owner, ch.EntityId)
	return err
}

// loadMergeState reads what the merge needs from the stored row: its
// tenant, field clock and set columns.
func loadMergeState(ctx context.Context, tx *sqlx.Tx, spec entitySpec, id string) (row wsync.Row, exists bool, org string, err error) {
	row = wsync.Row{Clock: wsync.FieldClock{}, Sets: map[string]wsync.ORSet{}}
	for _, sc := range spec.setCols {
		row.Sets[sc] = wsync.ORSet{}
	}
	owner := "organization_id"
	if spec.perUser {
		owner = "''" // an account's own row: checkOwnRow has checked whose it is
	}
	cols := append([]string{owner + " AS organization_id", "field_hlc"}, spec.setCols...)
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
	// logged_at is the server's receive time: compaction goes by it.
	_, err := tx.ExecContext(ctx, tx.Rebind(`
		INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id, logged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		uuid.NewString(), seq, ch.ScopeId, ch.Entity, ch.EntityId, int32(ch.Op), ch.PayloadJson, ch.Hlc, ch.AuthorId, orgID,
		time.Now().UnixMilli())
	return err
}

// cursorToSeq reads the bare sequence number legacy clients use as cursor.
func cursorToSeq(c string) int64 {
	n, _ := strconv.ParseInt(c, 10, 64)
	return n
}

func seqToCursor(seq int64) string { return strconv.FormatInt(seq, 10) }
