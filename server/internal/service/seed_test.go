package service

import (
	"context"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// Seeding writes through the same flows as the API (placement, frozen
// context, events, change log) and, run again, rewrites the same rows.
func TestSeedUsesTheWriteFlowsAndIsRepeatable(t *testing.T) {
	f := newAPIFixture(t)
	id := auth.Identity{UserID: "seed-user", OrgID: "seed-org"}
	ctx := auth.WithIdentity(context.Background(), id)
	founded := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	harvested := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	seed := func() {
		t.Helper()
		err := f.writer.Seed(ctx, id, func(s *Seeder) error {
			if err := s.Wipe(); err != nil {
				return err
			}
			if err := s.Apiary(SeedApiary{ID: "seed-apiary", Name: "Meadow", Lat: 48.1, Lng: 7.8, CreatedAt: founded}); err != nil {
				return err
			}
			if err := s.Hive(SeedHive{ID: "seed-hive", ApiaryID: "seed-apiary", Name: "Hive 1", Type: 1, Boxes: 2, CreatedAt: founded}); err != nil {
				return err
			}
			if err := s.Queen(SeedQueen{ID: "seed-queen", HiveID: "seed-hive", Year: 2025, Origin: "Carnica", IntroducedAt: founded.AddDate(0, 2, 0)}); err != nil {
				return err
			}
			if err := s.Inspection("seed-insp", "seed-hive", harvested.AddDate(0, 0, -7), map[string]any{"frames": 9, "queen_seen": 1}); err != nil {
				return err
			}
			if err := s.Harvest("seed-harv", "seed-hive", harvested, map[string]any{
				"amount_kg": 14.5, "variety": "summer flow", "best_before": harvested.AddDate(2, 0, 0),
			}); err != nil {
				return err
			}
			return s.Treatment("seed-treat", "seed-hive", harvested.AddDate(0, 1, 0), map[string]any{"product": "Formic acid 60%"})
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	count := func(table string) int {
		t.Helper()
		n, err := countRows(ctx, f.db, "SELECT COUNT(*) FROM "+table+" WHERE organization_id = ?", id.OrgID)
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	seed()
	hv, err := f.harv.ListHarvests(ctx, &wv1.ListHarvestsRequest{})
	if err != nil || len(hv.Harvests) != 1 {
		t.Fatalf("harvests: %v %+v", err, hv)
	}
	if h := hv.Harvests[0]; h.Id != "seed-harv" || h.ApiaryId != "seed-apiary" || h.QueenId != "seed-queen" ||
		!h.Date.AsTime().Equal(harvested) || h.BestBefore == nil {
		t.Fatalf("seeded harvest: %+v", h)
	}
	hs, _ := f.stats.GetHoneyStats(ctx, &wv1.HoneyStatsRequest{})
	if len(hs.PerQueen) != 1 || hs.PerQueen[0].Kg != 14.5 || hs.PerApiary[0].Label != "Meadow" {
		t.Fatalf("seeded data must feed the statistics: %+v", hs)
	}
	// created, queen introduced, inspection, harvest, treatment
	if n := count("event"); n != 5 {
		t.Fatalf("events: %d", n)
	}
	changes := len(f.pullAll(t, ctx))

	seed()
	for table, want := range map[string]int{"apiary": 1, "hive": 1, "placement": 1, "queen": 1, "inspection": 1, "harvest": 1, "treatment": 1, "event": 5} {
		if n := count(table); n != want {
			t.Fatalf("%s after seeding twice: %d rows, want %d", table, n, want)
		}
	}
	if n := len(f.pullAll(t, ctx)); n != changes {
		t.Fatalf("change feed after seeding twice: %d entries, want %d", n, changes)
	}
	// Other tenants are untouched by Wipe.
	if n, _ := countRows(ctx, f.db, `SELECT COUNT(*) FROM hive WHERE organization_id = ?`, "org-a"); n != 1 {
		t.Fatalf("wipe must stay inside the tenant, org-a has %d hives", n)
	}
}
