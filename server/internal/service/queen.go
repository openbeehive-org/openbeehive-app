package service

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	"github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1/openbeehivev1connect"
)

type QueenService struct {
	openbeehivev1connect.UnimplementedQueenServiceHandler
	w *Writer
}

func NewQueenService(w *Writer) *QueenService { return &QueenService{w: w} }

func (s *QueenService) CreateQueen(ctx context.Context, req *wv1.CreateQueenRequest) (*wv1.CreateQueenResponse, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "")
	}
	m := req
	if m.HiveId == "" {
		return nil, invalid("hive_id is required")
	}
	if m.Year <= 0 {
		return nil, invalid("year is required")
	}
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		h, err := t.hive(m.HiveId)
		if err != nil {
			return err
		}
		qid, err := t.setQueen(queenInput{
			hiveID: m.HiveId, scope: rs(h["apiary_id"]), year: m.Year, marking: int32(m.Marking),
			origin: m.Origin, breeder: m.BreederNumber, note: m.Note, at: tsOrNow(m.IntroducedAt),
		})
		if err != nil {
			return err
		}
		out, err = t.row("queen", qid)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.CreateQueenResponse{Queen: queenFromRow(out)}, nil
}

func (s *QueenService) ListQueens(ctx context.Context, req *wv1.ListQueensRequest) (*wv1.ListQueensResponse, error) {
	id, _ := auth.FromContext(ctx)
	limit, offset := pageParams(req.Page)
	where := ` FROM queen WHERE organization_id = ? AND deleted = FALSE`
	args := []any{id.OrgID}
	if req.HiveId != "" {
		where += ` AND hive_id = ?`
		args = append(args, req.HiveId)
	}
	if req.OnlyActive {
		where += ` AND active = TRUE`
	}
	total, err := countRows(ctx, s.w.db, `SELECT COUNT(*)`+where, args...)
	if err != nil {
		return nil, writeErr(err)
	}
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+latestFirst("introduced_at")+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, writeErr(err)
	}
	out := make([]*wv1.Queen, 0, len(rows))
	for _, r := range rows {
		out = append(out, queenFromRow(r))
	}
	return &wv1.ListQueensResponse{Queens: out, Page: pageResponse(total, offset, len(rows), limit)}, nil
}

// queenUpdatable lists the fields UpdateQueen may change.
var queenUpdatable = []string{"year", "marking", "origin", "breeder_number", "introduced_at", "replaced_at", "active", "note"}

// UpdateQueen changes only the fields sent (see UpdateQueenRequest).
func (s *QueenService) UpdateQueen(ctx context.Context, req *wv1.UpdateQueenRequest) (*wv1.UpdateQueenResponse, error) {
	id, _ := auth.FromContext(ctx)
	q := req.Queen
	if q == nil || q.Id == "" {
		return nil, invalid("queen.id is required")
	}
	fields, err := queenUpdate(req)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("queen", q.Id)
		if err != nil {
			return err
		}
		h, err := t.hive(rs(cur["hive_id"]))
		if err != nil {
			return err
		}
		if m, ok := fields["marking"]; ok && m == int32(0) {
			// An unspecified colour follows the (new or stored) year.
			year := i32(ri(cur["year"]))
			if y, ok := fields["year"].(int32); ok {
				year = y
			}
			fields["marking"] = markingForYear(year)
		}
		if err := t.upsert("queen", q.Id, rs(h["apiary_id"]), fields); err != nil {
			return err
		}
		out, err = t.row("queen", q.Id)
		return err
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.UpdateQueenResponse{Queen: queenFromRow(out)}, nil
}

// queenUpdate returns the payload of an UpdateQueen request.
func queenUpdate(req *wv1.UpdateQueenRequest) (map[string]any, error) {
	q := req.Queen
	fields, err := updatePayload(q.ProtoReflect(), req.UpdateMask, queenUpdatable)
	if err != nil {
		return nil, err
	}
	if err := requireValue(fields, "year"); err != nil {
		return nil, err
	}
	if len(req.UpdateMask.GetPaths()) == 0 {
		// Without a mask a false `active` cannot be told from "not sent", so
		// the end of the reign is the signal: sending replaced_at retires the
		// queen, sending active = true clears it.
		if q.Active && q.ReplacedAt == nil {
			fields["replaced_at"] = nil
		} else if !q.Active && q.ReplacedAt != nil {
			fields["active"] = 0
		}
	}
	fields["updated_at"] = jsTimeNow()
	return fields, nil
}

// DeleteQueen soft-deletes the queen and the timeline events of her reign
// (introduced, replaced). Records made during her reign keep her id.
func (s *QueenService) DeleteQueen(ctx context.Context, req *wv1.DeleteQueenRequest) (*wv1.DeleteQueenResponse, error) {
	id, _ := auth.FromContext(ctx)
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("queen", req.Id)
		if err != nil {
			return err
		}
		h, err := t.hive(rs(cur["hive_id"]))
		if err != nil {
			return err
		}
		scope := rs(h["apiary_id"])
		evs, err := queryMaps(ctx, t.tx, `SELECT id, scope_id FROM event
			WHERE organization_id = ? AND queen_id = ? AND type IN (?, ?) AND deleted = FALSE`,
			id.OrgID, req.Id, evQueenIntroduced, evQueenReplaced)
		if err != nil {
			return err
		}
		if err := t.del("queen", req.Id, scope); err != nil {
			return err
		}
		return t.delEvents(evs, scope)
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteQueenResponse{}, nil
}
