package sqlstore

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// Signed-out sessions are kept by token hash until their token expires;
// recording one sweeps the expired records.
func TestSessionRevocations(t *testing.T) {
	onEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		s := b.blank(t)
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		// Hashes are hex SHA-256: 64 characters, the width of the column.
		hash := func(c string) string { return strings.Repeat(c, 64) }
		revoke := func(h string, expires time.Time) {
			t.Helper()
			if err := s.RevokeSession(ctx, h, expires); err != nil {
				t.Fatalf("revoke %s: %v", h[:1], err)
			}
		}
		later := now.Add(time.Hour).Truncate(time.Second)
		revoke(hash("a"), later)
		revoke(hash("b"), later)
		// Recording a token again replaces its record.
		revoke(hash("a"), later.Add(time.Minute))

		got, err := s.RevokedSessions(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, []string{hash("a"), hash("b")}) {
			t.Fatalf("revoked sessions: %v", keys)
		}
		if !got[hash("a")].Equal(later.Add(time.Minute)) || !got[hash("b")].Equal(later) {
			t.Fatalf("expiries: %v", got)
		}
		// Records whose tokens have expired by then are left out.
		if got, err := s.RevokedSessions(ctx, later); err != nil || len(got) != 1 {
			t.Fatalf("revoked sessions an hour later: %v %v", got, err)
		}

		// Recording a session sweeps the expired records.
		if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO session_revocation (token_hash, expires_at) VALUES (?, ?)`),
			hash("x"), now.Add(-time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		revoke(hash("c"), later)
		var left []string
		if err := s.db.Select(&left, `SELECT token_hash FROM session_revocation ORDER BY token_hash`); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(left, []string{hash("a"), hash("b"), hash("c")}) {
			t.Fatalf("records after the sweep: %v", left)
		}
	})
}
