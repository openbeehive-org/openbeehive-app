package auth

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// Provisioner creates and links users and their tenants (organizations).
// A new account always gets a personal tenant it owns; it may join others.
type Provisioner struct {
	users   storage.UserRepo
	orgs    storage.OrgRepo
	members storage.MemberRepo
}

func NewProvisioner(u storage.UserRepo, o storage.OrgRepo, m storage.MemberRepo) *Provisioner {
	return &Provisioner{users: u, orgs: o, members: m}
}

// EnsurePersonalTenant creates a personal tenant for a fresh user and makes them
// its owner (tenant admin). Returns the tenant (organization) id.
func (p *Provisioner) EnsurePersonalTenant(ctx context.Context, u *storage.User) (string, error) {
	name := u.Name
	if name == "" {
		name = "My apiaries"
	}
	org := &storage.Organization{ID: uuid.NewString(), Name: name, CreatedAt: time.Now().UTC()}
	if err := p.orgs.Create(ctx, org); err != nil {
		return "", err
	}
	if err := p.members.Add(ctx, &storage.Membership{OrgID: org.ID, UserID: u.ID, Role: "owner"}); err != nil {
		return "", err
	}
	return org.ID, nil
}

// ActiveOrg returns the user's default active tenant (first membership), or ""
// if they belong to none.
func (p *Provisioner) ActiveOrg(ctx context.Context, userID string) string {
	ms, _ := p.members.ListByUser(ctx, userID)
	if len(ms) > 0 {
		return ms[0].OrgID
	}
	return ""
}

// ResolveActiveOrg returns the user's active tenant, creating a personal one if
// they somehow have none yet.
func (p *Provisioner) ResolveActiveOrg(ctx context.Context, u *storage.User) string {
	if org := p.ActiveOrg(ctx, u.ID); org != "" {
		return org
	}
	org, _ := p.EnsurePersonalTenant(ctx, u)
	return org
}
