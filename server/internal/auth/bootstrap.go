package auth

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	sqlstore "github.com/openbeehive-org/openbeehive-app/server/internal/storage/sql"
)

// EnsureAdmin creates or repairs the dedicated instance admin account from
// BEEHIVE_ADMIN_EMAIL / BEEHIVE_ADMIN_PASSWORD. The env values are
// authoritative: on every boot the account is created if missing, and
// otherwise its role is forced back to "admin" and its password hash reset to
// the configured one, which doubles as password recovery. Sign-up never
// grants the admin role; this is the only path to it.
//
// Called at startup (after migrations, before the demo seeder) whenever
// password auth is enabled.
func EnsureAdmin(ctx context.Context, users storage.UserRepo, cfg *config.Config) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.Auth.AdminPassword), passwordHashCost)
	if err != nil {
		return fmt.Errorf("admin bootstrap: hash: %w", err)
	}
	u, err := users.GetByEmail(ctx, cfg.Auth.AdminEmail)
	if err == nil {
		if err := users.SetCredentials(ctx, u.ID, string(hash), "admin"); err != nil {
			return fmt.Errorf("admin bootstrap: update: %w", err)
		}
		return nil
	}
	u = &storage.User{
		ID: uuid.NewString(), Email: cfg.Auth.AdminEmail, Name: "Admin",
		Role: "admin", PasswordHash: string(hash), EmailVerified: true,
		CreatedAt: time.Now().UTC(),
	}
	if err := users.Create(ctx, u); err != nil {
		return fmt.Errorf("admin bootstrap: create: %w", err)
	}
	return nil
}

// LocalDataMover is the storage side of AdoptLocalData.
type LocalDataMover interface {
	OrgHasData(ctx context.Context, orgID string) (bool, error)
	MoveOrgData(ctx context.Context, m sqlstore.OrgMove) (int64, error)
}

// AdoptLocalData moves the records written in single-user mode (no login,
// everything under the fixed "local" workspace) into the instance admin's
// workspace, so that turning login on does not hide existing data. It runs at
// every boot with a login method enabled and does nothing once no row is
// left under "local". The move is one transaction; a failure leaves the data
// where it was and is retried at the next boot.
//
// Only password auth has an instance admin. With other login methods alone
// the data stays put and the log says how to adopt it.
func AdoptLocalData(ctx context.Context, store LocalDataMover, users storage.UserRepo, members storage.MemberRepo, prov *Provisioner, cfg *config.Config) error {
	has, err := store.OrgHasData(ctx, LocalUser.OrgID)
	if err != nil {
		return fmt.Errorf("single-user data: %w", err)
	}
	if !has {
		return nil
	}
	if !cfg.Auth.PasswordEnabled {
		log.Printf("auth: records from single-user mode exist but no instance admin is configured; " +
			"set BEEHIVE_PASSWORD_AUTH=true with BEEHIVE_ADMIN_EMAIL and BEEHIVE_ADMIN_PASSWORD to move them into the admin's workspace")
		return nil
	}
	admin, err := users.GetByEmail(ctx, cfg.Auth.AdminEmail)
	if err != nil {
		return fmt.Errorf("single-user data: instance admin: %w", err)
	}
	// The admin's own workspace: one they own, or a fresh personal one.
	target := ""
	memberships, err := members.ListByUser(ctx, admin.ID)
	if err != nil {
		return fmt.Errorf("single-user data: %w", err)
	}
	for _, m := range memberships {
		if m.Role == "owner" {
			target = m.OrgID
			break
		}
	}
	if target == "" {
		if target, err = prov.EnsurePersonalTenant(ctx, admin); err != nil {
			return fmt.Errorf("single-user data: create the admin's workspace: %w", err)
		}
	}
	rows, err := store.MoveOrgData(ctx, sqlstore.OrgMove{
		FromOrg: LocalUser.OrgID, ToOrg: target,
		FromUser: LocalUser.UserID, ToUser: admin.ID,
	})
	if err != nil {
		return fmt.Errorf("single-user data: move: %w", err)
	}
	log.Printf("auth: moved %d rows from single-user mode into the workspace of %s (%s); sign in as that account to see them",
		rows, cfg.Auth.AdminEmail, target)
	return nil
}
