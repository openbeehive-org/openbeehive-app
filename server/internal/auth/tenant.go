package auth

import (
	"context"
	"encoding/json"
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

// TenantAPI exposes multi-tenant onboarding: who am I, which tenants can I use,
// switch the active tenant, create a tenant, invite users, accept an invite.
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

func (t *TenantAPI) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/instance", t.instance)
	mux.HandleFunc("/auth/me", t.me)
	mux.HandleFunc("/auth/switch", t.switchTenant)
	mux.HandleFunc("/tenants/create", t.create)
	mux.HandleFunc("/tenants/invite", t.invite)
	mux.HandleFunc("/tenants/invites", t.listInvites)
	mux.HandleFunc("/tenants/invite/revoke", t.revokeInvite)
	mux.HandleFunc("/tenants/delete", t.deleteTenant)
	mux.HandleFunc("/auth/accept-invite", t.accept)
}

// instance advertises onboarding state + every enabled sign-in method, so the
// login screen can render exactly what's available.
func (t *TenantAPI) instance(w http.ResponseWriter, r *http.Request) {
	providers := t.cfg.OIDC.Enabled
	if providers == nil {
		providers = []string{}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"password_auth":      t.cfg.Auth.PasswordEnabled,
		"registration":       t.cfg.Auth.RegistrationOpen,
		"email_verification": t.cfg.Auth.EmailVerification,
		"oidc_providers":     providers,
		"webauthn":           t.cfg.Auth.WebAuthn.Enabled,
		"demo":               t.cfg.Demo.Enabled,
		// Pure-showcase hosts: anonymous visitors are signed into the demo
		// automatically instead of seeing the login screen.
		"demo_autologin": t.cfg.Demo.Enabled && t.cfg.Demo.AutoLogin,
	})
}

func (t *TenantAPI) me(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return
	}
	tenants, _ := t.members.ListByUser(r.Context(), id.UserID)
	list := make([]map[string]any, 0, len(tenants))
	for _, m := range tenants {
		list = append(list, map[string]any{"id": m.OrgID, "name": m.Name, "role": m.Role})
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"user_id":        id.UserID,
		"email":          id.Email,
		"instance_admin": id.Role == "admin",
		"active_org":     id.OrgID,
		"is_demo":        t.cfg.Demo.Enabled && strings.EqualFold(id.Email, t.cfg.Demo.Email),
		"tenants":        list,
	})
}

type switchReq struct {
	OrgID string `json:"org_id"`
}

func (t *TenantAPI) switchTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return
	}
	var req switchReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if _, err := t.members.Get(r.Context(), req.OrgID, id.UserID); err != nil {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "not a member of that tenant"})
		return
	}
	tok := t.reissue(w, id, req.OrgID)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "active_org": req.OrgID, "token": tok})
}

type createReq struct {
	Name string `json:"name"`
}

func (t *TenantAPI) create(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return
	}
	if id.Role == "demo" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": ErrDemoReadOnly.Error()})
		return
	}
	var req createReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "a tenant name is required"})
		return
	}
	org := &storage.Organization{ID: uuid.NewString(), Name: name, CreatedAt: time.Now().UTC()}
	if err := t.orgs.Create(r.Context(), org); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Creator becomes the tenant admin (owner) and switches into the new tenant.
	if err := t.members.Add(r.Context(), &storage.Membership{OrgID: org.ID, UserID: id.UserID, Role: "owner"}); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	tok := t.reissue(w, id, org.ID)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "org_id": org.ID, "name": org.Name, "active_org": org.ID, "token": tok})
}

type inviteReq struct {
	OrgID string `json:"org_id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (t *TenantAPI) invite(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return
	}
	if id.Role == "demo" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": ErrDemoReadOnly.Error()})
		return
	}
	var req inviteReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.OrgID == "" {
		req.OrgID = id.OrgID
	}
	// Only a tenant admin (owner) may invite.
	m, err := t.members.Get(r.Context(), req.OrgID, id.UserID)
	if err != nil || m.Role != "owner" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "only the tenant admin can invite"})
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if !strings.Contains(email, "@") {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "a valid email is required"})
		return
	}
	role := req.Role
	if role != "owner" {
		role = "member"
	}
	inv := &storage.Invite{ID: uuid.NewString(), OrgID: req.OrgID, Email: email, Role: role, Token: randomToken(), CreatedAt: time.Now().UTC()}
	if err := t.invites.Create(r.Context(), inv); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	link := t.inviteLink(inv.Token)
	// Email the invitee; when SMTP is unset (or fails) log the link instead so
	// it stays recoverable — the UI shows it to the inviting admin either way.
	orgName := req.OrgID
	if org, err := t.orgs.Get(r.Context(), req.OrgID); err == nil {
		orgName = org.Name
	}
	go func() {
		body := fmt.Sprintf("You have been invited to join %q on Openbeehive.\r\n\r\n"+
			"Open this link to accept the invitation:\r\n%s\r\n", orgName, link)
		if err := sendMail(t.cfg, email, "You're invited to Openbeehive", body); err != nil {
			log.Printf("tenant: invite email to %s not sent (%v); link: %s", email, err, link)
		}
	}()
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "token": inv.Token, "link": link})
}

func (t *TenantAPI) inviteLink(token string) string {
	return fmt.Sprintf("%s/login?invite=%s", strings.TrimRight(t.cfg.PublicBaseURL, "/"), token)
}

// requireOwner resolves the caller's identity and checks they are the admin
// (owner) of the given tenant; org defaults to the active one.
func (t *TenantAPI) requireOwner(w http.ResponseWriter, r *http.Request, orgID string) (Identity, string, bool) {
	id, ok := t.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return Identity{}, "", false
	}
	if orgID == "" {
		orgID = id.OrgID
	}
	m, err := t.members.Get(r.Context(), orgID, id.UserID)
	if err != nil || m.Role != "owner" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": "only the tenant admin can manage invites"})
		return Identity{}, "", false
	}
	return id, orgID, true
}

// listInvites returns the tenant's open (not yet accepted) invites, including
// the signup link so the admin can re-copy it — accepting deletes the row, so
// everything listed here is still pending.
func (t *TenantAPI) listInvites(w http.ResponseWriter, r *http.Request) {
	_, orgID, ok := t.requireOwner(w, r, r.URL.Query().Get("org_id"))
	if !ok {
		return
	}
	invites, err := t.invites.ListByOrg(r.Context(), orgID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	list := make([]map[string]any, 0, len(invites))
	for _, inv := range invites {
		list = append(list, map[string]any{
			"id":         inv.ID,
			"email":      inv.Email,
			"role":       inv.Role,
			"created_at": inv.CreatedAt,
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
	_ = json.NewDecoder(r.Body).Decode(&req)
	_, orgID, ok := t.requireOwner(w, r, req.OrgID)
	if !ok {
		return
	}
	// Look the invite up via the tenant's own list so an admin of tenant A
	// cannot delete tenant B's invites by guessing ids.
	invites, err := t.invites.ListByOrg(r.Context(), orgID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for _, inv := range invites {
		if inv.ID == req.ID {
			if err := t.invites.Delete(r.Context(), inv.ID); err != nil {
				respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
	}
	respondJSON(w, http.StatusNotFound, map[string]any{"error": "invite not found"})
}

type deleteTenantReq struct {
	OrgID string `json:"org_id"`
}

// deleteTenant permanently removes a tenant and everything in it — for every
// member, not just the caller. Owner-only. The caller's session is moved to
// another tenant they belong to (a fresh personal one if none is left).
func (t *TenantAPI) deleteTenant(w http.ResponseWriter, r *http.Request) {
	var req deleteTenantReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	id, orgID, ok := t.requireOwner(w, r, req.OrgID)
	if !ok {
		return
	}
	if id.Role == "demo" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": ErrDemoReadOnly.Error()})
		return
	}
	// Collect blob keys BEFORE the rows go away; purge after the delete
	// commits so a failed delete never loses photos.
	photoKeys, err := t.orgs.PhotoKeysByOrg(r.Context(), orgID)
	if err != nil {
		log.Printf("tenant: %s: listing photo keys failed (%v); blobs will be orphaned", orgID, err) //nolint:gosec // G706: orgID is an existing organization id (requireOwner)
	}
	if err := t.orgs.Delete(r.Context(), orgID); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	log.Printf("tenant: %s deleted by %s", orgID, id.UserID) //nolint:gosec // G706: ids come from the verified session and the owner check
	if t.blobs != nil && len(photoKeys) > 0 {
		go t.purgeBlobs(orgID, photoKeys) //nolint:gosec // G118: the purge outlives the request on purpose
	}
	next := t.prov.ActiveOrg(r.Context(), id.UserID)
	if next == "" {
		// The caller deleted their last tenant — recreate a personal one so
		// the account never ends up orphaned.
		if u, err := t.users.GetByID(r.Context(), id.UserID); err == nil {
			next = t.prov.ResolveActiveOrg(r.Context(), u)
		}
	}
	tok := t.reissue(w, id, next)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "active_org": next, "token": tok})
}

// purgeBlobs deletes a deleted tenant's photos, best-effort in the background:
// the rows are already gone, so a blob failure only leaves an orphan (logged),
// never a broken tenant. Detached from the request on purpose.
func (t *TenantAPI) purgeBlobs(orgID string, keys []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	failed := 0
	for _, key := range keys {
		if err := t.blobs.Delete(ctx, key); err != nil {
			failed++
			log.Printf("tenant: %s: blob %s not deleted: %v", orgID, key, err) //nolint:gosec // G706: orgID and key come from our own rows
		}
	}
	log.Printf("tenant: %s: purged %d/%d photo blobs", orgID, len(keys)-failed, len(keys)) //nolint:gosec // G706: orgID is an existing organization id (requireOwner)
}

type acceptReq struct {
	Token string `json:"token"`
}

func (t *TenantAPI) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.IdentityFromRequest(r)
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]any{"error": "not signed in"})
		return
	}
	if id.Role == "demo" {
		respondJSON(w, http.StatusForbidden, map[string]any{"error": ErrDemoReadOnly.Error()})
		return
	}
	var req acceptReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	inv, err := t.invites.GetByToken(r.Context(), req.Token)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid or expired invite"})
		return
	}
	if _, err := t.members.Get(r.Context(), inv.OrgID, id.UserID); err != nil {
		// not yet a member -> add
		if err := t.members.Add(r.Context(), &storage.Membership{OrgID: inv.OrgID, UserID: id.UserID, Role: inv.Role}); err != nil {
			respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	_ = t.invites.Delete(r.Context(), inv.ID)
	tok := t.reissue(w, id, inv.OrgID) // switch into the joined tenant
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "org_id": inv.OrgID, "active_org": inv.OrgID, "token": tok})
}

// reissue mints a fresh session for the same user with a new active tenant,
// sets the cookie and returns the token (for Bearer clients).
func (t *TenantAPI) reissue(w http.ResponseWriter, id Identity, orgID string) string {
	tok, err := t.sessions.Issue(Identity{UserID: id.UserID, OrgID: orgID, Email: id.Email, Role: id.Role})
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return ""
	}
	t.sessions.SetCookie(w, tok)
	return tok
}
