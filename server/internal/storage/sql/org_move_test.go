package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// The auth storage tests run on every backend: SQLite always, Postgres and
// MySQL/MariaDB when their DSN is set (see live_test.go).

func TestAuthStorageSQLite(t *testing.T) { testAuthStorage(t, openSQLite(t)) }

func TestAuthStoragePostgres(t *testing.T) {
	testAuthStorage(t, openLive(t, config.DriverPostgres, "BEEHIVE_TEST_PG_DSN"))
}

func TestAuthStorageMySQL(t *testing.T) {
	testAuthStorage(t, openLive(t, config.DriverMySQL, "BEEHIVE_TEST_MYSQL_DSN"))
}

func testAuthStorage(t *testing.T, s *Store) {
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Run("session revocation mark", func(t *testing.T) { testSessionsValidAfter(t, s) })
	t.Run("invite expiry", func(t *testing.T) { testInviteExpiry(t, s) })
	t.Run("workspace delete removes api keys", func(t *testing.T) { testDeleteRemovesAPIKeys(t, s) })
	t.Run("move workspace data", func(t *testing.T) { testOrgMove(t, s) })
}

func testSessionsValidAfter(t *testing.T, s *Store) {
	ctx := context.Background()
	u := &storage.User{ID: "va-u1", Email: "va@example.org", Role: "user", CreatedAt: time.Now().UTC()}
	if err := s.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.Users().GetByID(ctx, u.ID)
	if err != nil || got.SessionsValidAfter != 0 {
		t.Fatalf("a new account has no revocation mark: %v %+v", err, got)
	}
	mark := time.Now().UnixMilli() // needs more than 32 bits
	if err := s.Users().SetSessionsValidAfter(ctx, u.ID, mark); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Users().GetByID(ctx, u.ID); got.SessionsValidAfter != mark {
		t.Fatalf("revocation mark: got %d, want %d", got.SessionsValidAfter, mark)
	}
}

func testInviteExpiry(t *testing.T, s *Store) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(storage.InviteTTL)
	fresh := &storage.Invite{ID: "inv-new", OrgID: "o", Email: "a@example.org", Role: "member",
		Token: "tok-new", CreatedAt: now, ExpiresAt: &expires}
	if err := s.Invites().Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	// An invite stored by a 0.4.x server: no expires_at, created 8 days ago.
	if _, err := s.db.ExecContext(ctx, s.db.Rebind(
		`INSERT INTO invite (id, organization_id, email, role, token, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
		"inv-old", "o", "b@example.org", "member", "tok-old", now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := s.Invites().GetByToken(ctx, "tok-new")
	if err != nil || got.ExpiresAt == nil || got.Expiry().UTC().Sub(expires).Abs() > time.Second {
		t.Fatalf("stored expiry: %v %+v", err, got)
	}
	if got.Expired(now) || !got.Expired(expires.Add(time.Second)) {
		t.Fatalf("a fresh invite is valid for seven days: %+v", got)
	}
	old, err := s.Invites().GetByToken(ctx, "tok-old")
	if err != nil || old.ExpiresAt != nil || !old.Expired(now) {
		t.Fatalf("an invite without expires_at expires seven days after creation: %v %+v", err, old)
	}
	list, err := s.Invites().ListByOrg(ctx, "o")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", err, list)
	}
}

func testDeleteRemovesAPIKeys(t *testing.T, s *Store) {
	ctx := context.Background()
	now := time.Now().UTC()
	for _, org := range []string{"del-org", "keep-org"} {
		if err := s.Orgs().Create(ctx, &storage.Organization{ID: org, Name: org, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, s.db.Rebind(
			`INSERT INTO api_key (id, user_id, organization_id, key_hash, created_at) VALUES (?, ?, ?, ?, ?)`),
			"key-"+org, "u", org, "hash-"+org, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Orgs().Delete(ctx, "del-org"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var left []string
	if err := s.db.SelectContext(ctx, &left, `SELECT organization_id FROM api_key ORDER BY organization_id`); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0] != "keep-org" {
		t.Fatalf("api keys after delete: %v, want only keep-org's", left)
	}
}

// seedOrg writes one row into every record table plus change-log entries for
// the workspace, authored by user.
func seedOrg(t *testing.T, s *Store, org, user string, logEntries int, seq *int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	p := org + "-"
	inserts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO apiary (id, organization_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			[]any{p + "a", org, "Apiary", now, now}},
		{`INSERT INTO hive (id, organization_id, apiary_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{p + "h", org, p + "a", "Hive", now, now}},
		{`INSERT INTO queen (id, organization_id, hive_id, year, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{p + "q", org, p + "h", 2026, now, now}},
		{`INSERT INTO inspection (id, organization_id, hive_id, date, created_at) VALUES (?, ?, ?, ?, ?)`,
			[]any{p + "i", org, p + "h", now, now}},
		{`INSERT INTO task (id, organization_id, title, assigned_to, created_at) VALUES (?, ?, ?, ?, ?)`,
			[]any{p + "t", org, "Task", user, now}},
		{`INSERT INTO treatment (id, organization_id, hive_id, date) VALUES (?, ?, ?, ?)`,
			[]any{p + "tr", org, p + "h", now}},
		{`INSERT INTO harvest (id, organization_id, apiary_id, date) VALUES (?, ?, ?, ?)`,
			[]any{p + "ha", org, p + "a", now}},
		{`INSERT INTO placement (id, organization_id, hive_id, apiary_id, start_at) VALUES (?, ?, ?, ?, ?)`,
			[]any{p + "p", org, p + "h", p + "a", now}},
		{`INSERT INTO event (id, organization_id, scope_id, type, date, author_id) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{p + "e1", org, p + "a", 1, now, user}},
		{`INSERT INTO event (id, organization_id, scope_id, type, date, author_id) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{p + "e2", org, "user:" + user, 1, now, user}},
	}
	for _, in := range inserts {
		if _, err := s.db.ExecContext(ctx, s.db.Rebind(in.q), in.args...); err != nil {
			t.Fatalf("seed %s: %v", in.q[:30], err)
		}
	}
	for i := range logEntries {
		*seq++
		scope, payload := p+"a", fmt.Sprintf(
			`{"organization_id":%q,"name":"Näme <b> & \"quoted\" %d","lat":47.5,"author_id":%q,"deleted":0}`, org, i, user)
		switch i % 4 {
		case 1: // personal scope, a task assigned to the user
			scope = "user:" + user
			payload = fmt.Sprintf(`{ "organization_id" : %q, "assigned_to": %q, "scope_id": %q }`, org, user, "user:"+user)
		case 2: // a delete carries no workspace
			payload = `{"deleted":1}`
		case 3: // not JSON at all
			payload = ""
		}
		if _, err := s.db.ExecContext(ctx, s.db.Rebind(
			`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			fmt.Sprintf("%scl%d", p, i), *seq, scope, "apiary", p+"a", 1, payload, "hlc", user, org); err != nil {
			t.Fatalf("seed change_log: %v", err)
		}
	}
}

func countOrg(t *testing.T, s *Store, table, col, org string) int {
	t.Helper()
	var n int
	if err := s.db.Get(&n, s.db.Rebind(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = ?`, table, col)), org); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func testOrgMove(t *testing.T, s *Store) {
	ctx := context.Background()
	const logEntries = 450 // more than two pages of the change-log rewrite
	var seq int64
	seedOrg(t, s, "local", "local", logEntries, &seq)
	seedOrg(t, s, "other", "someone", 8, &seq)

	if has, err := s.OrgHasData(ctx, "local"); err != nil || !has {
		t.Fatalf("OrgHasData before: %v %v", has, err)
	}
	move := OrgMove{FromOrg: "local", ToOrg: "admin-org", FromUser: "local", ToUser: "admin-id"}
	moved, err := s.MoveOrgData(ctx, move)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	wantRows := int64(len(orgScopedTables) + 1 + logEntries) // event holds two rows
	if moved != wantRows {
		t.Errorf("moved %d rows, want %d", moved, wantRows)
	}

	for _, table := range orgScopedTables {
		want := 1
		if table == "event" {
			want = 2
		}
		if n := countOrg(t, s, table, "organization_id", "local"); n != 0 {
			t.Errorf("%s: %d rows left under local", table, n)
		}
		if n := countOrg(t, s, table, "organization_id", "admin-org"); n != want {
			t.Errorf("%s: %d rows in the target workspace, want %d", table, n, want)
		}
		if n := countOrg(t, s, table, "organization_id", "other"); n != want {
			t.Errorf("%s: another workspace was touched (%d rows, want %d)", table, n, want)
		}
	}
	if n := countOrg(t, s, "event", "author_id", "admin-id"); n != 2 {
		t.Errorf("event authorship not handed over: %d", n)
	}
	if n := countOrg(t, s, "event", "scope_id", "user:admin-id"); n != 1 {
		t.Errorf("event personal scope not handed over: %d", n)
	}
	if n := countOrg(t, s, "task", "assigned_to", "admin-id"); n != 1 {
		t.Errorf("task assignment not handed over: %d", n)
	}
	if n := countOrg(t, s, "task", "assigned_to", "someone"); n != 1 {
		t.Errorf("another workspace's task was reassigned")
	}

	// The change log: every entry moved, order kept, payloads rewritten.
	if n := countOrg(t, s, "change_log", "org_id", "local"); n != 0 {
		t.Errorf("change_log: %d entries left under local", n)
	}
	if n := countOrg(t, s, "change_log", "org_id", "other"); n != 8 {
		t.Errorf("change_log: another workspace was touched (%d)", n)
	}
	var rows []struct {
		ID      string `db:"id"`
		Seq     int64  `db:"seq"`
		ScopeID string `db:"scope_id"`
		Author  string `db:"author_id"`
		Payload string `db:"payload"`
	}
	if err := s.db.Select(&rows, s.db.Rebind(
		`SELECT id, seq, scope_id, author_id, payload FROM change_log WHERE org_id = ? ORDER BY seq`), "admin-org"); err != nil {
		t.Fatal(err)
	}
	if len(rows) != logEntries {
		t.Fatalf("change_log: %d entries in the target workspace, want %d", len(rows), logEntries)
	}
	for i, r := range rows {
		if r.ID != fmt.Sprintf("local-cl%d", i) || r.Seq != int64(i+1) {
			t.Fatalf("entry %d: order changed (id=%s seq=%d)", i, r.ID, r.Seq)
		}
		if r.Author != "admin-id" {
			t.Fatalf("entry %d: author %q", i, r.Author)
		}
		var fields map[string]any
		switch i % 4 {
		case 0:
			if r.ScopeID != "local-a" {
				t.Fatalf("entry %d: apiary scope changed to %q", i, r.ScopeID)
			}
			if err := json.Unmarshal([]byte(r.Payload), &fields); err != nil {
				t.Fatalf("entry %d: payload no longer JSON: %v", i, err)
			}
			wantName := fmt.Sprintf(`Näme <b> & "quoted" %d`, i)
			if fields["organization_id"] != "admin-org" || fields["author_id"] != "admin-id" ||
				fields["name"] != wantName || fields["lat"] != 47.5 || fields["deleted"] != float64(0) {
				t.Fatalf("entry %d: payload %s", i, r.Payload)
			}
		case 1:
			if r.ScopeID != "user:admin-id" {
				t.Fatalf("entry %d: personal scope %q", i, r.ScopeID)
			}
			_ = json.Unmarshal([]byte(r.Payload), &fields)
			if fields["organization_id"] != "admin-org" || fields["assigned_to"] != "admin-id" || fields["scope_id"] != "user:admin-id" {
				t.Fatalf("entry %d: payload %s", i, r.Payload)
			}
		case 2:
			if r.Payload != `{"deleted":1}` {
				t.Fatalf("entry %d: delete payload rewritten to %q", i, r.Payload)
			}
		case 3:
			if r.Payload != "" {
				t.Fatalf("entry %d: empty payload rewritten to %q", i, r.Payload)
			}
		}
	}
	var otherPayload string
	if err := s.db.Get(&otherPayload, `SELECT payload FROM change_log WHERE id = 'other-cl0'`); err != nil {
		t.Fatal(err)
	}
	if want := `{"organization_id":"other","name":"Näme <b> & \"quoted\" 0","lat":47.5,"author_id":"someone","deleted":0}`; otherPayload != want {
		t.Errorf("another workspace's payload changed: %s", otherPayload)
	}

	// Idempotent: nothing is left to move.
	if has, err := s.OrgHasData(ctx, "local"); err != nil || has {
		t.Fatalf("OrgHasData after: %v %v", has, err)
	}
	if again, err := s.MoveOrgData(ctx, move); err != nil || again != 0 {
		t.Fatalf("second move: %d rows, err %v", again, err)
	}
}

func TestRewritePayload(t *testing.T) {
	rw := map[string][2]string{"organization_id": {"local", "new"}}
	for _, c := range []struct{ name, in, want string }{
		{"match", `{"organization_id":"local","n":1}`, `{"n":1,"organization_id":"new"}`},
		{"other value", `{"organization_id":"else"}`, `{"organization_id":"else"}`},
		{"non-string value", `{"organization_id":7}`, `{"organization_id":7}`},
		{"absent", `{"n":1}`, `{"n":1}`},
		{"keeps escapes", `{"organization_id":"local","s":"<a> & é \n"}`, `{"organization_id":"new","s":"<a> & é \n"}`},
		{"array", `["local"]`, `["local"]`},
		{"null", `null`, `null`},
		{"garbage", `organization_id=local`, `organization_id=local`},
	} {
		if got := rewritePayload(c.in, rw); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// Every table that scopes rows by organization_id must either move with the
// workspace or be known not to hold records. A new synced table that is in
// neither list would silently stay behind when single-user data is adopted.
func TestOrgScopedTablesAreComplete(t *testing.T) {
	s := openSQLite(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	notRecords := map[string]bool{"member": true, "invite": true, "api_key": true}
	var got []string
	if err := s.db.Select(&got, `
		SELECT m.name FROM sqlite_master m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'organization_id'`); err != nil {
		t.Fatal(err)
	}
	var records []string
	for _, name := range got {
		if !notRecords[name] {
			records = append(records, name)
		}
	}
	want := append([]string(nil), orgScopedTables...)
	sort.Strings(records)
	sort.Strings(want)
	if fmt.Sprint(records) != fmt.Sprint(want) {
		t.Fatalf("tables with organization_id: %v\norgScopedTables:               %v\nadd the new table to orgScopedTables (or to notRecords if it holds no records)", records, want)
	}
}
