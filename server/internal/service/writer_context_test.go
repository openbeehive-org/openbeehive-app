package service

import (
	"testing"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// Hive ids are printed on QR labels. A foreign tenant that stores a placement
// and an active queen naming such a hive must not influence, or break, what
// the hive's own tenant writes.
func TestWriterIgnoresForeignTenantPlacementAndQueen(t *testing.T) {
	f := newAPIFixture(t)
	a, b := identityCtx("user-a", "org-a"), identityCtx("user-b", "org-b")
	err := push(f.sync, b,
		change("placement", "pl-b", "apiary-b", `{"organization_id":"org-b","hive_id":"hive-a","apiary_id":"apiary-b","start_at":"2020-01-01T00:00:00.000Z","end_at":null,"deleted":0}`),
		change("queen", "queen-b", "apiary-b", `{"organization_id":"org-b","hive_id":"hive-a","year":2020,"introduced_at":"2020-01-01T00:00:00.000Z","replaced_at":null,"active":1,"created_at":"2020-01-01T00:00:00.000Z","updated_at":"2020-01-01T00:00:00.000Z","deleted":0}`))
	if err != nil {
		t.Fatalf("tenant B push: %v", err)
	}

	hv, err := f.harv.CreateHarvest(a, &wv1.CreateHarvestRequest{HiveId: "hive-a", AmountKg: 5})
	if err != nil {
		t.Fatal(err)
	}
	if hv.Harvest.ApiaryId != "apiary-a" || hv.Harvest.QueenId != "" {
		t.Fatalf("harvest context must come from tenant A only: apiary %q queen %q", hv.Harvest.ApiaryId, hv.Harvest.QueenId)
	}
	tr, err := f.treat.CreateTreatment(a, &wv1.CreateTreatmentRequest{HiveId: "hive-a", Product: "Oxuvar"})
	if err != nil || tr.Treatment.ApiaryId != "apiary-a" || tr.Treatment.QueenId != "" {
		t.Fatalf("treatment context: %v %+v", err, tr)
	}
	if _, err := f.insp.CreateInspection(a, &wv1.CreateInspectionRequest{HiveId: "hive-a"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.pullAll(t, a) {
		if c.ScopeId != "apiary-a" {
			t.Fatalf("tenant A's change landed in scope %q: %+v", c.ScopeId, c)
		}
	}

	// The foreign rows must not make tenant A's own flows fail either.
	if _, err := f.queen.CreateQueen(a, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026}); err != nil {
		t.Fatalf("create queen: %v", err)
	}
	ap, err := f.apiary.CreateApiary(a, &wv1.CreateApiaryRequest{Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.hive.RelocateHive(a, &wv1.RelocateHiveRequest{Id: "hive-a", TargetApiaryId: ap.Apiary.Id}); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	// Tenant B's rows are untouched.
	qs, _ := f.queen.ListQueens(b, &wv1.ListQueensRequest{OnlyActive: true})
	if len(qs.Queens) != 1 || qs.Queens[0].Id != "queen-b" {
		t.Fatalf("tenant B's queen must stay active: %+v", qs.Queens)
	}
}

// Two devices that moved or requeened a hive while offline leave two open
// placements and two active queens. The server must pick one the same way
// every time, and its own flows must close all of them.
func TestCurrentPlacementAndQueenAreDeterministic(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	ap, err := f.apiary.CreateApiary(ctx, &wv1.CreateApiaryRequest{Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	second := ap.Apiary.Id
	queen := func(id, introduced string) *wv1.Change {
		return change("queen", id, "apiary-a", `{"organization_id":"org-a","hive_id":"hive-a","year":2026,"introduced_at":"`+introduced+
			`","replaced_at":null,"active":1,"created_at":"`+introduced+`","updated_at":"`+introduced+`","deleted":0}`)
	}
	placement := func(id, apiary string) *wv1.Change {
		return change("placement", id, apiary, `{"organization_id":"org-a","hive_id":"hive-a","apiary_id":"`+apiary+
			`","start_at":"2026-04-01T08:00:00.000Z","end_at":null,"deleted":0}`)
	}
	err = push(f.sync, ctx,
		placement("pl-2", second), placement("pl-1", "apiary-a"),
		queen("q-old", "2026-03-01T08:00:00.000Z"), queen("q-tie-2", "2026-04-01T08:00:00.000Z"), queen("q-tie-1", "2026-04-01T08:00:00.000Z"))
	if err != nil {
		t.Fatal(err)
	}

	d, err := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if d.ActiveQueens != 1 {
		t.Fatalf("one hive has one current queen, got %d", d.ActiveQueens)
	}
	// Same start: the higher id wins, for the placement and for the queen.
	for i := 0; i < 3; i++ {
		hv, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: "hive-a", AmountKg: 1})
		if err != nil {
			t.Fatal(err)
		}
		if hv.Harvest.ApiaryId != second || hv.Harvest.QueenId != "q-tie-2" {
			t.Fatalf("context must be the latest start, ties by id: apiary %q queen %q", hv.Harvest.ApiaryId, hv.Harvest.QueenId)
		}
	}

	// Requeening retires every queen still flagged active.
	nq, err := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2027})
	if err != nil {
		t.Fatal(err)
	}
	active, _ := f.queen.ListQueens(ctx, &wv1.ListQueensRequest{HiveId: "hive-a", OnlyActive: true})
	if len(active.Queens) != 1 || active.Queens[0].Id != nq.Queen.Id {
		t.Fatalf("only the new queen may stay active: %+v", active.Queens)
	}
	// Moving closes every open placement.
	if _, err := f.hive.RelocateHive(ctx, &wv1.RelocateHiveRequest{Id: "hive-a", TargetApiaryId: "apiary-a"}); err != nil {
		t.Fatal(err)
	}
	open, err := countRows(ctx, f.db, `SELECT COUNT(*) FROM placement WHERE hive_id = ? AND end_at IS NULL AND deleted = FALSE`, "hive-a")
	if err != nil || open != 1 {
		t.Fatalf("exactly one placement may stay open, got %d (%v)", open, err)
	}
}
