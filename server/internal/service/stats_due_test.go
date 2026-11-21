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

// The app and the API count days alike: whole calendar days, so a visit late
// on the day ten days back is due early in the morning, as in the app.
func TestDashboardDueCountsCalendarDays(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	y, m, d := time.Now().UTC().Date()
	now := time.Date(y, m, d, 1, 0, 0, 0, time.UTC)
	visited := now.AddDate(0, 0, -10).Add(22 * time.Hour) // 9 days and 3 hours before now
	if _, err := f.insp.CreateInspection(ctx, &wv1.CreateInspectionRequest{
		HiveId: "hive-a", Date: timestamppb.New(visited),
	}); err != nil {
		t.Fatal(err)
	}
	res := &wv1.DashboardResponse{}
	q := dashboardQuery{db: f.db, org: "org-a", user: "user-a", now: now, threshold: defaultDueDays}
	if err := dashboardDue(ctx, q, res); err != nil {
		t.Fatal(err)
	}
	if len(res.DueInspections) != 1 || res.DueInspections[0].DaysSinceLast != 10 {
		t.Fatalf("due = %+v, want hive-a 10 days ago", res.DueInspections)
	}
	for _, c := range []struct {
		d, now string
		want   int
	}{
		{"2026-06-14T23:50:00Z", "2026-06-15T00:10:00Z", 1},
		{"2026-06-15T00:10:00Z", "2026-06-15T23:50:00Z", 0},
		{"2026-06-16T12:00:00Z", "2026-06-15T12:00:00Z", 0},
		{"2026-06-01T12:00:00Z", "2026-06-15T08:00:00Z", 14},
	} {
		d, _ := time.Parse(time.RFC3339, c.d)
		n, _ := time.Parse(time.RFC3339, c.now)
		if got := calendarDaysSince(d, n); got != c.want {
			t.Errorf("calendarDaysSince(%s, %s) = %d, want %d", c.d, c.now, got, c.want)
		}
	}
}
