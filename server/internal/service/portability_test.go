package service

// Behaviour that has to be the same on SQLite, Postgres and the MySQL family
// although the three store and compare text and timestamps differently.

import (
	"testing"

	"google.golang.org/protobuf/proto"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

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
