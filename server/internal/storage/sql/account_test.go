package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// Accounts stored before the email key existed: the upgrade gives each
// address one holder, keeps accounts that differ only by case apart, and
// tells the operator about them.
func TestEmailKeysForExistingAccounts(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		// Everything before the account-lifecycle migration.
		if err := s.migrateUpTo(ctx, "0021"); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		for i, u := range []struct{ id, email string }{
			{"plain", "plain@example.org"},
			{"mixed", "Mixed@Example.org"},
			{"dup-upper", "Twin@Example.org"}, // older, but not in normal form
			{"dup-lower", "twin@example.org"},
			{"dup-third", "TWIN@example.org"},
		} {
			if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO users (id, email, oidc_subject, created_at) VALUES (?, ?, '', ?)`),
				u.id, u.email, now.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Fatalf("insert %s: %v", u.id, err)
			}
		}

		var logged bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&logged)
		err := s.Migrate(ctx)
		log.SetOutput(prev)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}

		keys := map[string]sql.NullString{}
		emails := map[string]string{}
		var rows []struct {
			ID    string         `db:"id"`
			Email string         `db:"email"`
			Key   sql.NullString `db:"email_key"`
		}
		if err := s.db.Select(&rows, `SELECT id, email, email_key FROM users`); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			keys[r.ID], emails[r.ID] = r.Key, r.Email
		}
		if len(rows) != 5 {
			t.Fatalf("no account may be merged or deleted, got %d rows", len(rows))
		}
		for id, want := range map[string]string{"plain": "plain@example.org", "mixed": "mixed@example.org", "dup-lower": "twin@example.org"} {
			if !keys[id].Valid || keys[id].String != want {
				t.Errorf("%s: email_key = %v, want %q", id, keys[id], want)
			}
		}
		for _, id := range []string{"dup-upper", "dup-third"} {
			if keys[id].Valid {
				t.Errorf("%s shares its address with another account and must not hold the key, got %q", id, keys[id].String)
			}
		}
		// Addresses are left as their owners entered them.
		if emails["mixed"] != "Mixed@Example.org" || emails["dup-upper"] != "Twin@Example.org" {
			t.Errorf("stored addresses changed: %v", emails)
		}

		// Sign-in by email reaches one account, whatever the capitalisation.
		for _, address := range []string{"twin@example.org", "Twin@Example.org", "TWIN@EXAMPLE.ORG"} {
			if u, err := s.Users().GetByEmail(ctx, address); err != nil || u.ID != "dup-lower" {
				t.Errorf("GetByEmail(%q) = %+v, %v; want dup-lower", address, u, err)
			}
		}
		if u, err := s.Users().GetByEmail(ctx, "MIXED@example.org"); err != nil || u.ID != "mixed" {
			t.Errorf("GetByEmail for an upgraded account: %+v %v", u, err)
		}

		// The operator is told, with both accounts named, at every start.
		for _, want := range []string{"dup-upper", "dup-third", "dup-lower", "Twin@Example.org"} {
			if !strings.Contains(logged.String(), want) {
				t.Errorf("the log must name %s:\n%s", want, logged.String())
			}
		}
		if conflicts, err := s.claimEmailKeys(ctx); err != nil || len(conflicts) != 2 {
			t.Fatalf("unresolved conflicts are reported again: %v %+v", err, conflicts)
		}

		// The guard holds for new accounts, also against the duplicates.
		for i, address := range []string{"Twin@Example.org", "twin@example.org", "PLAIN@example.org"} {
			u := &storage.User{ID: "new-" + string(rune('a'+i)), Email: address, Role: "user", CreatedAt: now}
			if err := s.Users().Create(ctx, u); err == nil {
				t.Errorf("a new account for %q must be refused", address)
			}
		}

		// The operator's decision: one twin gets another address, one is removed.
		if _, err := s.db.Exec(`UPDATE users SET email = 'Other@Example.org' WHERE id = 'dup-upper'`); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteAccount(ctx, "dup-third"); err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if conflicts, err := s.claimEmailKeys(ctx); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts after the operator's fix: %v %+v", err, conflicts)
		}
		if u, err := s.Users().GetByEmail(ctx, "other@example.org"); err != nil || u.ID != "dup-upper" {
			t.Fatalf("the renamed account is reachable by its new address: %+v %v", u, err)
		}
	})
}

func TestPasswordResetTokens(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		create := func(user, hash string, expires time.Time) {
			t.Helper()
			if err := s.CreatePasswordReset(ctx, user, hash, expires); err != nil {
				t.Fatal(err)
			}
		}
		create("u1", "hash-a", now.Add(time.Hour))
		create("u1", "hash-b", now.Add(time.Hour))
		create("u2", "hash-c", now.Add(time.Hour))

		if _, err := s.ConsumePasswordReset(ctx, "unknown", now); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("unknown token: %v", err)
		}
		if user, err := s.ConsumePasswordReset(ctx, "hash-a", now); err != nil || user != "u1" {
			t.Fatalf("consume: %q %v", user, err)
		}
		// Used once, and the account's other links go with it.
		for _, hash := range []string{"hash-a", "hash-b"} {
			if _, err := s.ConsumePasswordReset(ctx, hash, now); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("%s after use: %v", hash, err)
			}
		}
		// Another account's link is untouched, until it expires.
		if _, err := s.ConsumePasswordReset(ctx, "hash-c", now.Add(2*time.Hour)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("expired token: %v", err)
		}
		if _, err := s.ConsumePasswordReset(ctx, "hash-c", now); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("an expired link is gone for good: %v", err)
		}

		// Creating a link sweeps expired ones.
		if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO password_reset (id, user_id, token_hash, created_at, expires_at) VALUES ('old', 'u3', 'hash-old', ?, ?)`),
			now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		create("u3", "hash-new", now.Add(time.Hour))
		var left []string
		if err := s.db.Select(&left, `SELECT token_hash FROM password_reset`); err != nil || len(left) != 1 || left[0] != "hash-new" {
			t.Fatalf("stored links: %v %v", left, err)
		}
	})
}

func TestMembershipChangesKeepAnOwner(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := s.Orgs().Create(ctx, &storage.Organization{ID: "o1", Name: "Club", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		for id, role := range map[string]string{"owner": "owner", "member": "member"} {
			if err := s.Users().Create(ctx, &storage.User{ID: id, Email: id + "@example.org", Name: strings.ToUpper(id), Role: "user", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := s.Members().Add(ctx, &storage.Membership{OrgID: "o1", UserID: id, Role: role}); err != nil {
				t.Fatal(err)
			}
		}
		members, err := s.Members().ListByOrg(ctx, "o1")
		if err != nil || len(members) != 2 || members[0].UserID != "owner" || members[0].Email != "owner@example.org" || members[1].Name != "MEMBER" {
			t.Fatalf("list, owners first: %v %+v", err, members)
		}

		if err := s.Members().Remove(ctx, "o1", "owner"); !errors.Is(err, storage.ErrLastOwner) {
			t.Fatalf("removing the last owner: %v", err)
		}
		if err := s.Members().SetRole(ctx, "o1", "owner", "member"); !errors.Is(err, storage.ErrLastOwner) {
			t.Fatalf("demoting the last owner: %v", err)
		}
		if err := s.Members().SetRole(ctx, "o1", "nobody", "owner"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("unknown member: %v", err)
		}
		if err := s.Members().SetRole(ctx, "o1", "member", "owner"); err != nil {
			t.Fatal(err)
		}
		if err := s.Members().Remove(ctx, "o1", "owner"); err != nil {
			t.Fatalf("with a second owner the first may go: %v", err)
		}
		if err := s.Members().Remove(ctx, "o1", "owner"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("removing twice: %v", err)
		}
		if n, err := s.PasskeyCount(ctx, "owner"); err != nil || n != 0 {
			t.Fatalf("passkey count: %d %v", n, err)
		}
	})
}

// A workspace that goes with its only member's account is deleted only if
// the account still is its only member when the delete runs.
func TestDeleteIfOnlyMember(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		for _, org := range []string{"shared", "alone"} {
			if err := s.Orgs().Create(ctx, &storage.Organization{ID: org, Name: org, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := s.Members().Add(ctx, &storage.Membership{OrgID: org, UserID: "u1", Role: "owner"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Members().Add(ctx, &storage.Membership{OrgID: "shared", UserID: "u2", Role: "member"}); err != nil {
			t.Fatal(err)
		}

		if err := s.Orgs().DeleteIfOnlyMember(ctx, "shared", "u1"); !errors.Is(err, storage.ErrNotOnlyMember) {
			t.Fatalf("a workspace with another member: %v", err)
		}
		if _, err := s.Orgs().Get(ctx, "shared"); err != nil {
			t.Fatalf("the refused delete must leave the workspace: %v", err)
		}
		if members, err := s.Members().ListByOrg(ctx, "shared"); err != nil || len(members) != 2 {
			t.Fatalf("the refused delete must leave the members: %v %+v", err, members)
		}
		// Not a member at all: everyone in it is "somebody else".
		if err := s.Orgs().DeleteIfOnlyMember(ctx, "alone", "u2"); !errors.Is(err, storage.ErrNotOnlyMember) {
			t.Fatalf("a workspace of somebody else: %v", err)
		}

		if err := s.Orgs().DeleteIfOnlyMember(ctx, "alone", "u1"); err != nil {
			t.Fatalf("the only member's workspace: %v", err)
		}
		if _, err := s.Orgs().Get(ctx, "alone"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("the workspace must be gone: %v", err)
		}
		// Nobody joins a workspace that no longer exists.
		if err := s.Members().Add(ctx, &storage.Membership{OrgID: "alone", UserID: "u2", Role: "member"}); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("joining a deleted workspace: %v", err)
		}
		var n int
		if err := s.db.Get(&n, `SELECT COUNT(*) FROM member WHERE organization_id = 'alone'`); err != nil || n != 0 {
			t.Fatalf("memberships of the deleted workspace: %d %v", n, err)
		}
	})
}

// A join waits for a delete that is checking the members, and the other way
// round: whichever comes first, a workspace never disappears under a member
// who has just joined it.
func TestJoinAndDeleteIfOnlyMemberDoNotCross(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		for round := range 20 {
			org := "o" + string(rune('a'+round))
			if err := s.Orgs().Create(ctx, &storage.Organization{ID: org, Name: org, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if err := s.Members().Add(ctx, &storage.Membership{OrgID: org, UserID: "owner", Role: "owner"}); err != nil {
				t.Fatal(err)
			}
			joined, deleted := make(chan error, 1), make(chan error, 1)
			go func() {
				joined <- s.Members().Add(ctx, &storage.Membership{OrgID: org, UserID: "late", Role: "member"})
			}()
			go func() { deleted <- s.Orgs().DeleteIfOnlyMember(ctx, org, "owner") }()
			joinErr, deleteErr := <-joined, <-deleted

			_, getErr := s.Orgs().Get(ctx, org)
			exists := getErr == nil
			switch {
			case joinErr == nil && errors.Is(deleteErr, storage.ErrNotOnlyMember) && exists:
				// The join came first: the workspace stays, with both members.
				if members, _ := s.Members().ListByOrg(ctx, org); len(members) != 2 {
					t.Fatalf("round %d: members after join-first: %+v", round, members)
				}
			case errors.Is(joinErr, storage.ErrNotFound) && deleteErr == nil && !exists:
				// The delete came first: there was nothing left to join.
				var n int
				if err := s.db.Get(&n, s.db.Rebind(`SELECT COUNT(*) FROM member WHERE organization_id = ?`), org); err != nil || n != 0 {
					t.Fatalf("round %d: memberships of the deleted workspace: %d %v", round, n, err)
				}
			default:
				t.Fatalf("round %d: join %v, delete %v, workspace exists %v", round, joinErr, deleteErr, exists)
			}
		}
	})
}

// The personal tasks of an account are found by one rule, in one workspace
// or in all of them.
func TestDeletePersonalData(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		seq := 0
		task := func(org, id, apiary, scope string) {
			t.Helper()
			seq++
			var apiaryID any
			if apiary != "" {
				apiaryID = apiary
			}
			// A personal task names its owner; its changes are logged in
			// that account's personal scope.
			owner := strings.TrimPrefix(scope, "user:")
			if owner == scope {
				owner = ""
			}
			if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO task (id, organization_id, title, apiary_id, owner_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
				id, org, id, apiaryID, owner, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO change_log (id, seq, scope_id, entity, entity_id, op, payload, hlc, author_id, org_id) VALUES (?, ?, ?, 'task', ?, 1, '{}', 'h', 'u1', ?)`),
				"cl-"+id, seq, scope, id, org); err != nil {
				t.Fatal(err)
			}
		}
		task("o1", "mine-o1", "", "user:u1")
		task("o2", "mine-o2", "", "user:u1")
		task("o1", "theirs-o1", "", "user:u2")
		task("o1", "shared-o1", "ap-1", "ap-1")
		left := func() []string {
			t.Helper()
			var ids []string
			if err := s.db.Select(&ids, `SELECT id FROM task ORDER BY id`); err != nil {
				t.Fatal(err)
			}
			return ids
		}
		run := func(userID, orgID string) {
			t.Helper()
			tx, err := s.db.BeginTxx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := deletePersonalData(ctx, tx, userID, orgID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}

		run("u1", "o1") // a membership ended
		if got := strings.Join(left(), ","); got != "mine-o2,shared-o1,theirs-o1" {
			t.Fatalf("after leaving o1: %s", got)
		}
		var logged []string
		if err := s.db.Select(&logged, `SELECT id FROM change_log ORDER BY id`); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(logged, ","); got != "cl-mine-o2,cl-shared-o1,cl-theirs-o1" {
			t.Fatalf("change log after leaving o1: %s", got)
		}
		run("u1", "") // the account is deleted
		if got := strings.Join(left(), ","); got != "shared-o1,theirs-o1" {
			t.Fatalf("after deleting the account: %s", got)
		}
	})
}
