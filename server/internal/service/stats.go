package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
	_ "time/tzdata" // IANA time zone names on hosts without a zone database

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

// StatsService computes the dashboard and honey figures from the tenant's
// rows. Aggregation happens in Go over small result sets, which keeps the
// SQL identical across SQLite, Postgres and MySQL and mirrors how the app
// computes the same numbers locally (dashboard route, history.ts).
type StatsService struct {
	openbeehivev1connect.UnimplementedStatsServiceHandler
	w *Writer
}

func NewStatsService(w *Writer) *StatsService { return &StatsService{w: w} }

// defaultDueDays is when a hive becomes due for a visit; the app's dashboard
// uses the same interval (DUE_AFTER_DAYS in app/src/lib/hivelist.ts).
const defaultDueDays = 10

// dashboardQuery is what every dashboard figure is computed for: one
// member of one tenant at one point in time.
type dashboardQuery struct {
	db        queryer
	org, user string
	now       time.Time
	threshold int
	// loc is the caller's time zone; calendar days start at its midnight.
	loc *time.Location
}

func (s *StatsService) GetDashboard(ctx context.Context, req *wv1.DashboardRequest) (*wv1.DashboardResponse, error) {
	id, _ := auth.FromContext(ctx)
	loc, err := callerZone(req.TimeZone)
	if err != nil {
		return nil, invalid(err.Error())
	}
	q := dashboardQuery{db: s.w.db, org: id.OrgID, user: id.UserID, now: time.Now().UTC(), threshold: int(req.ThresholdDays), loc: loc}
	if q.threshold <= 0 {
		q.threshold = defaultDueDays
	}
	res := &wv1.DashboardResponse{}
	for _, fill := range []func(context.Context, dashboardQuery, *wv1.DashboardResponse) error{
		dashboardCounts, dashboardTasks, dashboardHoney, dashboardDue, dashboardRecent,
	} {
		if err := fill(ctx, q, res); err != nil {
			return nil, writeErr(err)
		}
	}
	return res, nil
}

// dashboardCounts: apiaries, hives and queens in office. A hive has one
// current queen however many rows are flagged active (two devices that
// requeened offline leave two), and a deleted hive has none.
func dashboardCounts(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	var err error
	if res.Apiaries, err = count32(ctx, q.db, `SELECT COUNT(*) FROM apiary WHERE organization_id = ? AND deleted = FALSE`, q.org); err != nil {
		return err
	}
	if res.Hives, err = count32(ctx, q.db, `SELECT COUNT(*) FROM hive WHERE organization_id = ? AND deleted = FALSE`, q.org); err != nil {
		return err
	}
	res.ActiveQueens, err = count32(ctx, q.db, `SELECT COUNT(DISTINCT hive_id) FROM queen
		WHERE organization_id = ? AND deleted = FALSE AND active = TRUE
		  AND hive_id IN (SELECT id FROM hive WHERE organization_id = ? AND deleted = FALSE)`, q.org, q.org)
	return err
}

// dashboardTasks: open, overdue, and the next five by due date, among the
// tasks this member may see.
func dashboardTasks(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	tasks, err := queryMaps(ctx, q.db, `SELECT * FROM task
		WHERE organization_id = ? AND deleted = FALSE AND done = FALSE`+visibleTasks+tasksInOrder(q.db.DriverName()),
		q.org, q.user)
	if err != nil {
		return err
	}
	res.OpenTasks = i32(int64(len(tasks)))
	for _, r := range tasks {
		if due, ok := rtime(r["due_at"]); ok && due.Before(q.now) {
			res.OverdueTasks++
		}
		if len(res.NextTasks) < 5 {
			res.NextTasks = append(res.NextTasks, taskFromRow(r))
		}
	}
	return nil
}

// countedHarvests selects the HARVEST fact rows that count in the honey
// figures (alias e; arguments: tenant, event type). An event is left out
// when it is deleted, when its hive is deleted, or when the harvest record
// it points at is deleted: app versions that delete a harvest without its
// event must not leave the honey counted.
const countedHarvests = ` FROM event e
	WHERE e.organization_id = ? AND e.deleted = FALSE AND e.type = ?
	  AND NOT EXISTS (SELECT 1 FROM hive h WHERE h.id = e.hive_id
	    AND h.organization_id = e.organization_id AND h.deleted = TRUE)
	  AND NOT EXISTS (SELECT 1 FROM harvest v WHERE v.id = e.ref_id
	    AND v.organization_id = e.organization_id AND v.deleted = TRUE)`

// dashboardHoney: this year's total and per-month history from the HARVEST fact rows.
func dashboardHoney(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	harvests, err := queryMaps(ctx, q.db, `SELECT e.date, e.amount_kg`+countedHarvests, q.org, evHarvest)
	if err != nil {
		return err
	}
	months := map[[2]int]float64{}
	for _, r := range harvests {
		d, ok := rtime(r["date"])
		if !ok {
			continue
		}
		kg := rf(r["amount_kg"])
		if d.Year() == q.now.Year() {
			res.HoneyKgSeason += kg
		}
		months[[2]int{d.Year(), int(d.Month())}] += kg
	}
	for k, kg := range months {
		res.HoneyHistory = append(res.HoneyHistory, &wv1.HoneyPerMonth{Year: i32(int64(k[0])), Month: i32(int64(k[1])), Kg: kg})
	}
	sort.Slice(res.HoneyHistory, func(i, j int) bool {
		a, b := res.HoneyHistory[i], res.HoneyHistory[j]
		return a.Year < b.Year || (a.Year == b.Year && a.Month < b.Month)
	})
	return nil
}

// callerZone reads DashboardRequest.time_zone: empty (UTC), an IANA name,
// or a fixed offset "+HH:MM" / "-HH:MM".
func callerZone(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	if len(name) == 6 && (name[0] == '+' || name[0] == '-') && name[3] == ':' {
		h, errH := strconv.Atoi(name[1:3])
		m, errM := strconv.Atoi(name[4:])
		if errH == nil && errM == nil && h <= 14 && m < 60 {
			secs := (h*60 + m) * 60
			if name[0] == '-' {
				secs = -secs
			}
			return time.FixedZone(name, secs), nil
		}
	} else if loc, err := time.LoadLocation(name); err == nil && name != "Local" {
		return loc, nil
	}
	return nil, fmt.Errorf("time_zone must be an IANA time zone name such as Europe/Berlin or an offset such as +02:00, not %q", name)
}

// calendarDaysSince counts whole calendar days from d to now in loc, never
// fewer than 0: a visit on the previous day is 1 day ago at any hour. This
// is the app's rule: it counts in local time, stores visits at local noon,
// and reads a value at exactly UTC midnight (older apps, imports) as that
// date.
func calendarDaysSince(d, now time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	date := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }
	visit := date(d.In(loc).Date())
	if u := d.UTC(); u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0 {
		visit = date(u.Date())
	}
	return max(0, int(date(now.In(loc).Date()).Sub(visit).Hours()/24))
}

// dashboardDue: days since the last visit per hive (never = -1), hives at or
// past the threshold. Sensor readings are not visits and do not count. Lost
// and dissolved colonies cannot be inspected any more and are left out, as
// in the app's own list.
func dashboardDue(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	hives, err := queryMaps(ctx, q.db, `SELECT h.id, h.name, a.name AS apiary_name
		FROM hive h LEFT JOIN apiary a ON a.id = h.apiary_id AND a.organization_id = h.organization_id
		WHERE h.organization_id = ? AND h.deleted = FALSE AND h.status NOT IN (?, ?) ORDER BY h.name, h.id`,
		q.org, int32(wv1.HiveStatus_HIVE_STATUS_LOST), int32(wv1.HiveStatus_HIVE_STATUS_DISSOLVED))
	if err != nil {
		return err
	}
	last, err := queryMaps(ctx, q.db, `SELECT hive_id, `+tsMax(q.db.DriverName(), "date")+` AS last_date FROM inspection
		WHERE organization_id = ? AND deleted = FALSE AND source <> ? GROUP BY hive_id`, q.org, sourceSensor)
	if err != nil {
		return err
	}
	lastByHive := map[string]time.Time{}
	for _, r := range last {
		if d, ok := rtime(r["last_date"]); ok {
			lastByHive[rs(r["hive_id"])] = d
		}
	}
	for _, h := range hives {
		days := int32(-1)
		if d, ok := lastByHive[rs(h["id"])]; ok {
			days = i32(int64(calendarDaysSince(d, q.now, q.loc)))
			if days < i32(int64(q.threshold)) {
				continue
			}
		}
		res.DueInspections = append(res.DueInspections, &wv1.DueInspection{
			HiveId: rs(h["id"]), HiveName: rs(h["name"]), ApiaryName: rs(h["apiary_name"]), DaysSinceLast: days,
		})
	}
	sort.SliceStable(res.DueInspections, func(i, j int) bool {
		a, b := res.DueInspections[i].DaysSinceLast, res.DueInspections[j].DaysSinceLast
		if a < 0 || b < 0 {
			return a < 0 && b >= 0 // never inspected first
		}
		return a > b
	})
	return nil
}

// dashboardRecent: the ten latest visits (sensor readings are left out).
func dashboardRecent(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	recent, err := queryMaps(ctx, q.db, `SELECT * FROM inspection
		WHERE organization_id = ? AND deleted = FALSE AND source <> ?
		ORDER BY `+visitOrder(q.db.DriverName())+` LIMIT 10`, q.org, sourceSensor)
	if err != nil {
		return err
	}
	for _, r := range recent {
		res.RecentInspections = append(res.RecentInspections, inspectionFromRow(r))
	}
	return attachInspectionPhotos(ctx, q.db, q.org, res.RecentInspections)
}

func (s *StatsService) GetHoneyStats(ctx context.Context, req *wv1.HoneyStatsRequest) (*wv1.HoneyStatsResponse, error) {
	id, _ := auth.FromContext(ctx)
	db := s.w.db
	rows, err := queryMaps(ctx, db, `SELECT e.date, e.amount_kg, e.queen_id, e.apiary_id`+countedHarvests,
		id.OrgID, evHarvest)
	if err != nil {
		return nil, writeErr(err)
	}
	perQueen, perApiary, perYear := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, r := range rows {
		d, ok := rtime(r["date"])
		if !ok {
			continue
		}
		if req.Year != 0 && i32(int64(d.Year())) != req.Year {
			continue
		}
		kg := rf(r["amount_kg"])
		perYear[itoa(int64(d.Year()))] += kg
		if q := rs(r["queen_id"]); q != "" {
			perQueen[q] += kg
		}
		if a := rs(r["apiary_id"]); a != "" {
			perApiary[a] += kg
		}
	}

	apiaryNames := map[string]string{}
	if arows, err := queryMaps(ctx, db, `SELECT id, name FROM apiary WHERE organization_id = ?`, id.OrgID); err == nil {
		for _, r := range arows {
			apiaryNames[rs(r["id"])] = rs(r["name"])
		}
	}
	queenNames := map[string]string{}
	if qrows, err := queryMaps(ctx, db, `SELECT id, year, breeder_number FROM queen WHERE organization_id = ?`, id.OrgID); err == nil {
		for _, r := range qrows {
			label := "Queen " + itoa(ri(r["year"]))
			if bn := rs(r["breeder_number"]); bn != "" {
				label += " " + bn
			}
			queenNames[rs(r["id"])] = label
		}
	}
	res := &wv1.HoneyStatsResponse{
		PerQueen:  sums(perQueen, queenNames),
		PerApiary: sums(perApiary, apiaryNames),
		PerYear:   sums(perYear, nil),
	}
	return res, nil
}

// sums turns a key -> kg map into a list sorted by kg descending (years ascending).
func sums(m map[string]float64, labels map[string]string) []*wv1.HoneySum {
	out := make([]*wv1.HoneySum, 0, len(m))
	for k, kg := range m {
		label := k
		if labels != nil {
			if l, ok := labels[k]; ok {
				label = l
			}
		}
		out = append(out, &wv1.HoneySum{Key: k, Label: label, Kg: kg})
	}
	sort.Slice(out, func(i, j int) bool {
		if labels == nil {
			return out[i].Key < out[j].Key
		}
		return out[i].Kg > out[j].Kg
	})
	return out
}

func count32(ctx context.Context, q queryer, query string, args ...any) (int32, error) {
	n, err := countRows(ctx, q, query, args...)
	return i32(int64(n)), err
}
