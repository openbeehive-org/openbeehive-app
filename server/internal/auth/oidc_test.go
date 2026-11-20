package auth

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

func TestOIDCClaimsReadEmailVerified(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"sub":"s","email":"a@b.c","email_verified":true}`:    true,
		`{"sub":"s","email":"a@b.c","email_verified":"true"}`:  true, // some providers send a string
		`{"sub":"s","email":"a@b.c","email_verified":false}`:   false,
		`{"sub":"s","email":"a@b.c","email_verified":"false"}`: false,
		`{"sub":"s","email":"a@b.c","email_verified":1}`:       false,
		`{"sub":"s","email":"a@b.c","email_verified":null}`:    false,
		`{"sub":"s","email":"a@b.c"}`:                          false,
	} {
		var c Claims
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if bool(c.EmailVerified) != want {
			t.Errorf("%s: email_verified = %v, want %v", raw, c.EmailVerified, want)
		}
	}
}

// Linking a provider login to an existing account by email hands that
// account to whoever controls the provider identity, so both sides must have
// proven the address.
func TestOIDCLinksOnlyWhenBothSidesVerifiedTheEmail(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name                     string
		localVerified, providerV bool
		wantLinked               bool
	}{
		{"both verified", true, true, true},
		{"provider does not assert the email", true, false, false},
		{"local address never verified", false, true, false},
		{"neither", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAPIFixture(t, loginConfig())
			prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())
			victim := &storage.User{ID: "victim", Email: "victim@test.dev", Role: "user",
				PasswordHash: "x", EmailVerified: c.localVerified, CreatedAt: time.Now().UTC()}
			if err := f.store.Users().Create(ctx, victim); err != nil {
				t.Fatal(err)
			}
			claims := &Claims{Subject: "attacker-sub", Email: "Victim@Test.dev", EmailVerified: claimBoolean(c.providerV)}
			u, err := provisionOIDCUser(ctx, f.store.Users(), prov, f.cfg, "idp", claims)
			stored, _ := f.store.Users().GetByID(ctx, "victim")
			if c.wantLinked {
				if err != nil || u.ID != "victim" || stored.OIDCSubject != "idp:attacker-sub" {
					t.Fatalf("want the login linked to the account, got %v %+v", err, stored)
				}
				return
			}
			if !errors.Is(err, errLinkRefused) || u != nil {
				t.Fatalf("want errLinkRefused, got %v %+v", err, u)
			}
			if stored.OIDCSubject != "" {
				t.Fatalf("the refused login was linked anyway: %q", stored.OIDCSubject)
			}
			// No second account for the same address either.
			if _, err := f.store.Users().GetBySubject(ctx, "idp:attacker-sub"); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("a duplicate account was created: %v", err)
			}
		})
	}
}

func TestOIDCProvisioning(t *testing.T) {
	ctx := context.Background()
	f := newAPIFixture(t, loginConfig())
	prov := NewProvisioner(f.store.Users(), f.store.Orgs(), f.store.Members())

	// A new account: email stored lower-case, verification taken from the provider.
	u, err := provisionOIDCUser(ctx, f.store.Users(), prov, f.cfg, "idp",
		&Claims{Subject: "s1", Email: "  New.User@Test.DEV ", Name: "New", EmailVerified: true})
	if err != nil || u.Email != "new.user@test.dev" || !u.EmailVerified || u.Role != "user" {
		t.Fatalf("new account: %v %+v", err, u)
	}
	if ms, _ := f.store.Members().ListByUser(ctx, u.ID); len(ms) != 1 {
		t.Fatalf("a new account gets a personal workspace: %v", ms)
	}
	// The same subject signs in again without touching email matching.
	again, err := provisionOIDCUser(ctx, f.store.Users(), prov, f.cfg, "idp", &Claims{Subject: "s1"})
	if err != nil || again.ID != u.ID {
		t.Fatalf("returning subject: %v %+v", err, again)
	}
	unverified, err := provisionOIDCUser(ctx, f.store.Users(), prov, f.cfg, "idp", &Claims{Subject: "s2", Email: "u2@test.dev"})
	if err != nil || unverified.EmailVerified {
		t.Fatalf("an unasserted email must not be stored as verified: %v %+v", err, unverified)
	}
	// No email, no account.
	if _, err := provisionOIDCUser(ctx, f.store.Users(), prov, f.cfg, "idp", &Claims{Subject: "s3"}); !errors.Is(err, errNoEmail) {
		t.Fatalf("missing email: %v", err)
	}
	// Invite-only instances create no accounts over OIDC.
	f.cfg.Auth.RegistrationOpen = false
	if _, err := provisionOIDCUser(ctx, f.store.Users(), prov, f.cfg, "idp",
		&Claims{Subject: "s4", Email: "x@test.dev", EmailVerified: true}); !errors.Is(err, errInviteOnly) {
		t.Fatalf("invite-only: %v", err)
	}
}
