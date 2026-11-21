package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

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

// The author of an event is whoever created it, not what the payload says.
func TestEventAuthorIsTheSignedInUser(t *testing.T) {
	svc := newSyncFixture(t)
	a, b := identityCtx("user-a", "org-a"), identityCtx("user-b2", "org-a")
	ev := `{"scope_id":"apiary-a","type":6,"date":"2026-07-11","hive_id":"hive-a","title":"Oxalic","author_id":"someone-else"}`
	if err := push(svc, a, change("event", "ev-1", "apiary-a", ev)); err != nil {
		t.Fatal(err)
	}
	// A later edit by a colleague claiming authorship changes nothing about it.
	if err := push(svc, b, change("event", "ev-1", "apiary-a", `{"title":"Oxalic acid","author_id":"user-b2"}`)); err != nil {
		t.Fatal(err)
	}
	var author string
	if err := svc.db.Get(&author, `SELECT author_id FROM event WHERE id = 'ev-1'`); err != nil || author != "user-a" {
		t.Fatalf("stored author = %q (%v), want user-a", author, err)
	}
	for _, c := range pullAll(t, svc, identityCtx("user-c", "org-a")) {
		if c.EntityId == "ev-1" && (strings.Contains(c.PayloadJson, "someone-else") || strings.Contains(c.PayloadJson, "user-b2")) {
			t.Fatalf("pulled a claimed author: %s", c.PayloadJson)
		}
	}
}

// An apiary with the id of a member's personal scope must not open that
// scope to whoever created it.
func TestPersonalScopeCannotBeOpenedByAnApiary(t *testing.T) {
	svc := newSyncFixture(t)
	att := identityCtx("attacker", "org-a")
	res := pushRes(t, svc, att,
		change("apiary", "user:victim", "user:victim", `{"name":"x","created_at":"2026-07-11","updated_at":"2026-07-11"}`),
		change("task", "t-planted", "user:victim", `{"title":"planted","created_at":"2026-07-11"}`),
	)
	wantRejected(t, res, map[int32]string{0: rejForbiddenScope, 1: rejForbiddenScope})
	// Not even in the attacker's own scope, and not under any other id.
	res = pushRes(t, svc, att,
		change("apiary", "user:victim", "apiary-x", `{"name":"x"}`),
		change("hive", "user:victim", "apiary-a", `{"name":"x"}`))
	wantRejected(t, res, map[int32]string{0: rejForbiddenScope, 1: rejInvalid})
	for _, c := range pullAll(t, svc, identityCtx("victim", "org-a")) {
		if isPersonal(c.EntityId) || c.EntityId == "t-planted" {
			t.Fatalf("the victim pulls %s %s", c.Entity, c.EntityId)
		}
	}
}

// A shared row changed in the writer's personal scope would be stored for
// the workspace but logged where only the writer pulls it.
func TestSharedRowsAreNotWrittenInThePersonalScope(t *testing.T) {
	svc := newSyncFixture(t)
	a := identityCtx("user-a", "org-a")
	if err := push(svc, a,
		change("task", "task-shared", "apiary-a", `{"title":"varroa","apiary_id":"apiary-a","created_at":"2026-07-11","deleted":0}`),
		change("event", "ev-shared", "apiary-a", `{"scope_id":"apiary-a","apiary_id":"apiary-a","type":1,"date":"2026-07-11","deleted":0}`),
		change("task", "task-mine", "user:user-a", `{"title":"dentist","apiary_id":"","created_at":"2026-07-11","deleted":0}`),
		change("event", "ev-mine", "user:user-a", `{"scope_id":"user:user-a","type":1,"date":"2026-07-11","deleted":0}`),
	); err != nil {
		t.Fatal(err)
	}
	del := change("task", "task-shared", "user:user-a", `{"deleted":1}`)
	del.Op = wv1.ChangeOp_CHANGE_OP_DELETE
	res := pushRes(t, svc, a,
		change("hive", "hive-a", "user:user-a", `{"name":"renamed by A"}`),
		change("apiary", "apiary-a", "user:user-a", `{"name":"renamed"}`),
		change("task", "task-shared", "user:user-a", `{"title":"hidden"}`),
		del,
		change("task", "task-new", "user:user-a", `{"title":"t","apiary_id":"apiary-a"}`),
		change("event", "ev-shared", "user:user-a", `{"title":"hidden"}`),
		change("event", "ev-new", "user:user-a", `{"scope_id":"apiary-a","type":1}`),
		change("inspection", "in-new", "user:user-a", `{"hive_id":"hive-a","date":"2026-07-11"}`),
		// The caller's own personal rows stay writable there.
		change("task", "task-mine", "user:user-a", `{"done":1}`),
		change("event", "ev-mine", "user:user-a", `{"title":"note"}`),
		change("task", "task-mine2", "user:user-a", `{"title":"t2","created_at":"2026-07-11","deleted":0}`),
	)
	wantRejected(t, res, map[int32]string{0: rejForbiddenScope, 1: rejForbiddenScope, 2: rejForbiddenScope,
		3: rejForbiddenScope, 4: rejForbiddenScope, 5: rejForbiddenScope, 6: rejForbiddenScope, 7: rejForbiddenScope})
	if got := hiveName(t, svc, "hive-a"); got != "H1" {
		t.Fatalf("hive renamed to %q", got)
	}
}

// A 0.4.x app drops its whole outbox batch after a Push that succeeds and
// never looks at the rejections. A change it stamped too far ahead must
// therefore fail the Push, so the batch is kept and sent again.
func TestLegacyPushWithClockAheadFailsAsAWhole(t *testing.T) {
	svc := newSyncFixture(t)
	ahead := change("hive", "hive-a", "apiary-a", `{"note":"from a fast clock"}`)
	ahead.Hlc = wsync.Format(time.Now().Add(time.Hour).UnixMilli(), 0, "dev")
	fine := change("hive", "hive-a", "apiary-a", `{"name":"renamed"}`)

	_, err := svc.Push(legacyA, &wv1.PushRequest{Changes: []*wv1.Change{fine, ahead}})
	wantCode(t, err, connect.CodeFailedPrecondition)
	if got := hiveName(t, svc, "hive-a"); got != "H1" {
		t.Fatalf("part of the failed batch was applied: name %q", got)
	}
	// Other refusals stay per change for such an app: sending them again
	// could never succeed.
	res := pushRes(t, svc, legacyA, fine, change("hive", "hive-x", "apiary-b", `{"name":"intruder"}`))
	wantRejected(t, res, map[int32]string{1: rejForbiddenScope})
	// A current app gets the rejection and re-stamps.
	res = pushRes(t, svc, ctxA, ahead)
	wantRejected(t, res, map[int32]string{0: rejClockAhead})
}

// A change that changes nothing is not logged. After a restore every device
// sends all its rows again; logging them would grow the log by devices
// times rows.
func TestChangeThatChangesNothingIsNotLogged(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	create := change("inspection", "in-1", "apiary-a",
		`{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","created_at":"2026-07-11T10:00:00Z","deleted":0,"photo_keys":{"add":["data:x"]}}`)
	if err := push(svc, ctx, create); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := svc.db.Get(&n, `SELECT COUNT(*) FROM change_log`); err != nil {
			t.Fatal(err)
		}
		return n
	}
	logged := count()
	stale := change("inspection", "in-1", "apiary-a", `{"date":"2026-07-10T10:00:00Z"}`)
	stale.Hlc = wsync.Format(time.Now().Add(-time.Hour).UnixMilli(), 0, "dev")
	if err := push(svc, ctx, create, create, stale); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != logged {
		t.Fatalf("the log grew from %d to %d entries for changes that changed nothing", logged, got)
	}
	if err := push(svc, ctx, change("inspection", "in-1", "apiary-a", `{"note":"newer"}`)); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != logged+1 {
		t.Fatalf("a real change must be logged: %d entries, want %d", got, logged+1)
	}
}
