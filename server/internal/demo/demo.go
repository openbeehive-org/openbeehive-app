// Package demo installs a self-contained, read-mostly demo tenant for showcasing
// and testing. It is off by default and enabled with BEEHIVE_DEMO=true.
//
// The demo account (demo@app.openbeehive.org / demo) owns one tenant with 15
// hives across 4 apiaries and a season of realistic inspections. The data is
// re-seeded every hour so the showcase always looks the same.
//
// Data is written as sync change-log entries (the path the offline-first app
// pulls from), scoped to the demo apiaries, plus the apiary rows themselves so
// the scope resolves for the demo user.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

const (
	OrgID  = "demo-tenant"
	UserID = "demo-user"
)

type Seeder struct {
	db  *sqlx.DB
	hlc *wsync.HLC
	cfg *config.Config
}

func New(db *sqlx.DB, hlc *wsync.HLC, cfg *config.Config) *Seeder {
	return &Seeder{db: db, hlc: hlc, cfg: cfg}
}

// Install ensures the demo user/tenant exist, seeds the data once, and starts an
// hourly reset loop. Call once at startup when the demo is enabled.
func (s *Seeder) Install(ctx context.Context) error {
	if err := s.ensureAccount(ctx); err != nil {
		return err
	}
	if err := s.Seed(ctx); err != nil {
		return err
	}
	go s.resetLoop(ctx)
	log.Printf("demo: installed (%s / %s) — 15 hives, 4 apiaries, reset hourly", s.cfg.Demo.Email, "********")
	return nil
}

func (s *Seeder) resetLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Seed(ctx); err != nil {
				log.Printf("demo: hourly reset failed: %v", err)
			} else {
				log.Printf("demo: reset")
			}
		}
	}
}

func (s *Seeder) ensureAccount(ctx context.Context) error {
	hash, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.Demo.Password), bcrypt.DefaultCost)
	now := time.Now().UTC()
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM users WHERE id = ?`, []any{UserID}},
		{`INSERT INTO users (id, email, name, oidc_subject, password_hash, role, email_verified, verification_token, created_at)
		  VALUES (?, ?, ?, '', ?, 'user', TRUE, '', ?)`, []any{UserID, s.cfg.Demo.Email, "Demo Beekeeper", string(hash), now}},
		{`DELETE FROM organization WHERE id = ?`, []any{OrgID}},
		{`INSERT INTO organization (id, name, created_at) VALUES (?, 'Demo apiaries', ?)`, []any{OrgID, now}},
		{`DELETE FROM member WHERE organization_id = ?`, []any{OrgID}},
		{`INSERT INTO member (organization_id, benutzer_id, role) VALUES (?, ?, 'owner')`, []any{OrgID, UserID}},
	}
	for _, st := range stmts {
		if _, err := s.db.ExecContext(ctx, s.db.Rebind(st.q), st.args...); err != nil {
			return fmt.Errorf("demo account: %w", err)
		}
	}
	return nil
}

// Seed wipes and rebuilds the demo data (idempotent; stable ids).
func (s *Seeder) Seed(ctx context.Context) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Wipe previous demo data.
	if _, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM change_log WHERE org_id = ?`), OrgID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`DELETE FROM apiary WHERE organization_id = ?`), OrgID); err != nil {
		return err
	}

	now := time.Now().UTC()
	apiaries := []struct {
		name, addr string
		lat, lng   float64
	}{
		{"Orchard Meadow", "Apfelweg 3, 79341 Kenzingen", 48.193, 7.770},
		{"Forest Edge", "Waldrand 12, 79261 Gutach", 48.108, 8.022},
		{"Village Rooftops", "Hauptstr. 5, 79312 Emmendingen", 48.121, 7.847},
		{"River Pastures", "Auwiesen 1, 79331 Teningen", 48.130, 7.811},
	}
	// 15 hives distributed 4/4/4/3 across the apiaries.
	perApiary := []int{4, 4, 4, 3}
	hiveTypes := []int{1, 2, 4, 3, 5} // Zander, Dadant, Langstroth, Deutsch Normal, Warre
	hiveNo := 0

	for ai, a := range apiaries {
		apID := fmt.Sprintf("demo-apiary-%d", ai+1)
		af := map[string]any{
			"organization_id": OrgID, "name": a.name, "address": a.addr,
			"lat": a.lat, "lng": a.lng, "note": "", "created_at": iso(now), "updated_at": iso(now), "deleted": 0,
		}
		// Apiary needs a real row so the demo user's scopes resolve.
		if _, err := tx.ExecContext(ctx, tx.Rebind(
			`INSERT INTO apiary (id, organization_id, name, address, lat, lng, note, created_at, updated_at, field_hlc, deleted)
			 VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, '{}', FALSE)`),
			apID, OrgID, a.name, a.addr, a.lat, a.lng, now, now); err != nil {
			return err
		}
		if err := s.put(ctx, tx, "apiary", apID, apID, af); err != nil {
			return err
		}

		for h := 0; h < perApiary[ai]; h++ {
			hiveNo++
			if err := s.seedHive(ctx, tx, apID, hiveNo, hiveTypes, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// seedHive writes one hive with its queen, inspections, harvest and treatments.
func (s *Seeder) seedHive(ctx context.Context, tx *sqlx.Tx, apID string, hiveNo int, hiveTypes []int, now time.Time) error {
	hvID := fmt.Sprintf("demo-hive-%d", hiveNo)
	year := 2023 + (hiveNo % 3)
	marking := ((year%10)+4)%5 + 1
	if err := s.put(ctx, tx, "hive", apID, hvID, map[string]any{
		"organization_id": OrgID, "apiary_id": apID, "name": fmt.Sprintf("Hive %d", hiveNo),
		"type": hiveTypes[hiveNo%len(hiveTypes)], "status": 1, "boxes": 2 + hiveNo%3,
		"colony_origin": pick(hiveNo, "swarm 2024", "split from Hive 2", "own rearing", "nucleus"),
		"note":          "", "created_at": iso(now.AddDate(-1, 0, 0)), "updated_at": iso(now), "deleted": 0,
	}); err != nil {
		return err
	}
	// Current queen.
	qID := fmt.Sprintf("demo-queen-%d", hiveNo)
	if err := s.put(ctx, tx, "queen", apID, qID, map[string]any{
		"organization_id": OrgID, "hive_id": hvID, "year": year, "marking": marking,
		"origin": pick(hiveNo, "Buckfast", "Carnica", "own rearing"), "breeder_number": fmt.Sprintf("%d", 10+hiveNo),
		"introduced_at": iso(mostRecent(now, time.May, 10).AddDate(-(hiveNo % 2), 0, 0)), "replaced_at": nil,
		"active": 1, "note": "", "created_at": iso(now), "updated_at": iso(now), "deleted": 0,
	}); err != nil {
		return err
	}
	// Inspections over the trailing ~10 months, densest recently, so the
	// demo always shows recent activity (it re-seeds hourly).
	for v, off := range []int{4, 20, 45, 80, 130, 195, 280} {
		date := now.AddDate(0, 0, -(off + hiveNo%6))
		inspID := fmt.Sprintf("demo-insp-%d-%d", hiveNo, v)
		if err := s.put(ctx, tx, "inspection", apID, inspID, inspectionFields(hvID, date, hiveNo, v)); err != nil {
			return err
		}
	}
	// Last summer's honey harvest (most recent July at or before now).
	hd := mostRecent(now, time.July, 12+hiveNo%6)
	if err := s.put(ctx, tx, "harvest", apID, fmt.Sprintf("demo-harv-%d", hiveNo), map[string]any{
		"organization_id": OrgID, "apiary_id": apID, "hive_id": hvID, "queen_id": qID,
		"date": iso(hd), "variety": pick(hiveNo, "spring blossom", "summer flow", "forest"),
		"amount_kg": round1(10 + float64(hiveNo%8)), "water_content": round1(17 + float64(hiveNo%3)),
		"batch_number": fmt.Sprintf("B%d-%d", hd.Year(), hiveNo), "best_before": iso(hd.AddDate(2, 0, 0)),
		"note": "", "deleted": 0,
	}); err != nil {
		return err
	}
	// Varroa control: late-summer formic acid + midwinter oxalic acid.
	fa := mostRecent(now, time.August, 18)
	if err := s.put(ctx, tx, "treatment", apID, fmt.Sprintf("demo-treat-fa-%d", hiveNo), map[string]any{
		"organization_id": OrgID, "apiary_id": apID, "hive_id": hvID, "queen_id": qID,
		"date": iso(fa), "product": "Formic acid 60%", "active_ingredient": "formic acid",
		"dose": "30 ml", "method": "evaporation", "batch_number": fmt.Sprintf("FA-%d", hiveNo),
		"withdrawal_until": iso(fa.AddDate(0, 0, 14)), "reason": "varroa", "note": "", "deleted": 0,
	}); err != nil {
		return err
	}
	ox := mostRecent(now, time.December, 27)
	if err := s.put(ctx, tx, "treatment", apID, fmt.Sprintf("demo-treat-ox-%d", hiveNo), map[string]any{
		"organization_id": OrgID, "apiary_id": apID, "hive_id": hvID, "queen_id": qID,
		"date": iso(ox), "product": "Oxuvar 5.7%", "active_ingredient": "oxalic acid",
		"dose": "50 ml", "method": "trickling", "batch_number": fmt.Sprintf("OX-%d", hiveNo),
		"withdrawal_until": "", "reason": "varroa", "note": "broodless winter treatment", "deleted": 0,
	}); err != nil {
		return err
	}
	return nil
}

// mostRecent returns the most recent occurrence of month/day at or before `now`
// (this year, or last year if it hasn't happened yet this year).
func mostRecent(now time.Time, month time.Month, day int) time.Time {
	d := time.Date(now.Year(), month, day, 10, 0, 0, 0, time.UTC)
	if d.After(now) {
		d = d.AddDate(-1, 0, 0)
	}
	return d
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// inspectionFields builds a season-appropriate inspection for a given date.
// Activities are only recorded when they actually make sense for that time of
// year — no feeding in spring, no supering in winter, etc. (Northern hemisphere.)
func inspectionFields(hiveID string, date time.Time, hiveNo, idx int) map[string]any {
	m := int(date.Month())
	spring := m >= 3 && m <= 5
	summer := m >= 6 && m <= 8
	autumn := m >= 9 && m <= 10
	winter := m == 11 || m == 12 || m <= 2

	outByMonth := []float64{2, 3, 8, 13, 17, 21, 23, 22, 17, 12, 6, 3} // rough Central-European climate
	outTemp := outByMonth[m-1] + float64(hiveNo%4) - 1.5
	hiveTemp := 34.8 // brood-nest set point
	if winter {
		hiveTemp = 23 + float64(hiveNo%5) // winter cluster, no brood rearing
	}

	brood := 0
	switch {
	case spring:
		brood = 3 + idx%4
	case summer:
		brood = 7 + hiveNo%3
	case autumn:
		brood = 2 + hiveNo%2
	}
	frames := brood + 3 + hiveNo%3
	if winter {
		frames = 6 + hiveNo%3
	}
	varroaByMonth := []int{0, 0, 1, 1, 2, 3, 4, 6, 5, 2, 1, 0} // peaks late summer
	stores := 1                                                // 1=good
	if spring {
		stores = 2 // medium after winter
	}

	f := map[string]any{
		"organization_id": OrgID, "hive_id": hiveID, "date": iso(date),
		"weather":       weatherFor(m, idx),
		"queen_seen":    boolInt(!winter && idx%2 == 0),
		"eggs_seen":     boolInt(!winter),
		"covered_larva": boolInt(!winter),
		"temperament":   1 + (hiveNo+idx)%4, "calmness": 1 + (hiveNo+idx)%4,
		"frames": frames, "brood_frames": brood, "stores": stores, "queen_cells": 0,
		"varroa":    fmt.Sprintf("%d mites/day", varroaByMonth[m-1]+hiveNo%2),
		"temp_hive": round1(hiveTemp), "temp_outside": round1(outTemp),
		"humidity_hive": 55 + (hiveNo+idx)%8, "humidity_outside": 60 + (hiveNo+idx)%25,
		"note": "", "created_at": iso(date), "deleted": 0,
	}
	switch {
	case spring:
		f["queen_cells"] = idx % 3             // swarm season
		f["drone_frame_cut"] = boolInt(m >= 4) // varroa drone-brood removal
		f["super_added"] = boolInt(m >= 4)     // give room for the flow
		f["frames_added"] = 1 + idx%2
		f["weight_kg"] = round1(34 + float64(hiveNo%5))
	case summer:
		f["super_added"] = boolInt(m == 6)
		if m == 7 {
			f["honey_kg"] = round1(10 + float64(hiveNo%8)) // honey taken on this visit
		}
		f["weight_kg"] = round1(42 + float64(hiveNo%7))
	case autumn:
		f["fed_kg"] = round1(3 + float64(hiveNo%3)) // build winter stores
		f["frames_removed"] = 1
		f["weight_kg"] = round1(35 + float64(hiveNo%5))
	case winter:
		if m == 1 || m == 2 {
			f["fed_kg"] = 1.0 // emergency fondant only
		}
		f["weight_kg"] = round1(30 + float64(hiveNo%4))
	}
	return f
}

func weatherFor(m, idx int) string {
	switch {
	case m == 12 || m <= 2:
		return pick(idx, "cold, 1°C", "frosty, -3°C", "grey, 4°C")
	case m >= 3 && m <= 5:
		return pick(idx, "sunny, 16°C", "mild, 14°C", "warm, light breeze")
	case m >= 6 && m <= 8:
		return pick(idx, "warm, 26°C", "hot, 30°C", "sunny, 24°C")
	default:
		return pick(idx, "mild, 12°C", "overcast, 10°C", "crisp, 8°C")
	}
}

// put writes a create change-log entry for an entity, scoped to scopeID.
func (s *Seeder) put(ctx context.Context, tx *sqlx.Tx, entity, scopeID, id string, fields map[string]any) error {
	var seq int64
	if _, err := tx.ExecContext(ctx, "UPDATE seq_counter SET val = val + 1 WHERE name = 'change'"); err != nil {
		return err
	}
	if err := tx.GetContext(ctx, &seq, "SELECT val FROM seq_counter WHERE name = 'change'"); err != nil {
		return err
	}
	payload, _ := json.Marshal(fields)
	_, err := tx.ExecContext(ctx, tx.Rebind(
		`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`),
		fmt.Sprintf("demo-cl-%d", seq), seq, scopeID, entity, id, string(payload), s.hlc.Now(), UserID, OrgID)
	return err
}

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func pick(i int, opts ...string) string { return opts[i%len(opts)] }
