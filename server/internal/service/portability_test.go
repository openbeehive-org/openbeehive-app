package service

// Behaviour that has to be the same on SQLite, Postgres and the MySQL family
// although the three store and compare text and timestamps differently.

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// TestFrozenContextComparesInstants: where a hive stood and which queen
// reigned at a given moment is decided by the instants the timestamps denote,
// whatever form a client wrote them in (with or without milliseconds, with a
// zone offset).
func TestFrozenContextComparesInstants(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := push(f.sync, ctx,
		change("apiary", "apiary-a2", "apiary-a2",
			`{"name":"Second","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","deleted":0}`),
		// The hive stood on apiary-a until 10:00 UTC on 1 June, written with
		// a +02:00 offset, and on apiary-a2 from that moment on.
		change("placement", "pl-1", "apiary-a",
			`{"hive_id":"hive-a","apiary_id":"apiary-a","start_at":"2026-03-01T08:00:00Z","end_at":"2026-06-01T12:00:00+02:00","deleted":0}`),
		change("placement", "pl-2", "apiary-a2",
			`{"hive_id":"hive-a","apiary_id":"apiary-a2","start_at":"2026-06-01T12:00:00+02:00","deleted":0}`),
		// The first queen arrived at that same moment, written without
		// milliseconds, and was replaced on 1 August.
		change("queen", "queen-1", "apiary-a",
			`{"hive_id":"hive-a","year":2025,"introduced_at":"2026-06-01T10:00:00Z","replaced_at":"2026-08-01T02:00:00+02:00","active":0,`+
				`"created_at":"2026-06-01T10:00:00Z","updated_at":"2026-06-01T10:00:00Z","deleted":0}`),
		change("queen", "queen-2", "apiary-a",
			`{"hive_id":"hive-a","year":2026,"introduced_at":"2026-08-01T00:00:00Z","active":1,`+
				`"created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-01T00:00:00Z","deleted":0}`),
	); err != nil {
		t.Fatalf("push: %v", err)
	}

	// A treatment at exactly 10:00:00.000 UTC on 1 June.
	at := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	res, err := f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{
		HiveId: "hive-a", Product: "Formic acid 60%", Date: timestamppb.New(at),
	})
	if err != nil {
		t.Fatalf("create treatment: %v", err)
	}
	if got := res.Treatment.ApiaryId; got != "apiary-a2" {
		t.Errorf("apiary at %s: got %q, want apiary-a2", at.Format(time.RFC3339), got)
	}
	if got := res.Treatment.QueenId; got != "queen-1" {
		t.Errorf("queen at %s: got %q, want queen-1", at.Format(time.RFC3339), got)
	}

	// The offset form is stored as the instant it denotes.
	queens, err := f.queen.ListQueens(ctx, &wv1.ListQueensRequest{HiveId: "hive-a"})
	if err != nil {
		t.Fatalf("list queens: %v", err)
	}
	for _, q := range queens.Queens {
		if q.Id != "queen-1" {
			continue
		}
		if want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC); !q.ReplacedAt.AsTime().Equal(want) {
			t.Errorf("replaced_at: got %s, want %s", q.ReplacedAt.AsTime(), want)
		}
	}
}

// TestPushStoresLargePhoto: a hive photo of about 1 MB (inline data URL)
// is stored and comes back through Pull byte for byte.
func TestPushStoresLargePhoto(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	photo := "data:image/jpeg;base64," + strings.Repeat("A", 1<<20)
	payload := `{"apiary_id":"apiary-a","name":"Photo hive","photo":"` + photo +
		`","created_at":"2026-07-11T10:00:00Z","updated_at":"2026-07-11T10:00:00Z"}`
	if err := push(svc, ctx, change("hive", "hive-photo", "apiary-a", payload)); err != nil {
		t.Fatalf("push: %v", err)
	}
	var stored string
	if err := svc.db.Get(&stored, `SELECT photo FROM hive WHERE id = 'hive-photo'`); err != nil || stored != photo {
		t.Fatalf("stored photo: %d bytes, err %v; want %d bytes", len(stored), err, len(photo))
	}
	res, err := svc.Pull(ctx, &wv1.PullRequest{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Changes) != 1 || !strings.Contains(res.Changes[0].PayloadJson, `"photo":"`+photo+`"`) {
		t.Fatalf("pulled %d changes; the photo must come back unchanged", len(res.Changes))
	}
}

// TestDecimalsReadBackAsEntered: weights, temperatures and coordinates come
// back as the numbers that were sent, not as their nearest 4-byte float.
func TestDecimalsReadBackAsEntered(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	res, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: "hive-a", WeightKg: proto.Float64(12.3), FedKg: proto.Float64(0.7),
		TempHive: proto.Float64(34.8), HumidityOutside: proto.Float64(61.9),
	})
	if err != nil {
		t.Fatalf("create inspection: %v", err)
	}
	got := res.Inspection
	if got.GetWeightKg() != 12.3 || got.GetFedKg() != 0.7 || got.GetTempHive() != 34.8 || got.GetHumidityOutside() != 61.9 {
		t.Fatalf("inspection: weight %v, fed %v, temp %v, humidity %v",
			got.GetWeightKg(), got.GetFedKg(), got.GetTempHive(), got.GetHumidityOutside())
	}

	if err := push(f.sync, ctx, change("apiary", "apiary-geo", "apiary-geo",
		`{"name":"Geo","lat":48.193127,"lng":7.770411,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","deleted":0}`)); err != nil {
		t.Fatalf("push apiary: %v", err)
	}
	var lat, lng float64
	if err := f.sync.db.QueryRow(`SELECT lat, lng FROM apiary WHERE id = 'apiary-geo'`).Scan(&lat, &lng); err != nil {
		t.Fatalf("read apiary: %v", err)
	}
	if lat != 48.193127 || lng != 7.770411 {
		t.Fatalf("coordinates: %v, %v; want 48.193127, 7.770411", lat, lng)
	}
}
