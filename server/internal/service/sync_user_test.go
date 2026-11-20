package service

import (
	"context"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/auth"
	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
	wsync "github.com/openbeehive-org/openbeehive-app/server/internal/sync"
)

// setting is a change of the account settings of user, as its app sends it.
func setting(user, payload string) *wv1.Change {
	return change("user_settings", user, "user:"+user, payload)
}

// settingsOf reads the stored settings row of an account.
func settingsOf(t *testing.T, svc *SyncService, user string) (weight, temp, lang string, rows int) {
	t.Helper()
	var got []struct {
		Weight *string `db:"weight_unit"`
		Temp   *string `db:"temp_unit"`
		Lang   *string `db:"language"`
	}
	if err := svc.db.Select(&got, svc.db.Rebind(
		`SELECT weight_unit, temp_unit, language FROM user_settings WHERE id = ?`), user); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		return "", "", "", 0
	}
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return s(got[0].Weight), s(got[0].Temp), s(got[0].Lang), len(got)
}

func entitiesOf(changes []*wv1.Change) map[string]int {
	out := map[string]int{}
	for _, ch := range changes {
		out[ch.Entity+"/"+ch.EntityId]++
	}
	return out
}

// A preference set while signed in to one workspace reaches the account's
// devices in every other workspace, and nobody else's.
func TestAccountSettingsFollowTheAccountAcrossWorkspaces(t *testing.T) {
	svc := newSyncFixture(t)
	inA, inB := identityCtx("user-a", "org-a"), identityCtx("user-a", "org-b")
	if err := push(svc, inA, setting("user-a", `{"weight_unit":"lb","temp_unit":"f","language":"de"}`)); err != nil {
		t.Fatal(err)
	}

	for name, ctx := range map[string]context.Context{"workspace A": inA, "workspace B": inB} {
		if got := entitiesOf(pullAll(t, svc, ctx)); got["user_settings/user-a"] != 1 {
			t.Errorf("%s: pull = %v, want the account's settings", name, got)
		}
		rows, _, _ := snapshotAll(t, svc, ctx)
		row, ok := rows["user_settings/user-a"]
		if !ok || row.fields["weight_unit"] != "lb" || row.fields["language"] != "de" || row.clock["temp_unit"] == "" {
			t.Errorf("%s: snapshot row = %+v (present %v)", name, row, ok)
		}
	}
	for name, ctx := range map[string]context.Context{
		"colleague in A": identityCtx("user-a2", "org-a"),
		"member of B":    identityCtx("user-b", "org-b"),
	} {
		if got := entitiesOf(pullAll(t, svc, ctx)); got["user_settings/user-a"] != 0 {
			t.Errorf("%s pulled user A's settings: %v", name, got)
		}
		if rows, _, _ := snapshotAll(t, svc, ctx); len(rows) > 0 {
			if _, leaked := rows["user_settings/user-a"]; leaked {
				t.Errorf("%s got user A's settings in a snapshot", name)
			}
		}
	}

	// Subscribe wakes the account's devices in the other workspace.
	seq, err := svc.latestSeq(inB, identityOf(t, inB))
	if err != nil || seq == 0 {
		t.Fatalf("latest seq in B = %d, %v", seq, err)
	}
	if other, _ := svc.latestSeq(context.Background(), identityOf(t, identityCtx("user-b", "org-b"))); other != 0 {
		t.Errorf("another account in B is woken at seq %d", other)
	}
}

// Two devices in two workspaces edit the settings offline: one row, merged
// field by field by stamp, whatever order the changes arrive in.
func TestAccountSettingsMergePerField(t *testing.T) {
	svc := newSyncFixture(t)
	inA, inB := identityCtx("user-a", "org-a"), identityCtx("user-a", "org-b")
	old := setting("user-a", `{"weight_unit":"kg","temp_unit":"c"}`) // stamped first, sent last
	newer := setting("user-a", `{"weight_unit":"lb"}`)
	lang := setting("user-a", `{"language":"fr"}`)
	if err := push(svc, inB, lang); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, inA, newer); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, inB, old); err != nil {
		t.Fatal(err)
	}
	w, temp, l, n := settingsOf(t, svc, "user-a")
	if n != 1 || w != "lb" || temp != "c" || l != "fr" {
		t.Fatalf("settings = %q %q %q (%d rows), want lb c fr in one row", w, temp, l, n)
	}
}

func TestAccountSettingsAreTheAccountsOwn(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	res := pushRes(t, svc, ctx,
		setting("user-b", `{"weight_unit":"lb"}`),                                // someone else's row
		change("user_settings", "user-a", "apiary-a", `{"weight_unit":"lb"}`),    // not the personal scope
		change("user_settings", "user-b", "user:user-a", `{"weight_unit":"lb"}`), // own scope, other row
		setting("user-a", `{"weight_unit":"pounds of honey"}`),
		setting("user-a", `{"language":7}`),
		setting("user-a", `{"weight_unit":"lb","unknown":"x"}`),
	)
	wantRejected(t, res, map[int32]string{
		0: rejForbiddenScope, 1: rejForbiddenScope, 2: rejForbiddenScope, 3: rejInvalid, 4: rejInvalid,
	})
	if _, _, _, n := settingsOf(t, svc, "user-b"); n != 0 {
		t.Error("a row for another account was stored")
	}
	if w, _, _, _ := settingsOf(t, svc, "user-a"); w != "lb" {
		t.Errorf("weight_unit = %q", w)
	}
}

// 0.4.x apps have no table for the settings and must never receive them.
func TestLegacyClientsDoNotReceiveAccountSettings(t *testing.T) {
	svc := newSyncFixture(t)
	if err := push(svc, identityCtx("user-a", "org-a"),
		setting("user-a", `{"weight_unit":"lb"}`), rename0(t)); err != nil {
		t.Fatal(err)
	}
	got := entitiesOf(pullLog(t, svc, legacyCtx("user-a", "org-a")))
	if got["user_settings/user-a"] != 0 || got["hive/hive-a"] != 1 {
		t.Fatalf("legacy pull = %v", got)
	}
}

func rename0(t *testing.T) *wv1.Change {
	t.Helper()
	return change("hive", "hive-a", "apiary-a", `{"name":"renamed"}`)
}

// Leaving a workspace keeps the settings; deleting the account removes them
// with their change-log entries.
func TestAccountSettingsOutliveAMembershipButNotTheAccount(t *testing.T) {
	store := newFixtureStore(t)
	svc := NewSyncService(store.DB(), wsync.NewHLC("test"))
	ctx := context.Background()
	now := time.Now().UTC()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, created_at) VALUES (?, ?, ?)`, []any{"org-a", "A", now}},
		{`INSERT INTO organization (id, name, created_at) VALUES (?, ?, ?)`, []any{"org-b", "B", now}},
		{`INSERT INTO users (id, email, oidc_subject, created_at) VALUES (?, ?, '', ?)`, []any{"user-a", "a@example.org", now}},
		{`INSERT INTO users (id, email, oidc_subject, created_at) VALUES (?, ?, '', ?)`, []any{"user-c", "c@example.org", now}},
		{`INSERT INTO member (organization_id, benutzer_id, role) VALUES (?, ?, ?)`, []any{"org-a", "user-a", "member"}},
		{`INSERT INTO member (organization_id, benutzer_id, role) VALUES (?, ?, ?)`, []any{"org-a", "user-c", "owner"}},
		{`INSERT INTO member (organization_id, benutzer_id, role) VALUES (?, ?, ?)`, []any{"org-b", "user-a", "owner"}},
	} {
		if _, err := store.DB().Exec(store.DB().Rebind(q.sql), q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := push(svc, identityCtx("user-a", "org-a"), setting("user-a", `{"weight_unit":"lb"}`)); err != nil {
		t.Fatal(err)
	}
	if err := push(svc, identityCtx("user-c", "org-a"), setting("user-c", `{"temp_unit":"f"}`)); err != nil {
		t.Fatal(err)
	}
	logged := func(user string) int {
		t.Helper()
		var n int
		if err := store.DB().Get(&n, store.DB().Rebind(
			`SELECT COUNT(*) FROM change_log WHERE entity = 'user_settings' AND entity_id = ?`), user); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := store.Members().Remove(ctx, "org-a", "user-a"); err != nil {
		t.Fatal(err)
	}
	if w, _, _, n := settingsOf(t, svc, "user-a"); n != 1 || w != "lb" || logged("user-a") != 1 {
		t.Fatalf("after leaving A: weight %q, %d rows, %d log entries", w, n, logged("user-a"))
	}
	if got := entitiesOf(pullAll(t, svc, identityCtx("user-a", "org-b"))); got["user_settings/user-a"] != 1 {
		t.Fatalf("after leaving A, a pull in B = %v", got)
	}

	if err := store.DeleteAccount(ctx, "user-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, n := settingsOf(t, svc, "user-a"); n != 0 || logged("user-a") != 0 {
		t.Fatalf("after deleting the account: %d rows, %d log entries", n, logged("user-a"))
	}
	if _, temp, _, n := settingsOf(t, svc, "user-c"); n != 1 || temp != "f" || logged("user-c") != 1 {
		t.Fatal("deleting one account touched another account's settings")
	}
}

// Pages of the workspace log and the account's own entries interleave by
// sequence number, and a page never holds more than the limit.
func TestPullInterleavesAccountSettings(t *testing.T) {
	svc := newSyncFixture(t)
	ctx := identityCtx("user-a", "org-a")
	for i := range 3 {
		if err := push(svc, ctx, rename0(t), setting("user-a", `{"language":"`+[]string{"de", "fr", "it"}[i]+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cur := ""
	for range 10 {
		res, err := svc.Pull(ctx, &wv1.PullRequest{Cursor: cur, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Changes) > 1 {
			t.Fatalf("page of %d changes with limit 1", len(res.Changes))
		}
		for _, ch := range res.Changes {
			seen = append(seen, ch.Entity)
		}
		if cur = res.NextCursor; !res.HasMore {
			break
		}
	}
	want := []string{"hive", "user_settings", "hive", "user_settings", "hive", "user_settings"}
	if len(seen) != len(want) {
		t.Fatalf("pulled %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("pulled %v, want %v", seen, want)
		}
	}
}

func TestMergeLog(t *testing.T) {
	e := func(seqs ...int64) []logEntry {
		var out []logEntry
		for _, s := range seqs {
			out = append(out, logEntry{seq: s})
		}
		return out
	}
	got := mergeLog(e(1, 4, 5), e(2, 3, 9), 4)
	var seqs []int64
	for _, x := range got {
		seqs = append(seqs, x.seq)
	}
	if len(seqs) != 4 || seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 || seqs[3] != 4 {
		t.Fatalf("merged = %v", seqs)
	}
	if got := mergeLog(e(1), nil, 4); len(got) != 1 {
		t.Fatalf("merged with nothing = %v", got)
	}
}

func identityOf(t *testing.T, ctx context.Context) auth.Identity {
	t.Helper()
	id, ok := auth.FromContext(ctx)
	if !ok {
		t.Fatal("no identity")
	}
	return id
}
