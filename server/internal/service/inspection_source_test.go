package service

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

func TestSensorReadingsAreNotVisits(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	visit, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: "hive-a", Date: timestamppb.New(time.Now().Add(-40 * 24 * time.Hour)), Frames: proto.Int32(9),
	})
	if err != nil {
		t.Fatal(err)
	}
	if visit.Inspection.Source != wv1.InspectionSource_INSPECTION_SOURCE_UNSPECIFIED {
		t.Fatalf("unset source stays unset (treated as manual): %v", visit.Inspection.Source)
	}
	for i := 0; i < 12; i++ {
		r, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
			HiveId: "hive-a", Date: timestamppb.New(time.Now().Add(-time.Duration(i) * time.Hour)),
			WeightKg: proto.Float64(40 + float64(i)), Source: wv1.InspectionSource_INSPECTION_SOURCE_SENSOR,
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Inspection.Source != wv1.InspectionSource_INSPECTION_SOURCE_SENSOR {
			t.Fatalf("source must be stored: %+v", r.Inspection)
		}
	}
	chs := changesFor(f.pullAll(t, ctx), "inspection")
	if last := chs[len(chs)-1]; !strings.Contains(last.PayloadJson, `"source":2`) {
		t.Fatalf("devices must learn the source: %s", last.PayloadJson)
	}

	d, err := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DueInspections) != 1 || d.DueInspections[0].DaysSinceLast < 39 {
		t.Fatalf("readings must not reset the last visit: %+v", d.DueInspections)
	}
	if len(d.RecentInspections) != 1 || d.RecentInspections[0].Id != visit.Inspection.Id {
		t.Fatalf("recent inspections list visits only: %d rows", len(d.RecentInspections))
	}

	count := func(src wv1.InspectionSource) int32 {
		t.Helper()
		r, err := f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{Source: src, Page: &wv1.PageRequest{PageSize: 1}})
		if err != nil {
			t.Fatal(err)
		}
		return r.Page.Total
	}
	if all, manual, sensor := count(0), count(wv1.InspectionSource_INSPECTION_SOURCE_MANUAL), count(wv1.InspectionSource_INSPECTION_SOURCE_SENSOR); all != 13 || manual != 1 || sensor != 12 {
		t.Fatalf("source filter: all %d manual %d sensor %d", all, manual, sensor)
	}
	_, err = f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{HiveId: "hive-a", Source: 9})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestOptionalInspectionNumbersKeepNotRecorded(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: "hive-a", TempOutside: proto.Float64(0), QueenCells: proto.Int32(0), WeightKg: proto.Float64(38.4),
	})
	if err != nil {
		t.Fatal(err)
	}
	ls, err := f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{})
	if err != nil || len(ls.Inspections) != 1 {
		t.Fatalf("list: %v %+v", err, ls)
	}
	for _, i := range []*wv1.Inspection{cr.Inspection, ls.Inspections[0]} {
		if i.TempOutside == nil || i.GetTempOutside() != 0 || i.QueenCells == nil || i.GetQueenCells() != 0 || i.GetWeightKg() != 38.4 {
			t.Fatalf("an observed 0 must come back as 0: %+v", i)
		}
		if i.TempHive != nil || i.HumidityHive != nil || i.HumidityOutside != nil || i.Frames != nil || i.BroodFrames != nil ||
			i.HoneyKg != nil || i.FedKg != nil || i.FramesAdded != nil || i.FramesRemoved != nil || i.YoungestLarva != nil {
			t.Fatalf("values that were not sent must stay unset: %+v", i)
		}
	}
	// Devices get the same picture: no invented zeros in the payload.
	chs := changesFor(f.pullAll(t, ctx), "inspection")
	if p := chs[0].PayloadJson; strings.Contains(p, "temp_hive") || strings.Contains(p, `"frames"`) ||
		!strings.Contains(p, `"temp_outside":0`) || !strings.Contains(p, `"queen_cells":0`) {
		t.Fatalf("payload: %s", p)
	}

	// A device may push NULL for every optional number ("not recorded").
	err = push(f.sync, ctx, change("inspection", "insp-dev", "apiary-a",
		`{"organization_id":"org-a","hive_id":"hive-a","date":"2026-05-01T10:00:00.000Z","temperament":null,"frames":null,"stores":null,"queen_cells":null,"honey_kg":null,"brood_frames":null,"calmness":null,"fed_kg":null,"frames_added":null,"frames_removed":null,"weight_kg":null,"youngest_larva":null,"temp_hive":null,"temp_outside":null,"humidity_hive":null,"humidity_outside":null,"source":1,"created_at":"2026-05-01T10:00:00.000Z","deleted":0}`))
	if err != nil {
		t.Fatalf("NULL numbers must be accepted: %v", err)
	}
	ls, _ = f.insp.ListInspections(ctx, &wv1.ListInspectionsRequest{})
	var dev *wv1.Inspection
	for _, i := range ls.Inspections {
		if i.Id == "insp-dev" {
			dev = i
		}
	}
	if dev == nil || dev.Frames != nil || dev.WeightKg != nil || dev.QueenCells != nil || dev.Calmness != 0 ||
		dev.Temperament != wv1.Temperament_TEMPERAMENT_UNSPECIFIED || dev.Source != wv1.InspectionSource_INSPECTION_SOURCE_MANUAL {
		t.Fatalf("device inspection: %+v", dev)
	}
}
