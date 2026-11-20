package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// Settings chosen in single-user mode follow the move to the instance
// admin: field by field by stamp where the admin has settings of its own,
// with their change-log entries, and nothing is left under "local".
func TestMoveCarriesUserSettings(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		st := func(n int) string { return fmt.Sprintf("%015d:00000:dev", 1767225600000+n) }
		put := func(user, weight, temp, lang, clock string) {
			t.Helper()
			if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO user_settings (id, weight_unit, temp_unit, language, field_hlc)
				VALUES (?, ?, ?, ?, ?)`), user, weight, temp, lang, clock); err != nil {
				t.Fatal(err)
			}
		}
		put("local", "lb", "f", "", fmt.Sprintf(`{"weight_unit":%q,"temp_unit":%q}`, st(2), st(1)))
		put("admin-1", "kg", "c", "de", fmt.Sprintf(`{"weight_unit":%q,"temp_unit":%q,"language":%q}`, st(1), st(3), st(0)))
		put("local-only", "lb", "", "", fmt.Sprintf(`{"weight_unit":%q}`, st(5)))
		if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id)
			VALUES ('cl-s', 1, 'user:local', 'user_settings', 'local', 1, '{"weight_unit":"lb"}', ?, 'local', 'user:local')`), st(2)); err != nil {
			t.Fatal(err)
		}
		if has, err := s.UserHasSettings(ctx, "local"); err != nil || !has {
			t.Fatalf("UserHasSettings before: %v %v", has, err)
		}

		if _, err := s.MoveOrgData(ctx, OrgMove{FromOrg: "local", ToOrg: "org-1", FromUser: "local", ToUser: "admin-1"}); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Weight, Temp, Lang sql.NullString
		}
		if err := s.db.QueryRow(`SELECT weight_unit, temp_unit, language FROM user_settings WHERE id = 'admin-1'`).
			Scan(&got.Weight, &got.Temp, &got.Lang); err != nil {
			t.Fatal(err)
		}
		if got.Weight.String != "lb" || got.Temp.String != "c" || got.Lang.String != "de" {
			t.Fatalf("admin's settings = %+v, want lb (newer from local), c and de (newer of the admin's)", got)
		}
		if has, _ := s.UserHasSettings(ctx, "local"); has {
			t.Fatal("settings left under local")
		}
		var org, scope, entity, author string
		if err := s.db.QueryRow(`SELECT org_id, scope_id, entity_id, author_id FROM change_log WHERE id = 'cl-s'`).
			Scan(&org, &scope, &entity, &author); err != nil {
			t.Fatal(err)
		}
		if org != "user:admin-1" || scope != "user:admin-1" || entity != "admin-1" || author != "admin-1" {
			t.Fatalf("log entry moved to %s %s %s %s", org, scope, entity, author)
		}

		// An admin without settings simply takes them over.
		if _, err := s.MoveOrgData(ctx, OrgMove{FromOrg: "x", ToOrg: "org-2", FromUser: "local-only", ToUser: "admin-2"}); err != nil {
			t.Fatal(err)
		}
		var w string
		if err := s.db.QueryRow(`SELECT weight_unit FROM user_settings WHERE id = 'admin-2'`).Scan(&w); err != nil || w != "lb" {
			t.Fatalf("admin-2 weight = %q, %v", w, err)
		}
	})
}
