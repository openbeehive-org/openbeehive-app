package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
}

// Run executes fn in one transaction and commits if it returns nil.
func (w *Writer) Run(ctx context.Context, id auth.Identity, fn func(t *txWriter) error) error {
	tx, err := w.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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

// emit records an immutable history event with frozen context.
func (t *txWriter) emit(typ int, scope string, e eventInput) error {
	id := uuid.NewString()
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

// resolveContext mirrors history.ts: where the hive lived and which queen
// reigned at the (possibly back-dated) point in time.
func (t *txWriter) resolveContext(hiveID, date string) (apiaryID, queenID string, err error) {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT apiary_id FROM placement
		WHERE hive_id = ? AND deleted = FALSE AND start_at <= ?
		  AND (end_at IS NULL OR end_at > ?)
		ORDER BY start_at DESC LIMIT 1`, hiveID, t.tsArg(date), t.tsArg(date))
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
		WHERE hive_id = ? AND deleted = FALSE AND introduced_at <= ?
		  AND (replaced_at IS NULL OR replaced_at > ?)
		ORDER BY introduced_at DESC LIMIT 1`, hiveID, t.tsArg(date), t.tsArg(date))
	if err != nil {
		return "", "", err
	}
	if len(rows) > 0 {
		queenID = rs(rows[0]["id"])
	}
	if queenID == "" {
		rows, err = queryMaps(t.ctx, t.tx, `SELECT id FROM queen
			WHERE hive_id = ? AND active = TRUE AND deleted = FALSE LIMIT 1`, hiveID)
		if err != nil {
			return "", "", err
		}
		if len(rows) > 0 {
			queenID = rs(rows[0]["id"])
		}
	}
	return apiaryID, queenID, nil
}

// --- write flows (ports of history.ts) ---

// createHive: hive row + open placement interval + CREATED event.
func (t *txWriter) createHive(apiaryID, name string, typ, boxes int32, colonyOrigin, note string) (string, error) {
	id := uuid.NewString()
	ts := jsTime(time.Now())
	if err := t.upsert("hive", id, apiaryID, map[string]any{
		"organization_id": t.id.OrgID, "apiary_id": apiaryID, "name": name,
		"type": typ, "status": int32(wv1.HiveStatus_HIVE_STATUS_ACTIVE), "boxes": boxes,
		"colony_origin": colonyOrigin, "note": note,
		"created_at": ts, "updated_at": ts, "deleted": 0,
	}); err != nil {
		return "", err
	}
	if err := t.upsert("placement", uuid.NewString(), apiaryID, map[string]any{
		"organization_id": t.id.OrgID, "hive_id": id, "apiary_id": apiaryID,
		"start_at": ts, "end_at": nil, "deleted": 0,
	}); err != nil {
		return "", err
	}
	err := t.emit(evCreated, apiaryID, eventInput{date: ts, apiaryID: apiaryID, hiveID: id, title: name})
	return id, err
}

// moveHive: close the open placement, open a new one, move the hive, MOVED event.
func (t *txWriter) moveHive(hiveID, from, to, ts string) error {
	rows, err := queryMaps(t.ctx, t.tx, `SELECT id FROM placement
		WHERE hive_id = ? AND end_at IS NULL AND deleted = FALSE
		ORDER BY start_at DESC LIMIT 1`, hiveID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		if err := t.upsert("placement", rs(rows[0]["id"]), from, map[string]any{"end_at": ts}); err != nil {
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
	err = t.emit(evMoved, to, eventInput{date: ts, apiaryID: to, hiveID: hiveID,
		detail: map[string]string{"from": from, "to": to}})
	return err
}

// setQueen: end the current queen's reign, introduce the new one, two events.
func (t *txWriter) setQueen(hiveID, scope string, year, marking int32, origin, breeder, note, ts string) (string, error) {
	if marking == 0 {
		marking = markingForYear(year)
	}
	rows, err := queryMaps(t.ctx, t.tx, `SELECT id FROM queen
		WHERE hive_id = ? AND active = TRUE AND deleted = FALSE LIMIT 1`, hiveID)
	if err != nil {
		return "", err
	}
	if len(rows) > 0 {
		old := rs(rows[0]["id"])
		if err := t.upsert("queen", old, scope, map[string]any{"active": 0, "replaced_at": ts}); err != nil {
			return "", err
		}
		if err := t.emit(evQueenReplaced, scope, eventInput{date: ts, hiveID: hiveID, queenID: old}); err != nil {
			return "", err
		}
	}
	id := uuid.NewString()
	if err := t.upsert("queen", id, scope, map[string]any{
		"organization_id": t.id.OrgID, "hive_id": hiveID, "year": year, "marking": marking,
		"origin": origin, "breeder_number": breeder, "note": note,
		"introduced_at": ts, "replaced_at": nil, "active": 1,
		"created_at": ts, "updated_at": ts, "deleted": 0,
	}); err != nil {
		return "", err
	}
	err = t.emit(evQueenIntroduced, scope, eventInput{date: ts, hiveID: hiveID, queenID: id,
		title: "Queen " + itoa(int64(year))})
	return id, err
}

// recordInspection: inspection row with frozen context + INSPECTION event.
func (t *txWriter) recordInspection(hiveID, date string, fields map[string]any) (string, error) {
	apiaryID, queenID, err := t.resolveContext(hiveID, date)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	fields["organization_id"] = t.id.OrgID
	fields["hive_id"] = hiveID
	fields["date"] = date
	fields["created_at"] = jsTime(time.Now())
	fields["deleted"] = 0
	if err := t.upsert("inspection", id, apiaryID, fields); err != nil {
		return "", err
	}
	err = t.emit(evInspection, apiaryID, eventInput{date: date, apiaryID: apiaryID, hiveID: hiveID,
		queenID: queenID, refEntity: "inspection", refID: id, title: "Inspection"})
	return id, err
}

// recordHarvest: harvest row with frozen context + HARVEST fact event (the
// row the honey statistics read).
func (t *txWriter) recordHarvest(hiveID, date string, fields map[string]any) (string, error) {
	apiaryID, queenID, err := t.resolveContext(hiveID, date)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
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
	variety := rs(fields["variety"])
	if variety == "" {
		variety = "Honey"
	}
	err = t.emit(evHarvest, apiaryID, eventInput{date: date, apiaryID: apiaryID, hiveID: hiveID,
		queenID: queenID, refEntity: "harvest", refID: id, amountKg: kg,
		title: strconv.FormatFloat(kg, 'f', -1, 64) + " kg " + variety})
	return id, err
}

// recordTreatment: treatment row with frozen context + TREATMENT event.
func (t *txWriter) recordTreatment(hiveID, date string, fields map[string]any) (string, error) {
	apiaryID, queenID, err := t.resolveContext(hiveID, date)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	fields["organization_id"] = t.id.OrgID
	fields["apiary_id"] = apiaryID
	fields["hive_id"] = hiveID
	fields["queen_id"] = queenID
	fields["date"] = date
	fields["deleted"] = 0
	if err := t.upsert("treatment", id, apiaryID, fields); err != nil {
		return "", err
	}
	err = t.emit(evTreatment, apiaryID, eventInput{date: date, apiaryID: apiaryID, hiveID: hiveID,
		queenID: queenID, refEntity: "treatment", refID: id, title: rs(fields["product"])})
	return id, err
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

// writeErr maps writer errors to Connect codes.
func writeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	case errors.Is(err, errWrongTenant):
		return connect.NewError(connect.CodePermissionDenied, err.Error()).WithCause(err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
}

func invalid(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, msg)
}
