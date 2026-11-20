package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Payload validation for incoming changes. A payload is turned into its
// "wire form" first: only columns the entity has, every value a scalar of
// the column's kind, written the way devices store it (booleans as 0/1,
// timestamps as the strings they sent). That form is merged, appended to the
// change log and replayed to every other device, so nothing a client invents
// (unknown keys, nested values, megabyte strings) ever reaches another
// device's local database. dbValues then adapts the wire form to the column
// types of the server's own dialect.

// Rejection codes returned per change in PushResponse.rejected.
const (
	rejWrongTenant    = "wrong_tenant"
	rejForbiddenScope = "forbidden_scope"
	rejUnknownEntity  = "unknown_entity"
	rejInvalid        = "invalid"
	rejClockAhead     = "clock_ahead"
)

const (
	// One change may carry a few inline thumbnails, not an archive.
	maxPayloadBytes = 4 << 20
	maxValueBytes   = 2 << 20
	// Ids are primary keys; MySQL-family tables hold them in VARCHAR(255).
	maxIDLen = 255
)

// changeError is a permanent problem with one change: sending the same
// change again cannot succeed. Push reports it for that change and carries
// on with the rest of the batch.
type changeError struct {
	code   string
	reason string
}

func (e *changeError) Error() string { return e.code + ": " + e.reason }

func invalidChange(format string, args ...any) *changeError {
	return &changeError{code: rejInvalid, reason: fmt.Sprintf(format, args...)}
}

type colKind int

const (
	kindString colKind = iota
	kindBool
	kindInt
	kindFloat
	kindTime
)

// floatCols are the REAL columns per entity. Together with the bools, ints
// and times of entitySpec they give every whitelisted column a kind; what
// is in none of the lists is text.
var floatCols = map[string][]string{
	"apiary":     {"lat", "lng"},
	"inspection": {"honey_kg", "fed_kg", "weight_kg", "temp_hive", "temp_outside", "humidity_hive", "humidity_outside"},
	"harvest":    {"amount_kg", "water_content"},
	"event":      {"amount_kg"},
}

var (
	colKindsOnce sync.Once
	colKinds     map[string]map[string]colKind
)

// kindsOf returns column -> kind for the scalar columns of an entity.
func kindsOf(entity string) map[string]colKind {
	colKindsOnce.Do(func() {
		colKinds = make(map[string]map[string]colKind, len(entityCols))
		for name, spec := range entityCols {
			kinds := make(map[string]colKind, len(spec.cols))
			for _, c := range spec.cols {
				kinds[c] = kindString
			}
			for kind, cols := range map[colKind][]string{
				kindBool: spec.bools, kindInt: spec.ints, kindTime: spec.times, kindFloat: floatCols[name],
			} {
				for _, c := range cols {
					kinds[c] = kind
				}
			}
			colKinds[name] = kinds
		}
	})
	return colKinds[entity]
}

// checkChange validates everything about a change that needs no database:
// entity, id, operation, stamp format and how far the stamp is ahead of now.
func checkChange(ch *wv1.Change, now time.Time) (entitySpec, *changeError) {
	spec, ok := entityCols[ch.Entity]
	if !ok {
		return spec, &changeError{code: rejUnknownEntity, reason: fmt.Sprintf("unknown entity %q", clip(ch.Entity))}
	}
	if ch.EntityId == "" || len(ch.EntityId) > maxIDLen {
		return spec, invalidChange("entity id must have 1 to %d characters", maxIDLen)
	}
	if ch.Op != wv1.ChangeOp_CHANGE_OP_UPSERT && ch.Op != wv1.ChangeOp_CHANGE_OP_DELETE {
		return spec, invalidChange("unknown operation %d", int32(ch.Op))
	}
	ahead, ok := wsync.AheadBy(ch.Hlc, now)
	if !ok {
		return spec, invalidChange("malformed hlc stamp %q", clip(ch.Hlc))
	}
	if ahead > wsync.MaxDrift {
		return spec, &changeError{code: rejClockAhead, reason: fmt.Sprintf(
			"stamp is %s ahead of the server clock (limit %s); server time is %d ms",
			ahead.Round(time.Second), wsync.MaxDrift, now.UnixMilli())}
	}
	return spec, nil
}

// clip keeps client-controlled text short when it is echoed in a reason.
func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// wireFields decodes a change's payload into its wire form (see the top of
// this file). Unknown keys are dropped silently, so an app that knows more
// columns than this server keeps syncing; malformed, nested or oversized
// values make the whole change invalid.
func wireFields(entity string, spec entitySpec, ch *wv1.Change) (map[string]any, *changeError) {
	if ch.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
		return wsync.Tombstone(), nil
	}
	if len(ch.PayloadJson) > maxPayloadBytes {
		return nil, invalidChange("payload has %d bytes, limit is %d", len(ch.PayloadJson), maxPayloadBytes)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(ch.PayloadJson), &raw); err != nil || raw == nil {
		return nil, invalidChange("payload must be a JSON object")
	}
	kinds := kindsOf(entity)
	out := make(map[string]any, len(raw))
	for key, v := range raw {
		if key == "id" || key == "field_hlc" {
			continue
		}
		if isSetCol(spec, key) {
			d, cerr := wireSetDelta(key, v)
			if cerr != nil {
				return nil, cerr
			}
			out[key] = d
			continue
		}
		kind, known := kinds[key]
		if !known {
			continue
		}
		val, cerr := wireScalar(key, kind, v)
		if cerr != nil {
			return nil, cerr
		}
		out[key] = val
	}
	return out, nil
}

func wireSetDelta(col string, v any) (wsync.SetDelta, *changeError) {
	d, err := wsync.ParseSetDelta(v)
	if err != nil {
		return d, invalidChange("%s: %v", col, err)
	}
	for _, list := range [][]string{d.Add, d.Remove} {
		for _, e := range list {
			if len(e) > maxValueBytes {
				return d, invalidChange("%s: element has %d bytes, limit is %d", col, len(e), maxValueBytes)
			}
			if cerr := checkSetElement(col, e); cerr != nil {
				return d, cerr
			}
		}
	}
	now := time.Now()
	for elem, tags := range d.RemovedTags {
		if cerr := checkSetElement(col, elem); cerr != nil {
			return d, cerr
		}
		for _, tag := range tags {
			// A removed tag names an add this or another device made, and
			// every add was checked against the same limit when it arrived.
			if ahead, ok := wsync.AheadBy(tag, now); !ok || ahead > wsync.MaxDrift {
				return d, invalidChange("%s: removed_tags must hold hlc stamps that are not ahead of the server clock", col)
			}
		}
	}
	return d, nil
}

// checkSetElement applies what a set column may hold. The only set column,
// inspection.photo_keys, is the inline photo list of 0.4.x apps: data URLs
// and nothing else. Anything else has no use, and an element named like a
// built-in property of a JavaScript object ("constructor", "__proto__")
// breaks the merge of apps that keep their sets in plain objects.
func checkSetElement(col, elem string) *changeError {
	if col == "photo_keys" && !isDataURL(elem) {
		return invalidChange("%s: elements must be data URLs", col)
	}
	return nil
}

// wireScalar checks one value against its column kind and returns it in
// wire form. JSON decoding yields nil, bool, float64, string, or a nested
// value; the last is never a column value.
func wireScalar(col string, kind colKind, v any) (any, *changeError) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if len(t) > maxValueBytes {
			return nil, invalidChange("%s: value has %d bytes, limit is %d", col, len(t), maxValueBytes)
		}
		return wireFromString(col, kind, t)
	case bool:
		return wireFromNumber(col, kind, boolNum(t))
	case float64:
		return wireFromNumber(col, kind, t)
	default:
		return nil, invalidChange("%s: value must be a string, number, boolean or null", col)
	}
}

func boolNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// wireFromNumber: SQLite on the device stores a number in a TEXT column as
// its text, so the same happens here; timestamps are always strings.
func wireFromNumber(col string, kind colKind, n float64) (any, *changeError) {
	switch kind {
	case kindBool:
		return int64(boolNum(n != 0)), nil
	case kindInt:
		if math.Abs(n) > 1<<53 {
			return nil, invalidChange("%s: integer out of range", col)
		}
		return int64(n), nil
	case kindFloat:
		return n, nil
	case kindTime:
		return nil, invalidChange("%s: timestamp must be a string", col)
	default:
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	}
}

// wireFromString: numeric columns accept numbers written as text ("" means
// not recorded), boolean columns the usual spellings.
func wireFromString(col string, kind colKind, s string) (any, *changeError) {
	switch kind {
	case kindBool:
		switch strings.ToLower(s) {
		case "1", "true":
			return int64(1), nil
		case "0", "false", "":
			return int64(0), nil
		}
		return nil, invalidChange("%s: not a boolean", col)
	case kindInt, kindFloat:
		if s == "" {
			return nil, nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, invalidChange("%s: not a number", col)
		}
		return wireFromNumber(col, kind, n)
	default:
		return s, nil
	}
}

// dbValues adapts scalar wire values to what the target dialect's column
// types accept. SQLite stores whatever it is given, but Postgres rejects
// integers in BOOLEAN columns and MySQL cannot parse RFC3339 into DATETIME,
// so: 0/1 -> bool, "" -> NULL for timestamps, and (outside SQLite) RFC3339
// strings -> time.Time. Set columns are not part of the result.
func dbValues(entity string, wire map[string]any, driver string) map[string]any {
	kinds := kindsOf(entity)
	out := make(map[string]any, len(wire))
	for col, v := range wire {
		kind, scalar := kinds[col]
		if !scalar {
			continue
		}
		out[col] = dbValue(kind, v, driver)
	}
	return out
}

func dbValue(kind colKind, v any, driver string) any {
	switch kind {
	case kindBool:
		if n, ok := v.(int64); ok {
			return n != 0
		}
	case kindTime:
		s, ok := v.(string)
		if !ok {
			return v
		}
		if s == "" {
			return nil // nullable timestamp: "" means "not set"
		}
		if driver == "sqlite" {
			return s // stored verbatim, as the devices themselves do
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
		// Other layouts (e.g. date-only) stay strings; Postgres and MySQL
		// parse those natively.
	}
	return v
}

// asChangeError maps an error from applying one change to a rejection, or
// returns nil when the error says nothing about the change itself (lost
// connection, deadlock, cancelled request) and the whole Push must fail so
// the device retries.
func asChangeError(err error, isDataError func(error) bool) *changeError {
	var cerr *changeError
	switch {
	case errors.As(err, &cerr):
		return cerr
	case errors.Is(err, errWrongTenant):
		return &changeError{code: rejWrongTenant, reason: errWrongTenant.Error()}
	case isDataError(err):
		return invalidChange("the database refused a value of this change")
	}
	return nil
}
