package service

import (
	"encoding/json"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// legacyCols lists what an app of release 0.4.x can store: the synced
// tables and their columns as of tag v0.4.1 (app/src/lib/local/schema.ts and
// the server's column whitelist at that tag). Such an app writes every key
// of a pulled payload into a column of that name. One key it has no column
// for makes the statement fail, and because it retries the same page for
// ever, its sync stops for good.
//
// This list describes software that is already installed. It must never
// grow: columns and entities added later reach only clients that announce a
// protocol version. TestLegacyPullStaysInsideTheFrozenSet and the app's
// legacy.test.ts fail when anything else gets through.
var legacyCols = map[string][]string{
	"apiary": {"organization_id", "name", "address", "lat", "lng", "note", "created_at", "updated_at", "deleted"},
	"hive": {"organization_id", "apiary_id", "name", "type", "status", "boxes", "colony_origin", "note", "qr_code",
		"photo", "created_at", "updated_at", "deleted"},
	"queen": {"organization_id", "hive_id", "year", "marking", "origin", "breeder_number", "introduced_at",
		"replaced_at", "active", "note", "created_at", "updated_at", "deleted"},
	"inspection": {"organization_id", "hive_id", "date", "weather", "queen_seen", "eggs_seen", "temperament",
		"frames", "stores", "queen_cells", "varroa", "honey_kg", "note", "brood_frames", "calmness", "fed_kg",
		"frames_added", "frames_removed", "drone_frame_cut", "super_added", "weight_kg", "youngest_larva",
		"covered_larva", "temp_hive", "temp_outside", "humidity_hive", "humidity_outside", "photo_keys",
		"created_at", "deleted"},
	"task": {"organization_id", "title", "hive_id", "apiary_id", "due_at", "done", "priority", "note",
		"recurrence", "assigned_to", "created_at", "deleted"},
	"placement": {"organization_id", "hive_id", "apiary_id", "start_at", "end_at", "deleted"},
	"harvest": {"organization_id", "apiary_id", "hive_id", "queen_id", "date", "variety", "amount_kg",
		"water_content", "batch_number", "best_before", "note", "deleted"},
	"treatment": {"organization_id", "apiary_id", "hive_id", "queen_id", "date", "product", "active_ingredient",
		"dose", "method", "batch_number", "withdrawal_until", "reason", "note", "deleted"},
	"event": {"organization_id", "scope_id", "type", "date", "apiary_id", "hive_id", "queen_id", "ref_entity",
		"ref_id", "title", "amount_kg", "detail", "author_id", "deleted"},
}

var legacyColSet = func() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(legacyCols))
	for entity, cols := range legacyCols {
		set := make(map[string]bool, len(cols))
		for _, c := range cols {
			set[c] = true
		}
		out[entity] = set
	}
	return out
}()

// forLegacy reduces a logged change to what a 0.4.x app can store. It
// reports false when nothing of the change is left for such an app: an
// entity it has no table for, or an update that touched only newer columns.
func forLegacy(ch *wv1.Change) bool {
	known, ok := legacyColSet[ch.Entity]
	if !ok {
		return false
	}
	if ch.Op == wv1.ChangeOp_CHANGE_OP_DELETE {
		return true // such an app ignores the payload of a delete
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ch.PayloadJson), &fields); err != nil || fields == nil {
		return false
	}
	dropped := false
	for key := range fields {
		if !known[key] {
			delete(fields, key)
			dropped = true
		}
	}
	if len(fields) == 0 {
		return false
	}
	if dropped {
		payload, err := json.Marshal(fields)
		if err != nil {
			return false
		}
		ch.PayloadJson = string(payload)
	}
	return true
}
