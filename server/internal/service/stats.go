package service

import (
	"context"
	"sort"
	"time"

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

const defaultDueDays = 21 // the app highlights hives unvisited for 21+ days

// dashboardQuery is what every dashboard figure is computed for: one
// member of one tenant at one point in time.
type dashboardQuery struct {
	db        queryer
	org, user string
	now       time.Time
	threshold int
}

func (s *StatsService) GetDashboard(ctx context.Context, req *wv1.DashboardRequest) (*wv1.DashboardResponse, error) {
	id, _ := auth.FromContext(ctx)
	q := dashboardQuery{db: s.w.db, org: id.OrgID, user: id.UserID, now: time.Now().UTC(), threshold: int(req.ThresholdDays)}
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
		WHERE organization_id = ? AND deleted = FALSE AND done = FALSE`+visibleTasks+tasksInOrder,
		q.org, personalScope(q.user))
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

// dashboardHoney: this year's total and per-month history from the HARVEST fact rows.
func dashboardHoney(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	harvests, err := queryMaps(ctx, q.db, `SELECT date, amount_kg FROM event WHERE organization_id = ? AND deleted = FALSE AND type = ?`, q.org, evHarvest)
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

// dashboardDue: days since the last visit per hive (never = -1), hives at or past the threshold.
func dashboardDue(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	hives, err := queryMaps(ctx, q.db, `SELECT h.id, h.name, a.name AS apiary_name
		FROM hive h LEFT JOIN apiary a ON a.id = h.apiary_id AND a.organization_id = h.organization_id
		WHERE h.organization_id = ? AND h.deleted = FALSE ORDER BY h.name, h.id`, q.org)
	if err != nil {
		return err
	}
	last, err := queryMaps(ctx, q.db, `SELECT hive_id, MAX(date) AS last_date FROM inspection
		WHERE organization_id = ? AND deleted = FALSE GROUP BY hive_id`, q.org)
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
			days = i32(int64(q.now.Sub(d).Hours() / 24))
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

// dashboardRecent: the ten latest inspections.
func dashboardRecent(ctx context.Context, q dashboardQuery, res *wv1.DashboardResponse) error {
	recent, err := queryMaps(ctx, q.db, `SELECT * FROM inspection
		WHERE organization_id = ? AND deleted = FALSE
		ORDER BY date DESC, id DESC LIMIT 10`, q.org)
	if err != nil {
		return err
	}
	for _, r := range recent {
		res.RecentInspections = append(res.RecentInspections, inspectionFromRow(r))
	}
	return nil
}

func (s *StatsService) GetHoneyStats(ctx context.Context, req *wv1.HoneyStatsRequest) (*wv1.HoneyStatsResponse, error) {
	id, _ := auth.FromContext(ctx)
	db := s.w.db
	rows, err := queryMaps(ctx, db, `SELECT date, amount_kg, queen_id, apiary_id FROM event
		WHERE organization_id = ? AND deleted = FALSE AND type = ?`, id.OrgID, evHarvest)
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
