package service

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

func day(y int, m time.Month, d int) *timestamppb.Timestamp {
	return timestamppb.New(time.Date(y, m, d, 12, 0, 0, 0, time.UTC))
}

func TestListRPCsPaginate(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	// Three rows of each kind (the fixture already holds apiary-a and hive-a).
	for i := 0; i < 2; i++ {
		if _, err := f.apiary.CreateApiary(ctx, &wv1.CreateApiaryRequest{Name: "P" + itoa(int64(i))}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.hive.CreateHive(ctx, &wv1.CreateHiveRequest{ApiaryId: "apiary-a", Name: "P" + itoa(int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		d := day(2026, 5, 1+i)
		if _, err := f.queen.CreateQueen(ctx, &wv1.CreateQueenRequest{HiveId: "hive-a", Year: 2026, IntroducedAt: d}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.task.CreateTask(ctx, &wv1.CreateTaskRequest{Title: "t", HiveId: "hive-a", DueAt: d}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.treat.CreateTreatment(ctx, &wv1.CreateTreatmentRequest{HiveId: "hive-a", Product: "p", Date: d}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.harv.CreateHarvest(ctx, &wv1.CreateHarvestRequest{HiveId: "hive-a", AmountKg: 1, Date: d}); err != nil {
			t.Fatal(err)
		}
	}
	type page struct {
		rows int
		info *wv1.PageResponse
	}
	lists := map[string]func(p *wv1.PageRequest) (page, error){
		"apiaries": func(p *wv1.PageRequest) (page, error) {
			r, err := f.apiary.ListApiaries(ctx, &wv1.ListApiariesRequest{Page: p})
			return page{len(r.GetApiaries()), r.GetPage()}, err
		},
		"hives": func(p *wv1.PageRequest) (page, error) {
			r, err := f.hive.ListHives(ctx, &wv1.ListHivesRequest{Page: p, ApiaryId: "apiary-a"})
			return page{len(r.GetHives()), r.GetPage()}, err
		},
		"queens": func(p *wv1.PageRequest) (page, error) {
			r, err := f.queen.ListQueens(ctx, &wv1.ListQueensRequest{Page: p})
			return page{len(r.GetQueens()), r.GetPage()}, err
		},
		"tasks": func(p *wv1.PageRequest) (page, error) {
			r, err := f.task.ListTasks(ctx, &wv1.ListTasksRequest{Page: p})
			return page{len(r.GetTasks()), r.GetPage()}, err
		},
		"treatments": func(p *wv1.PageRequest) (page, error) {
			r, err := f.treat.ListTreatments(ctx, &wv1.ListTreatmentsRequest{Page: p})
			return page{len(r.GetTreatments()), r.GetPage()}, err
		},
		"harvests": func(p *wv1.PageRequest) (page, error) {
			r, err := f.harv.ListHarvests(ctx, &wv1.ListHarvestsRequest{Page: p})
			return page{len(r.GetHarvests()), r.GetPage()}, err
		},
	}
	for name, list := range lists {
		first, err := list(&wv1.PageRequest{PageSize: 2})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if first.rows != 2 || first.info.Total != 3 || first.info.NextPageToken != "2" {
			t.Fatalf("%s page 1: %d rows, total %d, next %q", name, first.rows, first.info.Total, first.info.NextPageToken)
		}
		second, err := list(&wv1.PageRequest{PageSize: 2, PageToken: first.info.NextPageToken})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if second.rows != 1 || second.info.Total != 3 || second.info.NextPageToken != "" {
			t.Fatalf("%s page 2: %d rows, total %d, next %q", name, second.rows, second.info.Total, second.info.NextPageToken)
		}
	}
}
