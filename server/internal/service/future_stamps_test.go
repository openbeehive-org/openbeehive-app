package service

import (
	"context"
	"testing"
	"time"

	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

func TestCountFutureStamps(t *testing.T) {
	svc := newSyncFixture(t)
	now := time.Now()
	far := wsync.Format(now.AddDate(2, 0, 0).UnixMilli(), 0, "olddevice")
	near := wsync.Format(now.Add(5*time.Minute).UnixMilli(), 0, "dev")
	past := wsync.Format(now.Add(-time.Hour).UnixMilli(), 0, "dev")
	svc.db.MustExec(svc.db.Rebind(`UPDATE apiary SET field_hlc = ? WHERE id = 'apiary-a'`),
		`{"name":"`+far+`","note":"`+far+`","address":"`+near+`"}`)
	svc.db.MustExec(svc.db.Rebind(`UPDATE apiary SET field_hlc = ? WHERE id = 'apiary-b'`), `{"name":"`+past+`"}`)
	svc.db.MustExec(svc.db.Rebind(`UPDATE hive SET field_hlc = ? WHERE id = 'hive-a'`), `{"name":"`+far+`","x":"garbage"}`)

	found, err := countFutureStamps(context.Background(), svc.db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found["apiary"].fields != 2 || found["hive"].fields != 1 {
		t.Fatalf("found %+v", found)
	}
	if rows := found["apiary"].rows; len(rows) != 1 || rows[0] != "apiary-a" {
		t.Fatalf("apiary rows %v", rows)
	}
	if err := ReportFutureStamps(context.Background(), svc.db); err != nil {
		t.Fatal(err)
	}
}
