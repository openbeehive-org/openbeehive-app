package service

import (
	"context"
	"strings"
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

// The photo set holds data URLs only. An element named like a built-in
// property of a JavaScript object would break the merge on apps that keep
// sets in plain objects, and with it every pull of the workspace.
func TestPhotoKeysAcceptOnlyDataURLs(t *testing.T) {
	svc := newSyncFixture(t)
	insp := `{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","created_at":"2026-07-11T10:00:00Z","deleted":0}`
	if err := push(svc, legacyA, change("inspection", "in-1", "apiary-a", insp)); err != nil {
		t.Fatal(err)
	}
	future := wsync.Format(time.Now().Add(time.Hour).UnixMilli(), 0, "dev")
	for _, ctx := range []context.Context{legacyA, ctxA} {
		res := pushRes(t, svc, ctx,
			change("inspection", "in-1", "apiary-a", `{"photo_keys":{"add":["constructor"]}}`),
			change("inspection", "in-1", "apiary-a", `{"photo_keys":{"add":["__proto__"]}}`),
			change("inspection", "in-1", "apiary-a", `{"photo_keys":{"remove":["toString"],"removed_tags":{}}}`),
			change("inspection", "in-1", "apiary-a", `{"photo_keys":{"remove":["data:x"],"removed_tags":{"valueOf":[]}}}`),
			change("inspection", "in-1", "apiary-a", `{"photo_keys":{"remove":["data:x"],"removed_tags":{"data:x":["`+future+`"]}}}`),
			change("inspection", "in-1", "apiary-a", `{"note":"still fine"}`),
		)
		wantRejected(t, res, map[int32]string{0: rejInvalid, 1: rejInvalid, 2: rejInvalid, 3: rejInvalid, 4: rejInvalid})
	}
}

// Elements of another kind stored before that rule never reach a device.
func TestStoredForeignPhotoKeysAreNotSent(t *testing.T) {
	svc := newSyncFixture(t)
	insp := `{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","created_at":"2026-07-11T10:00:00Z","deleted":0}`
	if err := push(svc, legacyA, change("inspection", "in-1", "apiary-a", insp),
		change("inspection", "in-1", "apiary-a", `{"note":"n","photo_keys":{"add":["data:image/png;base64,AA"]}}`),
		change("inspection", "in-1", "apiary-a", `{"photo_keys":{"add":["data:image/png;base64,BB"]}}`)); err != nil {
		t.Fatal(err)
	}
	svc.db.MustExec(`UPDATE change_log SET payload = REPLACE(payload, 'data:image/png;base64,AA', 'constructor')`)
	svc.db.MustExec(`UPDATE change_log SET payload = REPLACE(payload, 'data:image/png;base64,BB', '__proto__')`)

	old := pullAll(t, svc, legacyA)
	for _, c := range old {
		if strings.Contains(c.PayloadJson, "constructor") || strings.Contains(c.PayloadJson, "__proto__") {
			t.Fatalf("a 0.4.x app was sent %s", c.PayloadJson)
		}
	}
	if len(changesFor(old, "inspection")) != 2 {
		t.Fatalf("want the row and the note, got %v", old)
	}
	for _, c := range pullAll(t, svc, ctxA) {
		if strings.Contains(c.PayloadJson, "photo_keys") {
			t.Fatalf("a current app was sent %s", c.PayloadJson)
		}
	}
}
