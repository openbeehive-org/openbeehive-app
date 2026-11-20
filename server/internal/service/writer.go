package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Writer is the write path shared by every CRUD service. Each write is
// applied through the same per-field merge as SyncService.Push and appended
// to the change log, so rows created through the API reach devices on their
// next Pull, and rows devices push later merge against them field by field.
//
// The flows below (hive + placement + event, queen replacement, frozen
// context for inspections and treatments) mirror app/src/lib/local/history.ts
// so the history a device sees is the same whether a record was entered in
// the app or through the API.
type Writer struct {
	db  *sqlx.DB
	hlc *wsync.HLC
}

func NewWriter(db *sqlx.DB, hlc *wsync.HLC) *Writer { return &Writer{db: db, hlc: hlc} }

// Event types, mirroring history.ts.
const (
	evCreated         = 1
	evQueenIntroduced = 2
	evQueenReplaced   = 3
	evMoved           = 4
	evInspection      = 5
	evTreatment       = 6
	evHarvest         = 7
)

// txWriter is one transaction on behalf of one identity.
type txWriter struct {
	ctx context.Context
	tx  *sqlx.Tx
	w   *Writer
	id  auth.Identity
	// stableIDs gives the rows a flow writes next to its main record
	// (placements, events) ids derived from that record's id instead of
	// random ones. Seeding sets it so that seeding again rewrites the same
	// rows instead of adding copies.
	stableIDs bool
}

// sideID is the id of a row written alongside the record parent.
func (t *txWriter) sideID(parent, role string) string {
	if t.stableIDs {
		return parent + ":" + role
	}
	return uuid.NewString()
}

// orNew returns id, or a fresh random id when the caller did not choose one.
func orNew(id string) string {
	if id == "" {
		return uuid.NewString()
	}
	return id
}

// Run executes fn in one transaction and commits if it returns nil.
func (w *Writer) Run(ctx context.Context, id auth.Identity, fn func(t *txWriter) error) error {
	tx, err := w.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Same lock, same order as SyncService.Push: API writes and device
	// pushes merge the same rows and must not interleave.
	if err := lockWrites(ctx, tx); err != nil {
		return err
	}
	if err := fn(&txWriter{ctx: ctx, tx: tx, w: w, id: id}); err != nil {
		return err
	}
	return tx.Commit()
}

// upsert writes the given fields of one row (partial delta, per-field LWW).
func (t *txWriter) upsert(entity, id, scope string, fields map[string]any) error {
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return t.apply(&wv1.Change{
		Entity: entity, EntityId: id, ScopeId: scope,
		Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: string(payload),
	})
}

// del soft-deletes one row (deleted = true), like the app does.
func (t *txWriter) del(entity, id, scope string) error {
	return t.apply(&wv1.Change{
		Entity: entity, EntityId: id, ScopeId: scope,
		Op: wv1.ChangeOp_CHANGE_OP_DELETE, PayloadJson: `{"deleted":1}`,
	})
}

func (t *txWriter) apply(ch *wv1.Change) error {
	spec, ok := entityCols[ch.Entity]
	if !ok {
		return errors.New("unknown entity " + ch.Entity)
	}
	ch.Hlc = t.w.hlc.Now()
	ch.AuthorId = t.id.UserID
	if err := applyChange(t.ctx, t.tx, spec, ch, t.id.OrgID); err != nil {
		return err
	}
	return appendChangeLog(t.ctx, t.tx, ch, t.id.OrgID)
}

// emit records a history event with frozen context under the given id.
func (t *txWriter) emit(id string, typ int, scope string, e eventInput) error {
	detail := e.detail
	if detail == nil {
		detail = map[string]any{}
	}
	dj, _ := json.Marshal(detail)
	return t.upsert("event", id, scope, map[string]any{
		"organization_id": t.id.OrgID, "scope_id": scope, "type": typ, "date": e.date,
		"apiary_id": e.apiaryID, "hive_id": e.hiveID, "queen_id": e.queenID,
		"ref_entity": e.refEntity, "ref_id": e.refID, "title": e.title,
		"amount_kg": e.amountKg, "detail": string(dj), "author_id": t.id.UserID,
		"deleted": 0,
	})
}

type eventInput struct {
	date                      string
	apiaryID, hiveID, queenID string
	refEntity, refID, title   string
	amountKg                  float64
	detail                    any
}

// --- lookups ---

// hive returns the caller's non-deleted hive or storage.ErrNotFound.
func (t *txWriter) hive(hiveID string) (map[string]any, error) {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT id, apiary_id, name FROM hive
		WHERE id = ? AND organization_id = ? AND deleted = FALSE`, hiveID, t.id.OrgID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, storage.ErrNotFound
	}
	return rows[0], nil
}

// apiaryExists reports whether the caller owns a non-deleted apiary with this id.
func (t *txWriter) apiaryExists(apiaryID string) (bool, error) {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT id FROM apiary
		WHERE id = ? AND organization_id = ? AND deleted = FALSE`, apiaryID, t.id.OrgID)
	return len(rows) > 0, err
}

// row loads one non-deleted row of the caller's tenant.
func (t *txWriter) row(entity, id string) (map[string]any, error) {
	rows, err := queryMaps(t.ctx, t.tx, "SELECT * FROM "+entityCols[entity].table+
		" WHERE id = ? AND organization_id = ? AND deleted = FALSE", id, t.id.OrgID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, storage.ErrNotFound
	}
	return rows[0], nil
}

// tsArg turns a JS-style ISO timestamp into a comparable query argument:
// SQLite compares the stored strings, Postgres and MySQL need time.Time.
func (t *txWriter) tsArg(iso string) any {
	if t.tx.DriverName() == "sqlite" {
		return iso
	}
	if v, err := time.Parse(time.RFC3339Nano, iso); err == nil {
		return v
	}
	return iso
}

// latestFirst orders candidate rows so that "the current one" is a
// deterministic pick: the latest value of col, ties broken by the higher id.
// Two devices that moved or requeened a hive while offline leave two open
// placements or two active queens; every reader must then agree on which one
// counts instead of trusting "end_at IS NULL" or the active flag to be unique.
func latestFirst(col string) string {
	return " ORDER BY CASE WHEN " + col + " IS NULL THEN 1 ELSE 0 END, " + col + " DESC, id DESC"
}

// resolveContext mirrors history.ts: where the hive lived and which queen
// reigned at the (possibly back-dated) point in time. Every lookup is bound
// to the caller's tenant: hive ids are printed on QR labels, so another
// tenant can store placements or queens that name this hive.
func (t *txWriter) resolveContext(hiveID, date string) (apiaryID, queenID string, err error) {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT apiary_id FROM placement
		WHERE hive_id = ? AND organization_id = ? AND deleted = FALSE AND start_at <= ?
		  AND (end_at IS NULL OR end_at > ?)`+latestFirst("start_at")+` LIMIT 1`,
		hiveID, t.id.OrgID, t.tsArg(date), t.tsArg(date))
	if err != nil {
		return "", "", err
	}
	if len(rows) > 0 {
		apiaryID = rs(rows[0]["apiary_id"])
	}
	if apiaryID == "" {
		h, err := t.hive(hiveID)
		if err != nil {
			return "", "", err
		}
		apiaryID = rs(h["apiary_id"])
	}

	rows, err = queryMaps(t.ctx, t.tx, `SELECT id FROM queen
		WHERE hive_id = ? AND organization_id = ? AND deleted = FALSE AND introduced_at <= ?
		  AND (replaced_at IS NULL OR replaced_at > ?)`+latestFirst("introduced_at")+` LIMIT 1`,
		hiveID, t.id.OrgID, t.tsArg(date), t.tsArg(date))
	if err != nil {
		return "", "", err
	}
	if len(rows) > 0 {
		return apiaryID, rs(rows[0]["id"]), nil
	}
	active, err := t.activeQueens(hiveID)
	if err != nil {
		return "", "", err
	}
	if len(active) > 0 {
		queenID = active[0]
	}
	return apiaryID, queenID, nil
}

// activeQueens returns the ids of the hive's queens flagged active, the
// current one first.
func (t *txWriter) activeQueens(hiveID string) ([]string, error) {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT id FROM queen
		WHERE hive_id = ? AND organization_id = ? AND active = TRUE AND deleted = FALSE`+
		latestFirst("introduced_at"), hiveID, t.id.OrgID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, rs(r["id"]))
	}
	return ids, nil
}

// refEvents returns the live timeline events that point at a record.
func (t *txWriter) refEvents(entity, id string) ([]map[string]any, error) {
	return queryMaps(t.ctx, t.tx, `SELECT id, scope_id FROM event
		WHERE organization_id = ? AND ref_entity = ? AND ref_id = ? AND deleted = FALSE`,
		t.id.OrgID, entity, id)
}

// recordScope is the sync scope a dated record lives in: its frozen apiary,
// or for inspections (which carry none) the hive's apiary at the visit date.
func (t *txWriter) recordScope(entity string, cur map[string]any) (string, error) {
	if entity != "inspection" {
		if a := rs(cur["apiary_id"]); a != "" {
			return a, nil
		}
	}
	scope, _, err := t.resolveContext(rs(cur["hive_id"]), rs(cur["date"]))
	return scope, err
}

// --- write flows (ports of history.ts) ---

// createApiary: a new apiary opens its own sync scope (scope id = apiary id),
// as in the app.
func (t *txWriter) createApiary(id, name, address string, lat, lng float64, note, ts string) error {
	return t.upsert("apiary", id, id, map[string]any{
		"organization_id": t.id.OrgID, "name": name, "address": address,
		"lat": lat, "lng": lng, "note": note,
		"created_at": ts, "updated_at": ts, "deleted": 0,
	})
}

// hiveInput describes a new hive; id "" picks a random one.
type hiveInput struct {
	id, apiaryID, name string
	typ, boxes         int32
	colonyOrigin, note string
	at                 string // creation time
}

// createHive: hive row + open placement interval + CREATED event.
func (t *txWriter) createHive(h hiveInput) (string, error) {
	id := orNew(h.id)
	if err := t.upsert("hive", id, h.apiaryID, map[string]any{
		"organization_id": t.id.OrgID, "apiary_id": h.apiaryID, "name": h.name,
		"type": h.typ, "status": int32(wv1.HiveStatus_HIVE_STATUS_ACTIVE), "boxes": h.boxes,
		"colony_origin": h.colonyOrigin, "note": h.note,
		"created_at": h.at, "updated_at": h.at, "deleted": 0,
	}); err != nil {
		return "", err
	}
	if err := t.upsert("placement", t.sideID(id, "placement"), h.apiaryID, map[string]any{
		"organization_id": t.id.OrgID, "hive_id": id, "apiary_id": h.apiaryID,
		"start_at": h.at, "end_at": nil, "deleted": 0,
	}); err != nil {
		return "", err
	}
	err := t.emit(t.sideID(id, "created"), evCreated, h.apiaryID,
		eventInput{date: h.at, apiaryID: h.apiaryID, hiveID: id, title: h.name})
	return id, err
}

// moveHive: close the hive's open placements, open a new one, move the hive,
// MOVED event. Closing every open interval (normally one) also repairs the
// duplicates concurrent offline moves leave behind.
func (t *txWriter) moveHive(hiveID, from, to, ts string) error {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT id, apiary_id FROM placement
		WHERE hive_id = ? AND organization_id = ? AND end_at IS NULL AND deleted = FALSE`,
		hiveID, t.id.OrgID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		scope := rs(r["apiary_id"])
		if scope == "" {
			scope = from
		}
		if err := t.upsert("placement", rs(r["id"]), scope, map[string]any{"end_at": ts}); err != nil {
			return err
		}
	}
	if err := t.upsert("placement", uuid.NewString(), to, map[string]any{
		"organization_id": t.id.OrgID, "hive_id": hiveID, "apiary_id": to,
		"start_at": ts, "end_at": nil, "deleted": 0,
	}); err != nil {
		return err
	}
	if err := t.upsert("hive", hiveID, to, map[string]any{"apiary_id": to, "updated_at": ts}); err != nil {
		return err
	}
	return t.emit(uuid.NewString(), evMoved, to, eventInput{date: ts, apiaryID: to, hiveID: hiveID,
		detail: map[string]string{"from": from, "to": to}})
}

// queenInput describes a new queen; id "" picks a random one, marking 0
// derives the colour from the year.
type queenInput struct {
	id, hiveID, scope     string
	year, marking         int32
	origin, breeder, note string
	at                    string // introduction time
}

// setQueen: end the reign of every queen still flagged active (normally
// one), introduce the new one, and record the events.
func (t *txWriter) setQueen(q queenInput) (string, error) {
	if q.marking == 0 {
		q.marking = markingForYear(q.year)
	}
	active, err := t.activeQueens(q.hiveID)
	if err != nil {
		return "", err
	}
	for _, old := range active {
		if err := t.upsert("queen", old, q.scope, map[string]any{"active": 0, "replaced_at": q.at}); err != nil {
			return "", err
		}
		if err := t.emit(t.sideID(old, "replaced"), evQueenReplaced, q.scope,
			eventInput{date: q.at, hiveID: q.hiveID, queenID: old}); err != nil {
			return "", err
		}
	}
	id := orNew(q.id)
	if err := t.upsert("queen", id, q.scope, map[string]any{
		"organization_id": t.id.OrgID, "hive_id": q.hiveID, "year": q.year, "marking": q.marking,
		"origin": q.origin, "breeder_number": q.breeder, "note": q.note,
		"introduced_at": q.at, "replaced_at": nil, "active": 1,
		"created_at": q.at, "updated_at": q.at, "deleted": 0,
	}); err != nil {
		return "", err
	}
	err = t.emit(t.sideID(id, "introduced"), evQueenIntroduced, q.scope,
		eventInput{date: q.at, hiveID: q.hiveID, queenID: id, title: "Queen " + itoa(int64(q.year))})
	return id, err
}

// recordInspection: inspection row with frozen context + INSPECTION event.
func (t *txWriter) recordInspection(id, hiveID, date string, fields map[string]any) (string, error) {
	apiaryID, queenID, err := t.resolveContext(hiveID, date)
	if err != nil {
		return "", err
	}
	id = orNew(id)
	fields["organization_id"] = t.id.OrgID
	fields["hive_id"] = hiveID
	fields["date"] = date
	if _, ok := fields["created_at"]; !ok {
		fields["created_at"] = jsTime(time.Now())
	}
	fields["deleted"] = 0
	if err := t.upsert("inspection", id, apiaryID, fields); err != nil {
		return "", err
	}
	err = t.emit(t.sideID(id, "event"), evInspection, apiaryID, eventInput{date: date, apiaryID: apiaryID,
		hiveID: hiveID, queenID: queenID, refEntity: "inspection", refID: id, title: "Inspection"})
	return id, err
}

// harvestTitle is the timeline title of a harvest, as the app writes it.
func harvestTitle(kg float64, variety string) string {
	if variety == "" {
		variety = "Honey"
	}
	return strconv.FormatFloat(kg, 'f', -1, 64) + " kg " + variety
}

// recordHarvest: harvest row with frozen context + HARVEST fact event (the
// row the honey statistics read).
func (t *txWriter) recordHarvest(id, hiveID, date string, fields map[string]any) (string, error) {
	apiaryID, queenID, err := t.resolveContext(hiveID, date)
	if err != nil {
		return "", err
	}
	id = orNew(id)
	fields["organization_id"] = t.id.OrgID
	fields["apiary_id"] = apiaryID
	fields["hive_id"] = hiveID
	fields["queen_id"] = queenID
	fields["date"] = date
	fields["deleted"] = 0
	if err := t.upsert("harvest", id, apiaryID, fields); err != nil {
		return "", err
	}
	kg := rf(fields["amount_kg"])
	err = t.emit(t.sideID(id, "event"), evHarvest, apiaryID, eventInput{date: date, apiaryID: apiaryID,
		hiveID: hiveID, queenID: queenID, refEntity: "harvest", refID: id, amountKg: kg,
		title: harvestTitle(kg, rs(fields["variety"]))})
	return id, err
}

// recordTreatment: treatment row with frozen context + TREATMENT event.
func (t *txWriter) recordTreatment(id, hiveID, date string, fields map[string]any) (string, error) {
	apiaryID, queenID, err := t.resolveContext(hiveID, date)
	if err != nil {
		return "", err
	}
	id = orNew(id)
	fields["organization_id"] = t.id.OrgID
	fields["apiary_id"] = apiaryID
	fields["hive_id"] = hiveID
	fields["queen_id"] = queenID
	fields["date"] = date
	fields["deleted"] = 0
	if err := t.upsert("treatment", id, apiaryID, fields); err != nil {
		return "", err
	}
	err = t.emit(t.sideID(id, "event"), evTreatment, apiaryID, eventInput{date: date, apiaryID: apiaryID,
		hiveID: hiveID, queenID: queenID, refEntity: "treatment", refID: id, title: rs(fields["product"])})
	return id, err
}

// delRecord tombstones a record together with the timeline events that
// point at it, so it also leaves the history and the honey statistics.
func (t *txWriter) delRecord(entity, id, scope string) error {
	evs, err := t.refEvents(entity, id)
	if err != nil {
		return err
	}
	if err := t.del(entity, id, scope); err != nil {
		return err
	}
	return t.delEvents(evs, scope)
}

// delEvents tombstones events in their own scope (fallback for rows that
// carry none).
func (t *txWriter) delEvents(evs []map[string]any, fallback string) error {
	for _, e := range evs {
		scope := rs(e["scope_id"])
		if scope == "" {
			scope = fallback
		}
		if err := t.del("event", rs(e["id"]), scope); err != nil {
			return err
		}
	}
	return nil
}

// markingForYear: international queen colour by the year's last digit
// (1/6 white, 2/7 yellow, 3/8 red, 4/9 green, 5/0 blue); mirrors repo.ts.
func markingForYear(year int32) int32 {
	return ((year%10)+4)%5 + 1
}

// --- timestamps ---

// jsTime formats like JavaScript's Date.toISOString(), the form every
// device writes, so string comparison in SQLite orders correctly.
func jsTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// tsOrNow: request timestamp or now.
func tsOrNow(ts *timestamppb.Timestamp) string {
	if ts == nil || !ts.IsValid() {
		return jsTime(time.Now())
	}
	return jsTime(ts.AsTime())
}

// tsOrEmpty: request timestamp or "" (the app's value for "not set").
func tsOrEmpty(ts *timestamppb.Timestamp) string {
	if ts == nil || !ts.IsValid() {
		return ""
	}
	return jsTime(ts.AsTime())
}

// b2i: booleans travel as 0/1 in sync payloads, like the app's SQLite rows.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- error mapping ---

// writeErr maps writer and query errors to Connect codes. Caller mistakes
// keep their precise code and message. Anything else is logged and reported
// as a generic internal error, so database and driver messages (table names,
// constraint names, connection details) never reach API clients.
func writeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return connect.NewError(connect.CodeNotFound, storage.ErrNotFound.Error()).WithCause(err)
	case errors.Is(err, errWrongTenant):
		return connect.NewError(connect.CodePermissionDenied, errWrongTenant.Error()).WithCause(err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	log.Printf("service: internal error: %v", err)
	return connect.NewError(connect.CodeInternal, "internal error").WithCause(err)
}

func invalid(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, msg)
}

// precondition reports a request that is valid but cannot be carried out in
// the current state of the data.
func precondition(msg string) error {
	return connect.NewError(connect.CodeFailedPrecondition, msg)
}
