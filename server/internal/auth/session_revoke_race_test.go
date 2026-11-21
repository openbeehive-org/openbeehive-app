package auth

import (
	"context"
	"testing"
)

// A session issued after a revocation has returned must work, however soon
// after it: the revocation mark lies one millisecond ahead of the clock, and
// a sign-in, workspace switch or recovery in that millisecond used to get a
// token stamped before the mark, refused as revoked ("not signed in").
// Tokens issued before the revocation, even in its own millisecond, stay
// revoked.
func TestSessionIssuedRightAfterARevocationIsValid(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	a := f.signup(t, "a@test.dev")
	ctx := context.Background()
	id, err := f.sessions.Verify(ctx, a.token)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		before, _ := f.sessions.IssueLogin(id)
		if _, err := f.sessions.RevokeAll(ctx, id); err != nil {
			t.Fatal(err)
		}
		after, _ := f.sessions.IssueLogin(id)
		if _, err := f.sessions.Verify(ctx, after); err != nil {
			t.Fatalf("round %d: a session issued after the revocation was refused: %v", i, err)
		}
		if _, err := f.sessions.Verify(ctx, before); err == nil {
			t.Fatalf("round %d: a session issued before the revocation survived it", i)
		}
	}
}
