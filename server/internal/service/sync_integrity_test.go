package service

import (
	"testing"
	"time"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// A row may carry a stamp from a device whose clock was years ahead, stored
// before such stamps were refused. An API write to that row must not drag
// the server clock, which stamps every later server write, along with it.
func TestAPIWriteIgnoresStoredFarFutureStamp(t *testing.T) {
	f := newAPIFixture(t)
	ctx := identityCtx("user-a", "org-a")
	future := wsync.Format(time.Now().AddDate(3, 0, 0).UnixMilli(), 0, "olddevice")
	f.db.MustExec(f.db.Rebind(`UPDATE apiary SET field_hlc = ? WHERE id = 'apiary-a'`), `{"note":"`+future+`"}`)

	if _, err := f.apiary.UpdateApiary(ctx, &wv1.UpdateApiaryRequest{Id: "apiary-a", Name: "y", UpdateMask: mask("name")}); err != nil {
		t.Fatal(err)
	}
	var fc string
	if err := f.db.Get(&fc, `SELECT field_hlc FROM apiary WHERE id = 'apiary-a'`); err != nil {
		t.Fatal(err)
	}
	if ahead, _ := wsync.AheadBy(wsync.ParseFieldClock(fc)["name"], time.Now()); ahead > wsync.MaxDrift {
		t.Fatalf("the write was stamped %s ahead", ahead.Round(time.Hour))
	}
	if ahead, _ := wsync.AheadBy(f.writer.hlc.Now(), time.Now()); ahead > wsync.MaxDrift {
		t.Fatalf("the server clock runs %s ahead", ahead.Round(time.Hour))
	}
}
