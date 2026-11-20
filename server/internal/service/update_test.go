package service

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

func mask(paths ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: paths} }

func eventsOfType(evs []*wv1.Event, typ wv1.EventType) []*wv1.Event {
	var out []*wv1.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestUpdateApiaryChangesOnlyTheFieldsSent(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.apiary.CreateApiary(ctx, &wv1.CreateApiaryRequest{
		Name: "Orchard", Address: "Apfelweg 3", Lat: 48.125, Lng: 7.75, Note: "south slope",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := cr.Apiary.Id

	// A script that sends only the name must not wipe the rest.
	up, err := f.apiary.UpdateApiary(ctx, &wv1.UpdateApiaryRequest{Id: id, Name: "Old orchard"})
	if err != nil {
		t.Fatal(err)
	}
	if a := up.Apiary; a.Name != "Old orchard" || a.Address != "Apfelweg 3" || a.Lat != 48.125 || a.Lng != 7.75 || a.Note != "south slope" {
		t.Fatalf("fields not sent must keep their value: %+v", a)
	}
	// Devices receive only the changed field.
	chs := changesFor(f.pullAll(t, ctx), "apiary")
	if last := chs[len(chs)-1]; strings.Contains(last.PayloadJson, "address") || !strings.Contains(last.PayloadJson, `"name":"Old orchard"`) {
		t.Fatalf("update payload must carry only what changed: %s", last.PayloadJson)
	}

	// A mask writes exactly the masked fields, including to their zero value.
	up, err = f.apiary.UpdateApiary(ctx, &wv1.UpdateApiaryRequest{Id: id, Name: "ignored", UpdateMask: mask("note", "lat")})
	if err != nil {
		t.Fatal(err)
	}
	if a := up.Apiary; a.Note != "" || a.Lat != 0 || a.Name != "Old orchard" || a.Lng != 7.75 {
		t.Fatalf("mask must clear note and lat only: %+v", a)
	}

	// Caller mistakes are invalid_argument.
	_, err = f.apiary.UpdateApiary(ctx, &wv1.UpdateApiaryRequest{Id: id})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.apiary.UpdateApiary(ctx, &wv1.UpdateApiaryRequest{Id: id, UpdateMask: mask("name")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.apiary.UpdateApiary(ctx, &wv1.UpdateApiaryRequest{Id: id, Name: "x", UpdateMask: mask("hive_count")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.apiary.UpdateApiary(identityCtx("user-b", "org-b"), &wv1.UpdateApiaryRequest{Id: id, Name: "x"})
	wantCode(t, err, connect.CodeNotFound)
}

func TestUpdateHiveChangesOnlyTheFieldsSent(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{
		ApiaryId: "apiary-a", Name: "H7", Type: wv1.HiveType_HIVE_TYPE_DADANT, Boxes: 3,
		ColonyOrigin: "swarm 2025", Note: "strong",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := cr.Hive.Id

	up, err := f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{Hive: &wv1.Hive{Id: id, Name: "H7b"}})
	if err != nil {
		t.Fatal(err)
	}
	if h := up.Hive; h.Name != "H7b" || h.Type != wv1.HiveType_HIVE_TYPE_DADANT || h.Boxes != 3 ||
		h.Status != wv1.HiveStatus_HIVE_STATUS_ACTIVE || h.ColonyOrigin != "swarm 2025" || h.Note != "strong" {
		t.Fatalf("fields not sent must keep their value: %+v", h)
	}
	up, err = f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{
		Hive:       &wv1.Hive{Id: id, Status: wv1.HiveStatus_HIVE_STATUS_QUEENLESS},
		UpdateMask: mask("status", "note"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if h := up.Hive; h.Status != wv1.HiveStatus_HIVE_STATUS_QUEENLESS || h.Note != "" || h.Name != "H7b" || h.Boxes != 3 {
		t.Fatalf("mask must write status and clear note only: %+v", h)
	}
	_, err = f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{Hive: &wv1.Hive{Id: id}, UpdateMask: mask("status")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{Hive: &wv1.Hive{Id: id}, UpdateMask: mask("name")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{Hive: &wv1.Hive{Id: id, Name: "x"}, UpdateMask: mask("apiary_id")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.hive.UpdateHive(ctx, &wv1.UpdateHiveRequest{Hive: &wv1.Hive{Id: id}})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestUpdateQueenChangesOnlyTheFieldsSent(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{
		HiveId: "hive-a", Year: 2025, Origin: "Carnica", BreederNumber: "B-12",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := cr.Queen.Id
	up, err := f.queen.UpdateQueen(ctx, &wv1.UpdateQueenRequest{Queen: &wv1.Queen{Id: id, Note: "gentle"}})
	if err != nil {
		t.Fatal(err)
	}
	if q := up.Queen; q.Note != "gentle" || q.Year != 2025 || q.Origin != "Carnica" || q.BreederNumber != "B-12" ||
		q.Marking != wv1.MarkingColor(markingForYear(2025)) || !q.Active || q.IntroducedAt == nil {
		t.Fatalf("fields not sent must keep their value: %+v", q)
	}
	// Sending the end of the reign retires the queen without a mask.
	up, err = f.queen.UpdateQueen(ctx, &wv1.UpdateQueenRequest{Queen: &wv1.Queen{Id: id, ReplacedAt: day(2026, 6, 1)}})
	if err != nil || up.Queen.Active || up.Queen.ReplacedAt == nil {
		t.Fatalf("retire: %v %+v", err, up)
	}
	// With a mask, a year correction can reset the colour to the year's.
	up, err = f.queen.UpdateQueen(ctx, &wv1.UpdateQueenRequest{
		Queen: &wv1.Queen{Id: id, Year: 2024, Active: true}, UpdateMask: mask("year", "marking", "active", "replaced_at"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if q := up.Queen; q.Year != 2024 || q.Marking != wv1.MarkingColor(markingForYear(2024)) || !q.Active || q.ReplacedAt != nil || q.Note != "gentle" {
		t.Fatalf("masked update: %+v", q)
	}
	_, err = f.queen.UpdateQueen(ctx, &wv1.UpdateQueenRequest{Queen: &wv1.Queen{Id: id}, UpdateMask: mask("year")})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestUpdateHarvestCorrectsRecordEventAndStats(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	year := time.Now().Year()
	q1, _ := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2025, IntroducedAt: day(year-1, 4, 1)})
	q2, _ := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026, IntroducedAt: day(year, 3, 1)})
	cr, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{
		HiveId: "hive-a", AmountKg: 18.5, Variety: "spring flow", BatchNumber: "L1", Date: day(year, 3, 15),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Harvest.AmendedAt != nil || cr.Harvest.QueenId != q2.Queen.Id {
		t.Fatalf("new harvest: %+v", cr.Harvest)
	}

	up, err := f.harv.UpdateHarvest(ctx, &wv1.UpdateHarvestRequest{Harvest: &wv1.Harvest{Id: cr.Harvest.Id, AmountKg: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if h := up.Harvest; h.AmountKg != 12 || h.Variety != "spring flow" || h.BatchNumber != "L1" || h.AmendedAt == nil {
		t.Fatalf("corrected harvest: %+v", h)
	}
	ev := eventsOfType(f.hiveEvents(t, ctx), wv1.EventType_EVENT_TYPE_HARVEST)
	if len(ev) != 1 || ev[0].AmountKg != 12 || ev[0].Title != "12 kg spring flow" {
		t.Fatalf("the fact event must follow the correction: %+v", ev)
	}
	hs, _ := f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
	if len(hs.PerYear) != 1 || hs.PerYear[0].Kg != 12 {
		t.Fatalf("stats must show the corrected amount: %+v", hs.PerYear)
	}
	// Devices learn about the amendment.
	chs := changesFor(f.pullAll(t, ctx), "harvest")
	if last := chs[len(chs)-1]; !strings.Contains(last.PayloadJson, `"amended_at"`) || strings.Contains(last.PayloadJson, "variety") {
		t.Fatalf("amendment payload: %s", last.PayloadJson)
	}

	// Moving the date into the previous queen's reign freezes the context again.
	up, err = f.harv.UpdateHarvest(ctx, &wv1.UpdateHarvestRequest{
		Harvest: &wv1.Harvest{Id: cr.Harvest.Id, Date: day(year-1, 7, 1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Harvest.QueenId != q1.Queen.Id || up.Harvest.Date.AsTime().Year() != year-1 {
		t.Fatalf("context must follow the date: %+v", up.Harvest)
	}
	ev = eventsOfType(f.hiveEvents(t, ctx), wv1.EventType_EVENT_TYPE_HARVEST)
	if len(ev) != 1 || ev[0].QueenId != q1.Queen.Id || ev[0].Date.AsTime().Year() != year-1 {
		t.Fatalf("event must follow the date: %+v", ev)
	}

	_, err = f.harv.UpdateHarvest(ctx, &wv1.UpdateHarvestRequest{Harvest: &wv1.Harvest{Id: cr.Harvest.Id}, UpdateMask: mask("amount_kg")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.harv.UpdateHarvest(ctx, &wv1.UpdateHarvestRequest{Harvest: &wv1.Harvest{Id: cr.Harvest.Id}, UpdateMask: mask("date")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.harv.UpdateHarvest(ctx, &wv1.UpdateHarvestRequest{Harvest: &wv1.Harvest{Id: cr.Harvest.Id, HiveId: "x"}, UpdateMask: mask("hive_id")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.harv.UpdateHarvest(identityCtx("user-b", "org-b"), &wv1.UpdateHarvestRequest{Harvest: &wv1.Harvest{Id: cr.Harvest.Id, AmountKg: 1}})
	wantCode(t, err, connect.CodeNotFound)
}

func TestUpdateTreatmentSetsAmendedAt(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{
		HiveId: "hive-a", Product: "Oxuvar", Dose: "50 ml", WithdrawalUntil: day(2026, 12, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Treatment.AmendedAt != nil {
		t.Fatalf("a new treatment is not amended: %+v", cr.Treatment)
	}
	up, err := f.treat.UpdateTreatment(ctx, &wv1.UpdateTreatmentRequest{
		Treatment: &wv1.Treatment{Id: cr.Treatment.Id, Product: "Oxuvar 5.7%", BatchNumber: "OX-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr := up.Treatment; tr.Product != "Oxuvar 5.7%" || tr.BatchNumber != "OX-9" || tr.Dose != "50 ml" ||
		tr.WithdrawalUntil == nil || tr.Reason != "varroa" || tr.AmendedAt == nil {
		t.Fatalf("corrected treatment: %+v", tr)
	}
	ev := eventsOfType(f.hiveEvents(t, ctx), wv1.EventType_EVENT_TYPE_TREATMENT)
	if len(ev) != 1 || ev[0].Title != "Oxuvar 5.7%" {
		t.Fatalf("event title must follow the product: %+v", ev)
	}
	// A mask clears the withdrawal date.
	up, err = f.treat.UpdateTreatment(ctx, &wv1.UpdateTreatmentRequest{
		Treatment: &wv1.Treatment{Id: cr.Treatment.Id}, UpdateMask: mask("withdrawal_until"),
	})
	if err != nil || up.Treatment.WithdrawalUntil != nil || up.Treatment.Product != "Oxuvar 5.7%" {
		t.Fatalf("clear withdrawal: %v %+v", err, up)
	}
	_, err = f.treat.UpdateTreatment(ctx, &wv1.UpdateTreatmentRequest{Treatment: &wv1.Treatment{Id: cr.Treatment.Id}, UpdateMask: mask("product")})
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestUpdateInspectionAndTask(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	cr, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: "hive-a", Date: day(2026, 5, 1), Frames: proto.Int32(8), QueenSeen: true, Note: "calm",
	})
	if err != nil {
		t.Fatal(err)
	}
	up, err := f.insp.UpdateInspection(ctx, &wv1.UpdateInspectionRequest{
		Inspection: &wv1.Inspection{Id: cr.Inspection.Id, BroodFrames: proto.Int32(0), Date: day(2026, 5, 3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if i := up.Inspection; i.GetFrames() != 8 || !i.QueenSeen || i.Note != "calm" ||
		i.BroodFrames == nil || i.GetBroodFrames() != 0 || i.Date.AsTime().Day() != 3 {
		t.Fatalf("corrected inspection: %+v", i)
	}
	ev := eventsOfType(f.hiveEvents(t, ctx), wv1.EventType_EVENT_TYPE_INSPECTION)
	if len(ev) != 1 || ev[0].Date.AsTime().Day() != 3 {
		t.Fatalf("event must follow the date: %+v", ev)
	}
	// A mask clears an optional number back to "not recorded" and a flag to false.
	up, err = f.insp.UpdateInspection(ctx, &wv1.UpdateInspectionRequest{
		Inspection: &wv1.Inspection{Id: cr.Inspection.Id}, UpdateMask: mask("frames", "queen_seen"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if i := up.Inspection; i.Frames != nil || i.QueenSeen || i.BroodFrames == nil {
		t.Fatalf("masked clear: %+v", i)
	}
	_, err = f.insp.UpdateInspection(ctx, &wv1.UpdateInspectionRequest{Inspection: &wv1.Inspection{Id: cr.Inspection.Id}, UpdateMask: mask("photo_keys")})
	wantCode(t, err, connect.CodeInvalidArgument)

	tk, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "feed", HiveId: "hive-a", DueAt: day(2026, 9, 1), Note: "2:1 syrup"})
	if err != nil {
		t.Fatal(err)
	}
	ut, err := f.task.UpdateTask(ctx, &wv1.UpdateTaskRequest{Task: &wv1.Task{Id: tk.Task.Id, Title: "feed 5 kg", Done: true}})
	if err != nil {
		t.Fatal(err)
	}
	if x := ut.Task; x.Title != "feed 5 kg" || !x.Done || x.Note != "2:1 syrup" || x.DueAt == nil || x.ApiaryId != "apiary-a" {
		t.Fatalf("updated task: %+v", x)
	}
	ut, err = f.task.UpdateTask(ctx, &wv1.UpdateTaskRequest{Task: &wv1.Task{Id: tk.Task.Id}, UpdateMask: mask("done", "due_at")})
	if err != nil || ut.Task.Done || ut.Task.DueAt != nil || ut.Task.Title != "feed 5 kg" {
		t.Fatalf("reopen and undate: %v %+v", err, ut)
	}
	_, err = f.task.UpdateTask(ctx, &wv1.UpdateTaskRequest{Task: &wv1.Task{Id: tk.Task.Id}, UpdateMask: mask("title")})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = f.task.UpdateTask(ctx, &wv1.UpdateTaskRequest{Task: &wv1.Task{Id: "nope", Title: "x"}})
	wantCode(t, err, connect.CodeNotFound)
	// Another member's personal task cannot be updated.
	mine, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "dentist"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.task.UpdateTask(identityCtx("user-a2", "org-a"), &wv1.UpdateTaskRequest{Task: &wv1.Task{Id: mine.Task.Id, Title: "x"}})
	wantCode(t, err, connect.CodeNotFound)
}
