package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
	"github.com/openbeehive-org/openbeehive-app/server/internal/storage/blob"
)

// TenantAPI exposes the workspaces (tenants) of an account: who am I, which
// tenants can I use, switch the active one, create and delete a tenant,
// invite users, accept an invite, and manage members (members.go).
type TenantAPI struct {
	sessions *SessionManager
	users    storage.UserRepo
	orgs     storage.OrgRepo
	members  storage.MemberRepo
	invites  storage.InviteRepo
	prov     *Provisioner
	cfg      *config.Config
	blobs    blob.Store // for purging photo blobs on tenant deletion
}

func NewTenantAPI(s *SessionManager, u storage.UserRepo, o storage.OrgRepo, m storage.MemberRepo, i storage.InviteRepo, prov *Provisioner, cfg *config.Config, blobs blob.Store) *TenantAPI {
	return &TenantAPI{sessions: s, users: u, orgs: o, members: m, invites: i, prov: prov, cfg: cfg, blobs: blobs}
}

func (t *TenantAPI) Routes(rt *Router) {
	rt.Get("/auth/me", t.me)
	rt.PostJSON("/auth/switch", t.switchTenant)
	rt.PostJSON("/tenants/create", t.create)
	rt.PostJSON("/tenants/invite", t.invite)
	rt.Get("/tenants/invites", t.listInvites)
	rt.PostJSON("/tenants/invite/revoke", t.revokeInvite)
	rt.PostJSON("/tenants/delete", t.deleteTenant)
	rt.Get("/tenants/members", t.listMembers)
	rt.PostJSON("/tenants/members/remove", t.removeMember)
	rt.PostJSON("/tenants/members/role", t.setMemberRole)
	rt.PostJSON("/tenants/leave", t.leave)
	rt.PostJSON("/auth/accept-invite", t.accept)
}

// InstanceHandler serves GET /auth/instance: the onboarding state, every
// enabled sign-in method and the map settings, so the app renders exactly
// what this server offers. It is public, and also mounted when no login is
// configured (every sign-in method then reads false).
func InstanceHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		providers := cfg.OIDC.Enabled
		if providers == nil {
			providers = []string{}
		}
		login := cfg.LoginEnabled()
		respondJSON(w, http.StatusOK, map[string]any{
			"password_auth":      cfg.Auth.PasswordEnabled,
			"registration":       login && cfg.Auth.RegistrationOpen,
			"email_verification": login && cfg.Auth.EmailVerification,
			"oidc_providers":     providers,
			"webauthn":           cfg.Auth.WebAuthn.Enabled,
			"demo":               cfg.Demo.Enabled,
			// How a forgotten password is recovered: "email" (a link is
			// mailed), "admin" (no mail server: the instance admin issues
			// the link) or "off" (no password sign-in).
			"password_reset": passwordResetMode(cfg),
			// Pure-showcase hosts: anonymous visitors are signed into the demo
			// automatically instead of seeing the login screen.
			"demo_autologin": cfg.Demo.Enabled && cfg.Demo.AutoLogin,
			// Map tiles and address search are the only remote resources the
			// app loads; switched off, both URLs are empty.
			"map": map[string]any{
				"enabled":      cfg.Maps.Enabled,
				"tile_url":     cfg.Maps.TileURL,
				"geocoder_url": cfg.Maps.GeocoderURL,
			},
		})
	}
}

func passwordResetMode(cfg *config.Config) string {
	switch {
	case !cfg.Auth.PasswordEnabled:
		return "off"
	case cfg.Auth.SMTP.Host != "":
		return "email"
	}
	return "admin"
}

func (t *TenantAPI) me(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	tenants, err := t.members.ListByUser(r.Context(), id.UserID)
	if err != nil {
		internalError(w, "list workspaces", err)
		return
	}
	list := make([]map[string]any, 0, len(tenants))
	for _, m := range tenants {
		list = append(list, map[string]any{"id": m.OrgID, "name": m.Name, "role": m.Role})
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"user_id":        id.UserID,
		"email":          id.Email,
		"instance_admin": id.Role == "admin",
		"active_org":     id.OrgID,
		"is_demo":        isDemoAccount(t.cfg, id.Email),
		"tenants":        list,
	})
}

type switchReq struct {
	OrgID string `json:"org_id"`
}

func (t *TenantAPI) switchTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	var req switchReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := t.members.Get(r.Context(), req.OrgID, id.UserID); err != nil {
		respondError(w, http.StatusForbidden, "not_a_member", "not a member of that workspace")
		return
	}
	tok, ok := t.reissue(w, id, req.OrgID)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "active_org": req.OrgID, "token": tok})
}

type createReq struct {
	Name string `json:"name"`
}

func (t *TenantAPI) create(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return
	}
	var req createReq
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "name_required", "a workspace name is required")
		return
	}
	org := &storage.Organization{ID: uuid.NewString(), Name: name, CreatedAt: time.Now().UTC()}
	if err := t.orgs.Create(r.Context(), org); err != nil {
		internalError(w, "create workspace", err)
		return
	}
	// Creator becomes the tenant admin (owner) and switches into the new tenant.
	if err := t.members.Add(r.Context(), &storage.Membership{OrgID: org.ID, UserID: id.UserID, Role: "owner"}); err != nil {
		internalError(w, "create workspace: add owner", err)
		return
	}
	tok, ok := t.reissue(w, id, org.ID)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "org_id": org.ID, "name": org.Name, "active_org": org.ID, "token": tok})
}

type inviteReq struct {
	OrgID string `json:"org_id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (t *TenantAPI) invite(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return
	}
	var req inviteReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.OrgID == "" {
		req.OrgID = id.OrgID
	}
	// Only a tenant admin (owner) may invite.
	m, err := t.members.Get(r.Context(), req.OrgID, id.UserID)
	if err != nil || m.Role != "owner" {
		respondError(w, http.StatusForbidden, "owner_only", "only a workspace admin can invite")
		return
	}
	email := storage.NormalizeEmail(req.Email)
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n") {
		respondError(w, http.StatusBadRequest, "invalid_email", "a valid email is required")
		return
	}
	role := req.Role
	if role != "owner" {
		role = "member"
	}
	now := time.Now().UTC()
	expires := now.Add(storage.InviteTTL)
	inv := &storage.Invite{ID: uuid.NewString(), OrgID: req.OrgID, Email: email, Role: role, Token: randomToken(), CreatedAt: now, ExpiresAt: &expires}
	if err := t.invites.Create(r.Context(), inv); err != nil {
		internalError(w, "create invite", err)
		return
	}
	link := t.inviteLink(inv.Token)
	// Email the invitee. The link is a credential, so it is never logged:
	// when the mail cannot be sent, the inviting admin still has it in this
	// response and in the open-invites list.
	orgName := req.OrgID
	if org, err := t.orgs.Get(r.Context(), req.OrgID); err == nil {
		orgName = org.Name
	}
	go func() {
		body := fmt.Sprintf("You have been invited to join %q on Openbeehive.\r\n\r\n"+
			"Open this link to accept the invitation (valid for 7 days):\r\n%s\r\n", orgName, link)
		if err := sendMail(t.cfg, email, "You're invited to Openbeehive", body); err != nil && !errors.Is(err, errSMTPUnset) {
			log.Printf("tenant: invite email for invite %s not sent (%v); copy the link from Settings", inv.ID, err)
		}
	}()
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "token": inv.Token, "link": link, "expires_at": inv.Expiry()})
}

func (t *TenantAPI) inviteLink(token string) string {
	return fmt.Sprintf("%s/login?invite=%s", strings.TrimRight(t.cfg.PublicBaseURL, "/"), token)
}

// requireOwner resolves the caller's identity and checks they are the admin
// (owner) of the given tenant; org defaults to the active one.
func (t *TenantAPI) requireOwner(w http.ResponseWriter, r *http.Request, orgID string) (Identity, string, bool) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return Identity{}, "", false
	}
	if orgID == "" {
		orgID = id.OrgID
	}
	m, err := t.members.Get(r.Context(), orgID, id.UserID)
	if err != nil || m.Role != "owner" {
		respondError(w, http.StatusForbidden, "owner_only", "only a workspace admin can do this")
		return Identity{}, "", false
	}
	return id, orgID, true
}

// listInvites returns the tenant's open (not yet accepted) invites, including
// the signup link so the admin can re-copy it; accepting deletes the row, so
// everything listed here is still pending. Expired invites stay listed,
// flagged, until the admin revokes them.
func (t *TenantAPI) listInvites(w http.ResponseWriter, r *http.Request) {
	_, orgID, ok := t.requireOwner(w, r, r.URL.Query().Get("org_id"))
	if !ok {
		return
	}
	invites, err := t.invites.ListByOrg(r.Context(), orgID)
	if err != nil {
		internalError(w, "list invites", err)
		return
	}
	now := time.Now()
	list := make([]map[string]any, 0, len(invites))
	for _, inv := range invites {
		list = append(list, map[string]any{
			"id":         inv.ID,
			"email":      inv.Email,
			"role":       inv.Role,
			"created_at": inv.CreatedAt,
			"expires_at": inv.Expiry(),
			"expired":    inv.Expired(now),
			"link":       t.inviteLink(inv.Token),
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"invites": list})
}

type revokeReq struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
}

func (t *TenantAPI) revokeInvite(w http.ResponseWriter, r *http.Request) {
	var req revokeReq
	if !decodeJSON(w, r, &req) {
		return
	}
	_, orgID, ok := t.requireOwner(w, r, req.OrgID)
	if !ok {
		return
	}
	// Look the invite up via the tenant's own list so an admin of tenant A
	// cannot delete tenant B's invites by guessing ids.
	invites, err := t.invites.ListByOrg(r.Context(), orgID)
	if err != nil {
		internalError(w, "list invites", err)
		return
	}
	for _, inv := range invites {
		if inv.ID == req.ID {
			if err := t.invites.Delete(r.Context(), inv.ID); err != nil {
				internalError(w, "revoke invite", err)
				return
			}
			respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
	}
	respondError(w, http.StatusNotFound, "invite_not_found", "invite not found")
}

type deleteTenantReq struct {
	OrgID string `json:"org_id"`
}

// deleteTenant permanently removes a tenant and everything in it: for every
// member, not just the caller. Owner-only. The workspace must be named in the
// body; there is no default, so no request can delete "whatever is active".
// The caller's session is moved to another tenant they belong to (a fresh
// personal one if none is left); every other session for the deleted
// workspace stops working.
func (t *TenantAPI) deleteTenant(w http.ResponseWriter, r *http.Request) {
	var req deleteTenantReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.OrgID == "" {
		respondError(w, http.StatusBadRequest, "org_id_required", "org_id is required: name the workspace to delete")
		return
	}
	id, orgID, ok := t.requireOwner(w, r, req.OrgID)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return
	}
	if err := t.removeWorkspace(r.Context(), orgID, id.UserID, false); err != nil {
		internalError(w, "delete workspace", err)
		return
	}
	t.moveSession(w, r, id)
}

// removeWorkspace deletes a workspace with everything in it and purges its
// photos. Used when an owner deletes it and when an account that was its
// only member is deleted. In the second case (onlyMember) the delete itself
// makes sure byUser still is the only member and answers
// storage.ErrNotOnlyMember otherwise.
func (t *TenantAPI) removeWorkspace(ctx context.Context, orgID, byUser string, onlyMember bool) error {
	remove := t.orgs.Delete
	if onlyMember {
		remove = func(ctx context.Context, id string) error { return t.orgs.DeleteIfOnlyMember(ctx, id, byUser) }
	}
	if err := remove(ctx, orgID); err != nil {
		return err
	}
	t.sessions.Invalidate() // members of the deleted workspace lose access now
	log.Printf("tenant: %s deleted by %s", orgID, byUser)
	// The photos go after the rows: a failed delete never loses photos.
	if t.blobs != nil {
		go t.purgeBlobs(orgID) //nolint:gosec // G118: the purge outlives the request on purpose
	}
	return nil
}

// fallbackOrg returns a workspace the account can work in: one it belongs
// to, or a fresh personal one when none is left, so an account never ends up
// without a workspace.
func (t *TenantAPI) fallbackOrg(ctx context.Context, userID string) string {
	if next := t.prov.ActiveOrg(ctx, userID); next != "" {
		return next
	}
	u, err := t.users.GetByID(ctx, userID)
	if err != nil {
		return ""
	}
	return t.prov.ResolveActiveOrg(ctx, u)
}

// moveSession answers a request that took the caller's active workspace
// away (deleted, left) with a session for another one of theirs.
func (t *TenantAPI) moveSession(w http.ResponseWriter, r *http.Request, id Identity) {
	next := t.fallbackOrg(r.Context(), id.UserID)
	tok, ok := t.reissue(w, id, next)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "active_org": next, "token": tok})
}

// purgeBlobs removes everything the deleted workspace kept in the blob
// store, in the background: the rows are already gone, so a failure only
// leaves blobs without a record, which the photo sweep removes later.
// Detached from the request on purpose.
func (t *TenantAPI) purgeBlobs(orgID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	n, err := t.blobs.DeletePrefix(ctx, blob.WorkspacePrefix(orgID))
	if err != nil {
		log.Printf("tenant: %s: purging photo blobs stopped after %d: %v", orgID, n, err)
		return
	}
	log.Printf("tenant: %s: purged %d photo blobs", orgID, n)
}

type acceptReq struct {
	Token string `json:"token"`
}

// accept joins the workspace an invite was issued for. The invite only works
// for the account whose email it names: a forwarded or leaked link cannot be
// redeemed by someone else.
func (t *TenantAPI) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return
	}
	var req acceptReq
	if !decodeJSON(w, r, &req) {
		return
	}
	inv, ok := usableInvite(w, r, t.invites, req.Token)
	if !ok {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(inv.Email), strings.TrimSpace(id.Email)) {
		respondError(w, http.StatusForbidden, "invite_email_mismatch", "this invite was issued for a different email address")
		return
	}
	if _, err := t.members.Get(r.Context(), inv.OrgID, id.UserID); err != nil {
		// not yet a member -> add
		err := t.members.Add(r.Context(), &storage.Membership{OrgID: inv.OrgID, UserID: id.UserID, Role: inv.Role})
		if errors.Is(err, storage.ErrNotFound) {
			// The workspace was deleted while the invite was being accepted.
			respondError(w, http.StatusBadRequest, "invite_invalid", "this invite is not valid; ask for a new one")
			return
		}
		if err != nil {
			internalError(w, "accept invite", err)
			return
		}
		t.sessions.Invalidate() // a cached "not a member" must not outlive the join
	}
	_ = t.invites.Delete(r.Context(), inv.ID)
	tok, ok := t.reissue(w, id, inv.OrgID) // switch into the joined tenant
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "org_id": inv.OrgID, "active_org": inv.OrgID, "token": tok})
}

// usableInvite looks an invite up by its token and answers 400 when it does
// not exist or has expired.
func usableInvite(w http.ResponseWriter, r *http.Request, invites storage.InviteRepo, token string) (*storage.Invite, bool) {
	inv, err := invites.GetByToken(r.Context(), strings.TrimSpace(token))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		respondError(w, http.StatusBadRequest, "invite_invalid", "this invite is not valid; ask for a new one")
		return nil, false
	case err != nil:
		internalError(w, "look up invite", err)
		return nil, false
	case inv.Expired(time.Now()):
		respondError(w, http.StatusBadRequest, "invite_expired", "this invite has expired; ask for a new one")
		return nil, false
	}
	return inv, true
}

// reissue mints a fresh session for the same user with a new active tenant,
// sets the cookie and returns the token (for Bearer clients). On failure it
// writes the error response and returns false.
func (t *TenantAPI) reissue(w http.ResponseWriter, id Identity, orgID string) (string, bool) {
	tok, err := t.sessions.Issue(Identity{UserID: id.UserID, OrgID: orgID, Email: id.Email, Role: id.Role})
	if err != nil {
		internalError(w, "issue session", err)
		return "", false
	}
	t.sessions.SetCookie(w, tok)
	return tok, true
}
