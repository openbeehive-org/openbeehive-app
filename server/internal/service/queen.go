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
		qid, err := t.setQueen(m.HiveId, rs(h["apiary_id"]), m.Year, int32(m.Marking),
			m.Origin, m.BreederNumber, m.Note, tsOrNow(m.IntroducedAt))
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
	rows, err := queryMaps(ctx, s.w.db, `SELECT *`+where+` ORDER BY introduced_at DESC LIMIT ? OFFSET ?`,
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

func (s *QueenService) UpdateQueen(ctx context.Context, req *wv1.UpdateQueenRequest) (*wv1.UpdateQueenResponse, error) {
	id, _ := auth.FromContext(ctx)
	q := req.Queen
	if q == nil || q.Id == "" {
		return nil, invalid("queen.id is required")
	}
	var out map[string]any
	err := s.w.Run(ctx, id, func(t *txWriter) error {
		cur, err := t.row("queen", q.Id)
		if err != nil {
			return err
		}
		h, err := t.hive(rs(cur["hive_id"]))
		if err != nil {
			return err
		}
		marking := int32(q.Marking)
		if marking == 0 {
			marking = markingForYear(q.Year)
		}
		fields := map[string]any{
			"year": q.Year, "marking": marking, "origin": q.Origin, "breeder_number": q.BreederNumber,
			"note": q.Note, "active": b2i(q.Active), "updated_at": jsTimeNow(),
		}
		if q.IntroducedAt != nil {
			fields["introduced_at"] = tsOrEmpty(q.IntroducedAt)
		}
		if q.ReplacedAt != nil {
			fields["replaced_at"] = tsOrEmpty(q.ReplacedAt)
		} else if q.Active {
			fields["replaced_at"] = nil
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
		return t.del("queen", req.Id, rs(h["apiary_id"]))
	})
	if err != nil {
		return nil, writeErr(err)
	}
	return &wv1.DeleteQueenResponse{}, nil
}
