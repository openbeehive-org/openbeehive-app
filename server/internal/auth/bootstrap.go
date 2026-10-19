package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
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
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.Auth.AdminPassword), bcrypt.DefaultCost)
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
