package service

import (
	"context"
	"sort"
	"time"

	"connectrpc.com/connect"

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

func (s *StatsService) GetDashboard(ctx context.Context, req *connect.Request[wv1.DashboardRequest]) (*connect.Response[wv1.DashboardResponse], error) {
	id, _ := auth.FromContext(ctx)
	db := s.w.db
	org := id.OrgID
	threshold := int(req.Msg.ThresholdDays)
	if threshold <= 0 {
		threshold = defaultDueDays
	}
	now := time.Now().UTC()
	res := &wv1.DashboardResponse{}

	var err error
	if res.Apiaries, err = count32(ctx, db, `SELECT COUNT(*) FROM apiary WHERE organization_id = ? AND deleted = FALSE`, org); err != nil {
		return nil, writeErr(err)
	}
	if res.Hives, err = count32(ctx, db, `SELECT COUNT(*) FROM hive WHERE organization_id = ? AND deleted = FALSE`, org); err != nil {
		return nil, writeErr(err)
	}
	if res.ActiveQueens, err = count32(ctx, db, `SELECT COUNT(*) FROM queen WHERE organization_id = ? AND deleted = FALSE AND active = TRUE`, org); err != nil {
		return nil, writeErr(err)
	}
	for _, fill := range []func(context.Context, queryer, string, time.Time, int, *wv1.DashboardResponse) error{
		dashboardTasks, dashboardHoney, dashboardDue, dashboardRecent,
	} {
		if err := fill(ctx, db, org, now, threshold, res); err != nil {
			return nil, writeErr(err)
		}
	}
	return connect.NewResponse(res), nil
}

// dashboardTasks: open, overdue, and the next five by due date.
func dashboardTasks(ctx context.Context, db queryer, org string, now time.Time, _ int, res *wv1.DashboardResponse) error {
	tasks, err := queryMaps(ctx, db, `SELECT * FROM task WHERE organization_id = ? AND deleted = FALSE AND done = FALSE ORDER BY due_at ASC`, org)
	if err != nil {
		return err
	}
	res.OpenTasks = i32(int64(len(tasks)))
	for _, r := range tasks {
		if due, ok := rtime(r["due_at"]); ok && due.Before(now) {
			res.OverdueTasks++
		}
		if len(res.NextTasks) < 5 {
			res.NextTasks = append(res.NextTasks, taskFromRow(r))
		}
	}
	return nil
}

// dashboardHoney: this year's total and per-month history from the HARVEST fact rows.
func dashboardHoney(ctx context.Context, db queryer, org string, now time.Time, _ int, res *wv1.DashboardResponse) error {
	harvests, err := queryMaps(ctx, db, `SELECT date, amount_kg FROM event WHERE organization_id = ? AND deleted = FALSE AND type = ?`, org, evHarvest)
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
		if d.Year() == now.Year() {
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
func dashboardDue(ctx context.Context, db queryer, org string, now time.Time, threshold int, res *wv1.DashboardResponse) error {
	hives, err := queryMaps(ctx, db, `SELECT h.id, h.name, a.name AS apiary_name
		FROM hive h LEFT JOIN apiary a ON a.id = h.apiary_id
		WHERE h.organization_id = ? AND h.deleted = FALSE ORDER BY h.name`, org)
	if err != nil {
		return err
	}
	last, err := queryMaps(ctx, db, `SELECT hive_id, MAX(date) AS last_date FROM inspection
		WHERE organization_id = ? AND deleted = FALSE GROUP BY hive_id`, org)
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
			days = i32(int64(now.Sub(d).Hours() / 24))
			if days < i32(int64(threshold)) {
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
func dashboardRecent(ctx context.Context, db queryer, org string, _ time.Time, _ int, res *wv1.DashboardResponse) error {
	recent, err := queryMaps(ctx, db, `SELECT * FROM inspection WHERE organization_id = ? AND deleted = FALSE ORDER BY date DESC LIMIT 10`, org)
	if err != nil {
		return err
	}
	for _, r := range recent {
		res.RecentInspections = append(res.RecentInspections, inspectionFromRow(r))
	}
	return nil
}

func (s *StatsService) GetHoneyStats(ctx context.Context, req *connect.Request[wv1.HoneyStatsRequest]) (*connect.Response[wv1.HoneyStatsResponse], error) {
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
		if req.Msg.Year != 0 && i32(int64(d.Year())) != req.Msg.Year {
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
	return connect.NewResponse(res), nil
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
