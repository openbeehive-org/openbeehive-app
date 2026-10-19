package service

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// Row readers for the CRUD services. Rows come back as maps because the
// three dialects return different Go types for the same column (SQLite
// keeps the strings devices wrote, Postgres and MySQL return time.Time,
// bool and []byte); the rX helpers normalise them.

type queryer interface {
	QueryxContext(ctx context.Context, query string, args ...any) (*sqlx.Rows, error)
	Rebind(string) string
}

func queryMaps(ctx context.Context, q queryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := q.QueryxContext(ctx, q.Rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		m := map[string]any{}
		if err := rows.MapScan(m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func countRows(ctx context.Context, q queryer, query string, args ...any) (int, error) {
	rows, err := queryMaps(ctx, q, query, args...)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	for _, v := range rows[0] {
		return int(ri(v)), nil
	}
	return 0, nil
}

func rs(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return jsTime(t)
	case nil:
		return ""
	default:
		return ""
	}
}

func ri(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int32:
		return int64(t)
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(string(t), 10, 64)
		return n
	}
	return 0
}

func rf(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int32:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case []byte:
		f, _ := strconv.ParseFloat(string(t), 64)
		return f
	}
	return 0
}

func rb(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case int32:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t == "1" || t == "true"
	case []byte:
		return string(t) == "1" || string(t) == "true"
	}
	return false
}

var timeLayouts = []string{
	time.RFC3339Nano, time.RFC3339,
	"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02",
}

// parseTime accepts every form the app and the three dialects produce.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func rtime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		return parseTime(t)
	case []byte:
		return parseTime(string(t))
	}
	return time.Time{}, false
}

func rt(v any) *timestamppb.Timestamp {
	if t, ok := rtime(v); ok {
		return timestamppb.New(t)
	}
	return nil
}

// rset resolves an OR-Set column to its visible values.
func rset(v any) []string {
	return wsync.ParseORSet(rs(v)).Values()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// i32 narrows to int32, saturating instead of wrapping.
func i32(n int64) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	}
	return int32(n)
}

// --- row -> proto ---

func queenFromRow(r map[string]any) *wv1.Queen {
	return &wv1.Queen{
		Id: rs(r["id"]), OrganizationId: rs(r["organization_id"]), HiveId: rs(r["hive_id"]),
		Year: i32(ri(r["year"])), Marking: wv1.MarkingColor(i32(ri(r["marking"]))),
		Origin: rs(r["origin"]), BreederNumber: rs(r["breeder_number"]),
		IntroducedAt: rt(r["introduced_at"]), ReplacedAt: rt(r["replaced_at"]),
		Active: rb(r["active"]), Note: rs(r["note"]),
		CreatedAt: rt(r["created_at"]), UpdatedAt: rt(r["updated_at"]),
	}
}

func inspectionFromRow(r map[string]any) *wv1.Inspection {
	return &wv1.Inspection{
		Id: rs(r["id"]), OrganizationId: rs(r["organization_id"]), HiveId: rs(r["hive_id"]),
		Date: rt(r["date"]), Weather: rs(r["weather"]),
		QueenSeen: rb(r["queen_seen"]), EggsSeen: rb(r["eggs_seen"]),
		Temperament: wv1.Temperament(i32(ri(r["temperament"]))), Frames: i32(ri(r["frames"])),
		Stores: wv1.Stores(i32(ri(r["stores"]))), QueenCells: i32(ri(r["queen_cells"])),
		Varroa: rs(r["varroa"]), HoneyKg: rf(r["honey_kg"]), Note: rs(r["note"]),
		PhotoKeys: rset(r["photo_keys"]), CreatedAt: rt(r["created_at"]),
		BroodFrames: i32(ri(r["brood_frames"])), Calmness: i32(ri(r["calmness"])),
		FedKg: rf(r["fed_kg"]), FramesAdded: i32(ri(r["frames_added"])),
		FramesRemoved: i32(ri(r["frames_removed"])), DroneFrameCut: rb(r["drone_frame_cut"]),
		SuperAdded: rb(r["super_added"]), WeightKg: rf(r["weight_kg"]),
		YoungestLarva: i32(ri(r["youngest_larva"])), CoveredLarva: rb(r["covered_larva"]),
		TempHive: rf(r["temp_hive"]), TempOutside: rf(r["temp_outside"]),
		HumidityHive: rf(r["humidity_hive"]), HumidityOutside: rf(r["humidity_outside"]),
	}
}

func taskFromRow(r map[string]any) *wv1.Task {
	return &wv1.Task{
		Id: rs(r["id"]), OrganizationId: rs(r["organization_id"]), Title: rs(r["title"]),
		HiveId: rs(r["hive_id"]), ApiaryId: rs(r["apiary_id"]), DueAt: rt(r["due_at"]),
		Done: rb(r["done"]), Priority: wv1.TaskPriority(i32(ri(r["priority"]))), Note: rs(r["note"]),
		Recurrence: rs(r["recurrence"]), AssignedTo: rs(r["assigned_to"]), CreatedAt: rt(r["created_at"]),
	}
}

func treatmentFromRow(r map[string]any) *wv1.Treatment {
	return &wv1.Treatment{
		Id: rs(r["id"]), OrganizationId: rs(r["organization_id"]), ApiaryId: rs(r["apiary_id"]),
		HiveId: rs(r["hive_id"]), QueenId: rs(r["queen_id"]), Date: rt(r["date"]),
		Product: rs(r["product"]), ActiveIngredient: rs(r["active_ingredient"]), Dose: rs(r["dose"]),
		Method: rs(r["method"]), BatchNumber: rs(r["batch_number"]), WithdrawalUntil: rt(r["withdrawal_until"]),
		Reason: rs(r["reason"]), Note: rs(r["note"]),
	}
}

func eventFromRow(r map[string]any) *wv1.Event {
	return &wv1.Event{
		Id: rs(r["id"]), OrganizationId: rs(r["organization_id"]), ScopeId: rs(r["scope_id"]),
		Type: wv1.EventType(i32(ri(r["type"]))), Date: rt(r["date"]),
		ApiaryId: rs(r["apiary_id"]), HiveId: rs(r["hive_id"]), QueenId: rs(r["queen_id"]),
		RefEntity: rs(r["ref_entity"]), RefId: rs(r["ref_id"]), Title: rs(r["title"]),
		AmountKg: rf(r["amount_kg"]), DetailJson: rs(r["detail"]), AuthorId: rs(r["author_id"]),
	}
}

func harvestFromRow(r map[string]any) *wv1.Harvest {
	return &wv1.Harvest{
		Id: rs(r["id"]), OrganizationId: rs(r["organization_id"]), ApiaryId: rs(r["apiary_id"]),
		HiveId: rs(r["hive_id"]), QueenId: rs(r["queen_id"]), Date: rt(r["date"]),
		Variety: rs(r["variety"]), AmountKg: rf(r["amount_kg"]), WaterContent: rf(r["water_content"]),
		BatchNumber: rs(r["batch_number"]), BestBefore: rt(r["best_before"]), Note: rs(r["note"]),
	}
}
