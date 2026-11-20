package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql/sqltest"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// newSyncFixture opens a fresh store with two tenants, one apiary each, and
// one hive owned by tenant A. It runs on SQLite in a temp dir unless
// BEEHIVE_TEST_DATABASE_DRIVER + BEEHIVE_TEST_DATABASE_DSN select a live
// Postgres or MariaDB; every test gets a database of its own (see sqltest).
func newSyncFixture(t *testing.T) *SyncService {
	t.Helper()
	return NewSyncService(newFixtureStore(t).DB(), wsync.NewHLC("test"))
}

// newFixtureStore opens and seeds the store behind newSyncFixture.
func newFixtureStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	cfg := &config.Config{Profile: "selfhost"}
	cfg.DB.Driver, cfg.DB.DSN = sqltest.New(t)
	store, err := sqlstore.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db := store.DB()
	if err := RecordBoot(context.Background(), db); err != nil {
		t.Fatalf("record boot: %v", err)
	}
	seed := []string{
		`INSERT INTO apiary (id, organization_id, name, created_at, updated_at)
		 VALUES ('apiary-a', 'org-a', 'A', '2026-01-01', '2026-01-01')`,
		`INSERT INTO apiary (id, organization_id, name, created_at, updated_at)
		 VALUES ('apiary-b', 'org-b', 'B', '2026-01-01', '2026-01-01')`,
		`INSERT INTO hive (id, organization_id, apiary_id, name, created_at, updated_at)
		 VALUES ('hive-a', 'org-a', 'apiary-a', 'H1', '2026-01-01', '2026-01-01')`,
	}
	for _, q := range seed {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return store
}

// identityCtx is a call from the current app: signed in, announcing the
// sync protocol it speaks.
func identityCtx(user, org string) context.Context {
	return WithSyncProtocol(legacyCtx(user, org), protocolSnapshots)
}

// legacyCtx is a call from a 0.4.x app, which announces no protocol version.
func legacyCtx(user, org string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{UserID: user, OrgID: org})
}

// push sends the changes and fails when the request or any single change
// is refused: most tests expect a clean batch.
func push(svc *SyncService, ctx context.Context, changes ...*wv1.Change) error {
	res, err := svc.Push(ctx, &wv1.PushRequest{Changes: changes})
	if err != nil {
		return err
	}
	if len(res.Rejected) > 0 {
		return fmt.Errorf("change %d rejected: %s (%s)", res.Rejected[0].Index, res.Rejected[0].Code, res.Rejected[0].Reason)
	}
	return nil
}

// pushRes sends the changes and returns the per-change outcome.
func pushRes(t *testing.T, svc *SyncService, ctx context.Context, changes ...*wv1.Change) *wv1.PushResponse {
	t.Helper()
	res, err := svc.Push(ctx, &wv1.PushRequest{Changes: changes})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return res
}

// wantRejected asserts that exactly the given changes (index -> code) were refused.
func wantRejected(t *testing.T, res *wv1.PushResponse, want map[int32]string) {
	t.Helper()
	got := map[int32]string{}
	for _, r := range res.Rejected {
		got[r.Index] = r.Code
		if r.Reason == "" {
			t.Errorf("change %d: rejection without a reason", r.Index)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected = %v, want %v (%v)", got, want, res.Rejected)
	}
}

func pullAll(t *testing.T, svc *SyncService, ctx context.Context) []*wv1.Change {
	t.Helper()
	res, err := svc.Pull(ctx, &wv1.PullRequest{Limit: 500})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	return res.Changes
}

// One clock for all test changes: a fresh HLC per change would restart at
// counter 0 and hand two changes in the same millisecond identical stamps;
// the second write then loses the LWW comparison and is dropped as stale.
var testHLC = wsync.NewHLC("dev")

func change(entity, id, scope, payload string) *wv1.Change {
	return &wv1.Change{
		Entity: entity, EntityId: id, ScopeId: scope,
		Op: wv1.ChangeOp_CHANGE_OP_UPSERT, PayloadJson: payload,
		Hlc: testHLC.Now(), AuthorId: "dev",
	}
}

const newHive = `{"apiary_id":"apiary-a","name":"New","created_at":"2026-07-11","updated_at":"2026-07-11"}`

func hiveName(t *testing.T, svc *SyncService, id string) string {
	t.Helper()
	var name string
	err := svc.db.Get(&name, svc.db.Rebind(`SELECT name FROM hive WHERE id = ?`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read hive %s: %v", id, err)
	}
	return name
}

func TestPushRejectsForeignScope(t *testing.T) {
	svc := newSyncFixture(t)
	// User of tenant B labels a change with tenant A's apiary scope.
	res := pushRes(t, svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-x", "apiary-a", `{"name":"intruder"}`))
	wantRejected(t, res, map[int32]string{0: rejForbiddenScope})
	if hiveName(t, svc, "hive-x") != "" {
		t.Fatal("the refused change was stored")
	}
}

func TestPushRejectsForeignRow(t *testing.T) {
	svc := newSyncFixture(t)
	// Scope label is user B's own apiary, but the row id belongs to tenant A.
	res := pushRes(t, svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-a", "apiary-b", `{"name":"hijacked"}`))
	wantRejected(t, res, map[int32]string{0: rejWrongTenant})
	if name := hiveName(t, svc, "hive-a"); name != "H1" {
		t.Fatalf("victim row was modified: name=%q", name)
	}
}

func TestPushRejectsForeignOrgPayload(t *testing.T) {
	svc := newSyncFixture(t)
	// Insert claiming to belong to tenant A while authenticated as tenant B.
	res := pushRes(t, svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-y", "apiary-b", `{"organization_id":"org-a","name":"smuggled"}`))
	wantRejected(t, res, map[int32]string{0: rejWrongTenant})
}

// A new apiary opens its own scope, but only once it was really stored: an
// apiary id that belongs to another tenant must not unlock that scope for
// the changes that follow it in the same request.
func TestPushDoesNotOpenScopeOfRefusedApiary(t *testing.T) {
	svc := newSyncFixture(t)
	res := pushRes(t, svc, identityCtx("user-b", "org-b"),
		change("apiary", "apiary-a", "apiary-a", `{"name":"stolen"}`),
		change("hive", "hive-x", "apiary-a", `{"apiary_id":"apiary-a","name":"intruder","created_at":"2026-07-11","updated_at":"2026-07-11"}`))
	wantRejected(t, res, map[int32]string{0: rejWrongTenant, 1: rejForbiddenScope})
}

func TestPushRefusesReadOnlyIdentities(t *testing.T) {
	svc := newSyncFixture(t)
	for name, id := range map[string]auth.Identity{
		"demo":          {UserID: "demo", OrgID: "org-a", Role: "demo"},
		"read-only key": {UserID: "user-a", OrgID: "org-a", ReadOnly: true},
	} {
		_, err := svc.Push(auth.WithIdentity(context.Background(), id),
			&wv1.PushRequest{Changes: []*wv1.Change{change("hive", "hive-ro", "apiary-a", newHive)}})
		var cerr *connect.Error
		if !errors.As(err, &cerr) || cerr.Code() != connect.CodePermissionDenied {
			t.Fatalf("%s: want PermissionDenied for the whole request, got %v", name, err)
		}
	}
	if hiveName(t, svc, "hive-ro") != "" {
		t.Fatal("a read-only identity wrote a row")
	}
}

func TestPushStampsCallerTenant(t *testing.T) {
	svc := newSyncFixture(t)
	// A legitimate insert (no organization_id in the payload) lands in the
	// caller's tenant.
	if err := push(svc, identityCtx("user-a", "org-a"),
		change("hive", "hive-new", "apiary-a", `{"apiary_id":"apiary-a","name":"New","created_at":"2026-07-11","updated_at":"2026-07-11"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}
	var org string
	if err := svc.db.Get(&org, `SELECT organization_id FROM hive WHERE id = 'hive-new'`); err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if org != "org-a" {
		t.Fatalf("organization_id = %q, want org-a", org)
	}
}

func TestPushAllowsNewApiaryScope(t *testing.T) {
	svc := newSyncFixture(t)
	// A brand-new apiary opens its own scope (scope id = apiary id).
	if err := push(svc, identityCtx("user-a", "org-a"),
		change("apiary", "apiary-new", "apiary-new", `{"name":"Orchard","created_at":"2026-07-11","updated_at":"2026-07-11"}`)); err != nil {
		t.Fatalf("push: %v", err)
	}
	var org string
	if err := svc.db.Get(&org, `SELECT organization_id FROM apiary WHERE id = 'apiary-new'`); err != nil || org != "org-a" {
		t.Fatalf("new apiary org = %q err=%v, want org-a", org, err)
	}
}

func TestPullFiltersByScope(t *testing.T) {
	svc := newSyncFixture(t)
	if err := push(svc, identityCtx("user-a", "org-a"),
		change("hive", "hive-a", "apiary-a", `{"name":"A renamed"}`)); err != nil {
		t.Fatalf("push a: %v", err)
	}
	if err := push(svc, identityCtx("user-b", "org-b"),
		change("hive", "hive-b", "apiary-b", `{"apiary_id":"apiary-b","name":"B hive","created_at":"2026-07-11","updated_at":"2026-07-11"}`)); err != nil {
		t.Fatalf("push b: %v", err)
	}
	resp, err := svc.Pull(identityCtx("user-b", "org-b"),
		&wv1.PullRequest{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	for _, ch := range resp.Changes {
		if ch.ScopeId != "apiary-b" && ch.ScopeId != "user:user-b" {
			t.Fatalf("pull leaked foreign scope %q", ch.ScopeId)
		}
	}
	if len(resp.Changes) != 1 {
		t.Fatalf("want exactly the own change, got %d", len(resp.Changes))
	}
}

// TestPushAppliesSQLiteClientPayload pushes payloads shaped exactly like the
// offline clients produce them (SQLite stores booleans as 0/1, integers arrive
// as JSON numbers, unset timestamps as ""): the server must coerce them into
// whatever the backing dialect expects (Postgres rejects 0/1 for BOOLEAN,
// MySQL cannot parse RFC3339 into DATETIME).
func TestPushAppliesSQLiteClientPayload(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := push(svc, ctx,
		change("inspection", "insp-1", "apiary-a",
			`{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","queen_seen":1,"eggs_seen":0,`+
				`"covered_larva":1,"frames":9,"brood_frames":4,"honey_kg":2.5,"temp_hive":34.8,`+
				`"note":"","created_at":"2026-07-11T10:05:00Z","deleted":0,"photo_keys":{"add":["k1"]}}`),
		change("treatment", "treat-1", "apiary-a",
			`{"hive_id":"hive-a","date":"2026-08-18T09:00:00Z","product":"Formic acid 60%",`+
				`"withdrawal_until":"","deleted":0}`),
		change("task", "task-1", "apiary-a",
			`{"title":"feed","done":0,"priority":2,"due_at":"2026-08-01","created_at":"2026-07-11T10:00:00Z","deleted":0}`),
	); err != nil {
		t.Fatalf("push client payload: %v", err)
	}

	var queenSeen, eggsSeen bool
	var frames int
	if err := svc.db.QueryRow(
		`SELECT queen_seen, eggs_seen, frames FROM inspection WHERE id = 'insp-1'`).
		Scan(&queenSeen, &eggsSeen, &frames); err != nil {
		t.Fatalf("inspection row: %v", err)
	}
	if !queenSeen || eggsSeen || frames != 9 {
		t.Fatalf("coercion: queen_seen=%v eggs_seen=%v frames=%d, want true/false/9", queenSeen, eggsSeen, frames)
	}
	var withdrawal any
	if err := svc.db.QueryRow(
		`SELECT withdrawal_until FROM treatment WHERE id = 'treat-1'`).Scan(&withdrawal); err != nil {
		t.Fatalf("treatment row: %v", err)
	}
	if s, isStr := withdrawal.(string); withdrawal != nil && (!isStr || s != "") {
		t.Fatalf(`withdrawal_until = %v (%T), want NULL or ""`, withdrawal, withdrawal)
	}

	// A delete op flips the tombstone (bool column) on every dialect.
	del := change("task", "task-1", "apiary-a", ``)
	del.Op = wv1.ChangeOp_CHANGE_OP_DELETE
	if err := push(svc, ctx, del); err != nil {
		t.Fatalf("push delete: %v", err)
	}
	var deleted bool
	if err := svc.db.QueryRow(`SELECT deleted FROM task WHERE id = 'task-1'`).Scan(&deleted); err != nil {
		t.Fatalf("task row: %v", err)
	}
	if !deleted {
		t.Fatalf("task not tombstoned after delete op")
	}
}

// --- per-change results ---

// One refused change must neither drop nor block the rest of its batch.
func TestPushAppliesEveryAcceptableChangeOfAMixedBatch(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	ahead := change("hive", "hive-a", "apiary-a", `{"name":"from the future"}`)
	ahead.Hlc = wsync.Format(time.Now().Add(11*time.Minute).UnixMilli(), 0, "dev")
	garbled := change("hive", "hive-a", "apiary-a", `{"name":"no clock"}`)
	garbled.Hlc = "tomorrow"

	res := pushRes(t, svc, ctx,
		change("hive", "hive-1", "apiary-a", newHive),                      // 0 ok
		change("hive", "hive-x", "apiary-b", `{"name":"foreign scope"}`),   // 1
		change("apiary", "apiary-b", "apiary-b", `{"name":"foreign row"}`), // 2
		change("hive", "hive-a", "apiary-a", `{"name":{"nested":true}}`),   // 3
		change("gadget", "g1", "apiary-a", `{"name":"what"}`),              // 4
		ahead,   // 5
		garbled, // 6
		change("hive", "hive-a", "apiary-a", `not json`),                       // 7
		change("hive", "hive-2", "apiary-a", newHive),                          // 8 ok
		change("hive", "hive-a", "apiary-a", `{"name":"H1 renamed"}`),          // 9 ok
		change("hive", "hive-3", "apiary-a", `{"name":"no apiary, no dates"}`), // 10: NOT NULL columns missing
	)
	wantRejected(t, res, map[int32]string{
		1: rejForbiddenScope, 2: rejWrongTenant, 3: rejInvalid, 4: rejUnknownEntity,
		5: rejClockAhead, 6: rejInvalid, 7: rejInvalid, 10: rejInvalid,
	})
	if hiveName(t, svc, "hive-1") != "New" || hiveName(t, svc, "hive-2") != "New" || hiveName(t, svc, "hive-a") != "H1 renamed" {
		t.Fatal("accepted changes of a mixed batch were not applied")
	}
	if hiveName(t, svc, "hive-3") != "" {
		t.Fatal("a change the database refused left a row behind")
	}

	// Only the accepted changes reach other devices, in order, without gaps
	// left by the rolled-back ones.
	var ids []string
	var seqs []int64
	if err := svc.db.Select(&ids, `SELECT entity_id FROM change_log ORDER BY seq`); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.Select(&seqs, `SELECT seq FROM change_log ORDER BY seq`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"hive-1", "hive-2", "hive-a"}) || !reflect.DeepEqual(seqs, []int64{1, 2, 3}) {
		t.Fatalf("change log = %v %v", ids, seqs)
	}
}

func TestPushRejectsOversizedValuesAndIds(t *testing.T) {
	svc := newSyncFixture(t)
	big, _ := json.Marshal(map[string]string{"note": strings.Repeat("x", maxValueBytes+1)})
	res := pushRes(t, svc, identityCtx("user-a", "org-a"),
		change("hive", "hive-a", "apiary-a", string(big)),
		change("hive", strings.Repeat("i", maxIDLen+1), "apiary-a", newHive),
		change("hive", "", "apiary-a", newHive),
		&wv1.Change{Entity: "hive", EntityId: "hive-a", ScopeId: "apiary-a", Hlc: testHLC.Now(), PayloadJson: `{"name":"no op"}`},
	)
	wantRejected(t, res, map[int32]string{0: rejInvalid, 1: rejInvalid, 2: rejInvalid, 3: rejInvalid})
}

// The pull cursor must only ever come from Pull. Push used to return the
// global sequence counter; a device that stored it skipped everything
// other devices had written before its push.
func TestPushDoesNotHandOutACursor(t *testing.T) {
	svc := newSyncFixture(t)
	if err := push(svc, identityCtx("user-b", "org-b"),
		change("apiary", "apiary-b", "apiary-b", `{"name":"B renamed"}`)); err != nil {
		t.Fatal(err)
	}
	res := pushRes(t, svc, identityCtx("user-a", "org-a"), change("hive", "hive-1", "apiary-a", newHive))
	if res.ServerCursor != "" {
		t.Fatalf("push returned cursor %q; it exposes the global counter and lets devices skip changes", res.ServerCursor)
	}
	if skew := time.Since(time.UnixMilli(res.ServerTimeMs)); skew < 0 || skew > time.Minute {
		t.Fatalf("server_time_ms = %d", res.ServerTimeMs)
	}
}

func TestPushSetsAuthorFromIdentity(t *testing.T) {
	svc := newSyncFixture(t)
	ch := change("hive", "hive-1", "apiary-a", newHive)
	ch.AuthorId = "somebody-else"
	if err := push(svc, identityCtx("user-a", "org-a"), ch); err != nil {
		t.Fatal(err)
	}
	got := pullAll(t, svc, identityCtx("user-a", "org-a"))
	if len(got) != 1 || got[0].AuthorId != "user-a" {
		t.Fatalf("author must be the signed-in user, got %+v", got)
	}
}

// --- payload contract ---

// A payload key is never an SQL identifier on the server, and it must not
// travel on to other devices either, where older apps interpolate it.
func TestChangeLogCarriesOnlyKnownColumns(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	const evil = `name = 'x'; DROP TABLE apiary; --`
	payload, _ := json.Marshal(map[string]any{
		"name": "Renamed", evil: 1, "column_from_a_newer_app": "kept out",
		"id": "other-id", "field_hlc": `{"name":"999999999999999:99999:x"}`,
	})
	if err := push(svc, ctx, change("hive", "hive-a", "apiary-a", string(payload))); err != nil {
		t.Fatalf("unknown keys must be dropped, not refused: %v", err)
	}
	got := pullAll(t, svc, ctx)
	if len(got) != 1 || got[0].PayloadJson != `{"name":"Renamed"}` {
		t.Fatalf("payload replayed to devices = %+v", got)
	}
	if hiveName(t, svc, "hive-a") != "Renamed" {
		t.Fatal("known column was not applied")
	}
}

// The log holds the normalised form: what the device's SQLite would store.
func TestChangeLogPayloadIsNormalised(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	if err := push(svc, ctx, change("inspection", "insp-1", "apiary-a",
		`{"hive_id":"hive-a","date":"2026-07-11T10:00:00.000Z","created_at":"2026-07-11T10:05:00.000Z",`+
			`"queen_seen":true,"eggs_seen":"0","frames":"9","honey_kg":"2.5","varroa":3,"temp_hive":"",`+
			`"photo_keys":{"add":["k1"],"remove":["k0"],"removed_tags":{},"junk":1}}`)); err != nil {
		t.Fatal(err)
	}
	del := change("inspection", "insp-1", "apiary-a", `{"anything":"ignored"}`)
	del.Op = wv1.ChangeOp_CHANGE_OP_DELETE
	if err := push(svc, ctx, del); err != nil {
		t.Fatal(err)
	}
	got := pullAll(t, svc, ctx)
	want := `{"created_at":"2026-07-11T10:05:00.000Z","date":"2026-07-11T10:00:00.000Z","eggs_seen":0,"frames":9,` +
		`"hive_id":"hive-a","honey_kg":2.5,"organization_id":"org-a",` +
		`"photo_keys":{"add":["k1"],"remove":["k0"],"removed_tags":{}},"queen_seen":1,"temp_hive":null,"varroa":"3"}`
	if len(got) != 2 || got[0].PayloadJson != want {
		t.Fatalf("normalised payload =\n%s\nwant\n%s", got[0].PayloadJson, want)
	}
	if got[1].PayloadJson != `{"deleted":1}` {
		t.Fatalf("delete payload = %s", got[1].PayloadJson)
	}
}

func TestWireScalarKinds(t *testing.T) {
	cases := []struct {
		kind colKind
		in   any
		want any
		bad  bool
	}{
		{kindString, "x", "x", false},
		{kindString, 1.5, "1.5", false},
		{kindString, true, "1", false},
		{kindString, nil, nil, false},
		{kindString, []any{"x"}, nil, true},
		{kindString, map[string]any{}, nil, true},
		{kindBool, 1.0, int64(1), false},
		{kindBool, 0.0, int64(0), false},
		{kindBool, false, int64(0), false},
		{kindBool, "true", int64(1), false},
		{kindBool, "maybe", nil, true},
		{kindInt, 9.0, int64(9), false},
		{kindInt, "12", int64(12), false},
		{kindInt, "", nil, false},
		{kindInt, "many", nil, true},
		{kindInt, 1e300, nil, true},
		{kindFloat, 2.5, 2.5, false},
		{kindFloat, "2.5", 2.5, false},
		{kindFloat, "NaN", nil, true},
		{kindTime, "2026-07-11", "2026-07-11", false},
		{kindTime, "", "", false},
		{kindTime, 1783764000.0, nil, true},
	}
	for _, c := range cases {
		got, cerr := wireScalar("col", c.kind, c.in)
		if (cerr != nil) != c.bad || (!c.bad && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("kind %d, %#v: got %#v (%v), want %#v (bad=%v)", c.kind, c.in, got, cerr, c.want, c.bad)
		}
	}
}

// --- clocks ---

func serverAhead(svc *SyncService) time.Duration {
	d, _ := wsync.AheadBy(svc.hlc.Now(), time.Now())
	return d
}

// A stamp the server refuses (malformed, or too far ahead) must not move the
// server clock: it used to be folded in before the change was even checked.
func TestRefusedStampsDoNotMoveTheServerClock(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	farAhead := change("hive", "hive-a", "apiary-a", `{"name":"x"}`)
	farAhead.Hlc = wsync.Format(time.Now().Add(24*time.Hour).UnixMilli(), 0, "dev")
	overflow := change("hive", "hive-a", "apiary-a", `{"name":"x"}`)
	overflow.Hlc = "999999999999999999:0:x"
	foreign := change("hive", "hive-x", "apiary-b", `{"name":"x"}`)
	foreign.Hlc = wsync.Format(time.Now().Add(5*time.Minute).UnixMilli(), 0, "dev")

	res := pushRes(t, svc, ctx, farAhead, overflow, foreign)
	wantRejected(t, res, map[int32]string{0: rejClockAhead, 1: rejInvalid, 2: rejForbiddenScope})
	if d := serverAhead(svc); d > time.Second {
		t.Fatalf("server clock runs %v ahead after refused changes", d)
	}
}

func TestAcceptedStampWithinDriftMovesTheServerClock(t *testing.T) {
	svc := newSyncFixture(t)
	ch := change("hive", "hive-a", "apiary-a", `{"name":"slightly ahead"}`)
	ch.Hlc = wsync.Format(time.Now().Add(5*time.Minute).UnixMilli(), 3, "dev")
	if err := push(svc, identityCtx("user-a", "org-a"), ch); err != nil {
		t.Fatalf("a stamp within the allowed drift must be accepted: %v", err)
	}
	if next := svc.hlc.Now(); wsync.Compare(next, ch.Hlc) <= 0 {
		t.Fatalf("server stamp %s must sort after the accepted %s", next, ch.Hlc)
	}
}

// --- pull scoping ---

// The workspace is the sharing unit: every member sees the workspace's
// changes, nobody sees another workspace's, and personal rows stay with
// their owner inside the workspace they were written in.
func TestPullIsScopedByTenant(t *testing.T) {
	svc := newSyncFixture(t)
	const personalTask = `{"title":"mine","done":0,"priority":1,"created_at":"2026-07-11T10:00:00Z","deleted":0}`
	// user-x is a member of both tenants and keeps a personal task in A.
	if err := push(svc, identityCtx("user-x", "org-a"),
		change("task", "task-x", "user:user-x", personalTask),
		change("hive", "hive-1", "apiary-a", newHive)); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, identityCtx("user-b", "org-b"),
		change("apiary", "apiary-b", "apiary-b", `{"name":"B renamed"}`)); err != nil {
		t.Fatal(err)
	}

	seen := func(user, org string) []string {
		var ids []string
		for _, ch := range pullAll(t, svc, identityCtx(user, org)) {
			ids = append(ids, ch.EntityId)
		}
		return ids
	}
	if got := seen("user-x", "org-a"); !reflect.DeepEqual(got, []string{"task-x", "hive-1"}) {
		t.Fatalf("owner in the tenant of origin sees %v", got)
	}
	if got := seen("user-x", "org-b"); !reflect.DeepEqual(got, []string{"apiary-b"}) {
		t.Fatalf("personal rows leaked into the user's other tenant: %v", got)
	}
	if got := seen("user-a", "org-a"); !reflect.DeepEqual(got, []string{"hive-1"}) {
		t.Fatalf("a colleague sees %v, want only the shared hive", got)
	}

	// The leaked copy of the task on a device signed in to tenant B cannot
	// be written back there.
	res := pushRes(t, svc, identityCtx("user-x", "org-b"), change("task", "task-x", "user:user-x", `{"done":1}`))
	wantRejected(t, res, map[int32]string{0: rejWrongTenant})
	// Nor can anybody write into someone else's personal scope.
	res = pushRes(t, svc, identityCtx("user-a", "org-a"), change("task", "task-y", "user:user-x", personalTask))
	wantRejected(t, res, map[int32]string{0: rejForbiddenScope})
}

func TestPullPagesWithACursorAndReportsServerTime(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	for i := 0; i < 5; i++ {
		if err := push(svc, ctx, change("hive", "hive-a", "apiary-a", fmt.Sprintf(`{"name":"n%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.Pull(ctx, &wv1.PullRequest{Limit: 2})
	if err != nil || len(first.Changes) != 2 || !first.HasMore || first.ServerTimeMs == 0 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	rest, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: first.NextCursor, Limit: 500})
	if err != nil || len(rest.Changes) != 3 || rest.HasMore {
		t.Fatalf("second page: %+v %v", rest, err)
	}
	done, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: rest.NextCursor})
	if err != nil || len(done.Changes) != 0 || done.NextCursor != rest.NextCursor {
		t.Fatalf("empty page must keep the cursor: %+v %v", done, err)
	}
}

// A failing query is an error, never "nothing to sync" or "not allowed".
func TestSyncReturnsQueryErrors(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	svc.db.MustExec(`ALTER TABLE apiary RENAME TO apiary_gone`)
	if _, err := svc.Push(ctx, &wv1.PushRequest{Changes: []*wv1.Change{change("hive", "hive-1", "apiary-a", newHive)}}); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("push with a failing scope query: %v", err)
	}
	svc.db.MustExec(`ALTER TABLE change_log RENAME TO change_log_gone`)
	if _, err := svc.Pull(ctx, &wv1.PullRequest{}); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("pull with a failing query: %v", err)
	}
}

// --- subscribe ---

func TestSubscribeNudgesOnlyForTheCallersTenant(t *testing.T) {
	svc := newSyncFixture(t)
	svc.poll = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- svc.subscribe(ctx, auth.Identity{UserID: "user-a", OrgID: "org-a"}, 0,
			func(e *wv1.SubscribeEvent) error { events <- e.ServerCursor; return nil })
	}()

	// Activity in tenant B, and somebody else's personal row in tenant A,
	// are none of user A's business.
	if err := push(svc, identityCtx("user-b", "org-b"), change("apiary", "apiary-b", "apiary-b", `{"name":"B"}`)); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, identityCtx("user-x", "org-a"), change("task", "task-x", "user:user-x",
		`{"title":"mine","created_at":"2026-07-11T10:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-events:
		t.Fatalf("nudged with cursor %s for changes the caller cannot pull", c)
	case <-time.After(60 * time.Millisecond):
	}

	if err := push(svc, identityCtx("user-a", "org-a"), change("hive", "hive-a", "apiary-a", `{"name":"mine"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-events:
		if c != "3" {
			t.Fatalf("cursor = %s, want the sequence of the caller's own change", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no nudge for a change in the caller's tenant")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("subscribe ended with %v", err)
	}
}

func TestSubscribeCapsStreamsPerUser(t *testing.T) {
	svc := newSyncFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	id := auth.Identity{UserID: "user-a", OrgID: "org-a"}
	noop := func(*wv1.SubscribeEvent) error { return nil }
	done := make(chan error, maxSubscribeStreams)
	for i := 0; i < maxSubscribeStreams; i++ {
		go func() { done <- svc.subscribe(ctx, id, 0, noop) }()
	}
	open := func() int {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.streams["user-a"]
	}
	for deadline := time.Now().Add(2 * time.Second); open() < maxSubscribeStreams; {
		if time.Now().After(deadline) {
			t.Fatalf("only %d streams opened", open())
		}
		time.Sleep(time.Millisecond)
	}

	if err := svc.subscribe(ctx, id, 0, noop); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("stream beyond the cap: %v", err)
	}
	// Another user is not affected, and closed streams free their slots.
	other, cancelOther := context.WithCancel(context.Background())
	cancelOther()
	if err := svc.subscribe(other, auth.Identity{UserID: "user-b", OrgID: "org-b"}, 0, noop); err != nil {
		t.Fatalf("other user: %v", err)
	}
	cancel()
	for i := 0; i < maxSubscribeStreams; i++ {
		if err := <-done; err != nil {
			t.Fatalf("stream ended with %v", err)
		}
	}
	if open() != 0 {
		t.Fatalf("%d slots still taken after all streams closed", open())
	}
}

// --- concurrency ---

// Merging reads a row and writes back its field clock and set columns. Two
// pushes to the same row must not run that read-modify-write side by side:
// on Postgres and MySQL the later commit would silently undo the earlier
// one. (SQLite has a single writer; there this only checks the bookkeeping.)
func TestConcurrentPushesToOneRowLoseNothing(t *testing.T) {
	store := newFixtureStore(t)
	svc := NewSyncService(store.DB(), wsync.NewHLC("test"))
	writer := NewWriter(store.DB(), wsync.NewHLC("api"))
	ctx := identityCtx("user-a", "org-a")
	if err := push(svc, ctx, change("inspection", "insp-1", "apiary-a",
		`{"hive_id":"hive-a","date":"2026-07-11T10:00:00Z","created_at":"2026-07-11T10:00:00Z","deleted":0}`)); err != nil {
		t.Fatal(err)
	}

	cols := []string{"frames", "stores", "queen_cells", "brood_frames", "calmness", "frames_added", "frames_removed", "youngest_larva"}
	const rounds = 6
	var wg sync.WaitGroup
	errs := make(chan error, len(cols)*rounds*2)
	for w, col := range cols {
		wg.Add(2)
		go func() { // a device pushing
			defer wg.Done()
			for r := 1; r <= rounds; r++ {
				errs <- push(svc, ctx, change("inspection", "insp-1", "apiary-a",
					fmt.Sprintf(`{"%s":%d,"photo_keys":{"add":["p-%d-%d"]}}`, col, r, w, r)))
			}
		}()
		go func() { // the API writing the same row
			defer wg.Done()
			for r := 1; r <= rounds; r++ {
				errs <- writer.Run(ctx, auth.Identity{UserID: "user-a", OrgID: "org-a"}, func(tw *txWriter) error {
					return tw.upsert("inspection", "insp-1", "apiary-a",
						map[string]any{"photo_keys": map[string]any{"add": []string{fmt.Sprintf("api-%d-%d", w, r)}}})
				})
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	rows, err := queryMaps(context.Background(), svc.db, `SELECT * FROM inspection WHERE id = ?`, "insp-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("read row: %v", err)
	}
	if got, want := len(wsync.ParseORSet(asString(rows[0]["photo_keys"])).Values()), len(cols)*rounds*2; got != want {
		t.Errorf("%d of %d concurrently added photos survived", got, want)
	}
	clock := wsync.ParseFieldClock(asString(rows[0]["field_hlc"]))
	for _, col := range cols {
		if ri(rows[0][col]) != rounds {
			t.Errorf("%s = %v, want the last round's value %d", col, rows[0][col], rounds)
		}
		if !wsync.Valid(clock[col]) {
			t.Errorf("field clock lost %s", col)
		}
	}
	var logged int
	if err := svc.db.Get(&logged, `SELECT COUNT(*) FROM change_log`); err != nil || logged != 1+len(cols)*rounds*2 {
		t.Errorf("change log has %d entries (%v)", logged, err)
	}
}
