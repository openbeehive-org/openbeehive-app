package service

import (
	"context"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
)

// Seeder writes prepared data through the shared write path: the same flows
// the API uses (rows, placements, frozen context, timeline events, change
// log), but with caller-chosen ids and dates. The rows a flow adds next to
// its main record get ids derived from that record's id, so seeding the same
// data again rewrites the same rows instead of adding copies on every device
// that already pulled them.
type Seeder struct{ t *txWriter }

// Seed runs fn in one transaction on behalf of id.
func (w *Writer) Seed(ctx context.Context, id auth.Identity, fn func(s *Seeder) error) error {
	return w.Run(ctx, id, func(t *txWriter) error {
		t.stableIDs = true
		return fn(&Seeder{t})
	})
}

// Wipe removes every synced row and change-log entry of the identity's
// tenant. It is meant for throwaway tenants that are rebuilt from scratch;
// devices are not told about the removed rows.
func (s *Seeder) Wipe() error {
	t := s.t
	for _, spec := range entityCols {
		if _, err := t.tx.ExecContext(t.ctx, t.tx.Rebind(
			"DELETE FROM "+spec.table+" WHERE organization_id = ?"), t.id.OrgID); err != nil {
			return err
		}
	}
	_, err := t.tx.ExecContext(t.ctx, t.tx.Rebind(`DELETE FROM change_log WHERE org_id = ?`), t.id.OrgID)
	return err
}

// SeedApiary, SeedHive and SeedQueen describe the rows to write.
type SeedApiary struct {
	ID, Name, Address string
	Lat, Lng          float64
	CreatedAt         time.Time
}

type SeedHive struct {
	ID, ApiaryID, Name string
	Type, Boxes        int32
	ColonyOrigin       string
	CreatedAt          time.Time
}

type SeedQueen struct {
	ID, HiveID            string
	Year                  int32
	Origin, BreederNumber string
	IntroducedAt          time.Time
}

func (s *Seeder) Apiary(a SeedApiary) error {
	return s.t.createApiary(a.ID, a.Name, a.Address, a.Lat, a.Lng, "", jsTime(a.CreatedAt))
}

// Hive writes the hive, its first placement and the CREATED event.
func (s *Seeder) Hive(h SeedHive) error {
	_, err := s.t.createHive(hiveInput{
		id: h.ID, apiaryID: h.ApiaryID, name: h.Name, typ: h.Type, boxes: h.Boxes,
		colonyOrigin: h.ColonyOrigin, at: jsTime(h.CreatedAt),
	})
	return err
}

// Queen introduces a queen into an existing hive.
func (s *Seeder) Queen(q SeedQueen) error {
	h, err := s.t.hive(q.HiveID)
	if err != nil {
		return err
	}
	_, err = s.t.setQueen(queenInput{
		id: q.ID, hiveID: q.HiveID, scope: rs(h["apiary_id"]), year: q.Year,
		origin: q.Origin, breeder: q.BreederNumber, at: jsTime(q.IntroducedAt),
	})
	return err
}

// Inspection, Harvest and Treatment write one record of an existing hive
// with its timeline event. fields holds the record's columns as in a sync
// payload; time.Time values are converted to the stored timestamp form.
func (s *Seeder) Inspection(id, hiveID string, date time.Time, fields map[string]any) error {
	_, err := s.t.recordInspection(id, hiveID, jsTime(date), seedFields(fields))
	return err
}

func (s *Seeder) Harvest(id, hiveID string, date time.Time, fields map[string]any) error {
	_, err := s.t.recordHarvest(id, hiveID, jsTime(date), seedFields(fields))
	return err
}

func (s *Seeder) Treatment(id, hiveID string, date time.Time, fields map[string]any) error {
	_, err := s.t.recordTreatment(id, hiveID, jsTime(date), seedFields(fields))
	return err
}

func seedFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+6)
	for k, v := range fields {
		if ts, ok := v.(time.Time); ok {
			v = jsTime(ts)
		}
		out[k] = v
	}
	return out
}
