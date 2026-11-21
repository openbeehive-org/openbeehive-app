package service

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// The dashboard's due list uses the same interval as the app: a hive is due
// ten days after its last visit, and one visited today is not.
func TestDashboardDueUsesTheAppsInterval(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	visit := func(hiveID string, ago time.Duration) {
		t.Helper()
		if _, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
			HiveId: hiveID, Date: timestamppb.New(time.Now().Add(-ago)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	visit("hive-a", 12*24*time.Hour)
	fresh, err := f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{ApiaryId: "apiary-a", Name: "Fresh"})
	if err != nil {
		t.Fatal(err)
	}
	visit(fresh.Hive.Id, time.Hour)

	d, err := f.stats.GetDashboard(ctx, &wv1.DashboardRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DueInspections) != 1 || d.DueInspections[0].HiveId != "hive-a" || d.DueInspections[0].DaysSinceLast != 12 {
		t.Fatalf("due with the default interval: %+v", d.DueInspections)
	}
	// An explicit threshold still applies.
	d, err = f.stats.GetDashboard(ctx, &wv1.DashboardRequest{ThresholdDays: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DueInspections) != 0 {
		t.Fatalf("due with a 20-day threshold: %+v", d.DueInspections)
	}
}
