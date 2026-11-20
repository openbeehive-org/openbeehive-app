package auth

import (
	"errors"
	"log"
	"net/http"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// listMembers returns the members of a workspace the caller belongs to (the
// active one unless org_id names another). Owners get everyone's email
// address and role; other members see names only.
func (t *TenantAPI) listMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		orgID = id.OrgID
	}
	self, err := t.members.Get(r.Context(), orgID, id.UserID)
	if err != nil {
		respondError(w, http.StatusForbidden, "not_a_member", "not a member of that workspace")
		return
	}
	members, err := t.members.ListByOrg(r.Context(), orgID)
	if err != nil {
		internalError(w, "list members", err)
		return
	}
	owner := self.Role == "owner"
	list := make([]map[string]any, 0, len(members))
	for _, m := range members {
		entry := map[string]any{"user_id": m.UserID, "name": m.Name, "me": m.UserID == id.UserID}
		if owner || m.UserID == id.UserID {
			entry["email"], entry["role"] = m.Email, m.Role
		}
		list = append(list, entry)
	}
	respondJSON(w, http.StatusOK, map[string]any{"org_id": orgID, "can_manage": owner, "members": list})
}

type memberReq struct {
	OrgID  string `json:"org_id"`
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// memberChangeFailed answers a refused membership change; it returns false
// when err is nil and the handler should go on.
func memberChangeFailed(w http.ResponseWriter, what string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, storage.ErrLastOwner):
		respondError(w, http.StatusConflict, "last_owner", "a workspace needs at least one admin; make another member an admin first")
	case errors.Is(err, storage.ErrNotFound):
		respondError(w, http.StatusNotFound, "member_not_found", "not a member of this workspace")
	default:
		internalError(w, what, err)
	}
	return true
}

// ownerRequest decodes a membership change and checks that the caller is an
// owner of the workspace it names and not the read-only demo account.
func (t *TenantAPI) ownerRequest(w http.ResponseWriter, r *http.Request) (Identity, memberReq, bool) {
	var req memberReq
	if !decodeJSON(w, r, &req) {
		return Identity{}, req, false
	}
	id, orgID, ok := t.requireOwner(w, r, req.OrgID)
	if !ok {
		return Identity{}, req, false
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return Identity{}, req, false
	}
	req.OrgID = orgID
	return id, req, true
}

// removeMember ends another member's membership (owner only). Their sessions
// and API keys for the workspace stop working at once; their records stay.
func (t *TenantAPI) removeMember(w http.ResponseWriter, r *http.Request) {
	id, req, ok := t.ownerRequest(w, r)
	if !ok {
		return
	}
	if req.UserID == id.UserID {
		respondError(w, http.StatusBadRequest, "cannot_remove_self", "to end your own membership, leave the workspace")
		return
	}
	if memberChangeFailed(w, "remove member", t.members.Remove(r.Context(), req.OrgID, req.UserID)) {
		return
	}
	t.sessions.Invalidate()
	log.Printf("tenant: %s: member %s removed by %s", req.OrgID, req.UserID, id.UserID)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// setMemberRole makes a member an admin (owner) or an ordinary member (owner
// only). The last owner cannot be demoted.
func (t *TenantAPI) setMemberRole(w http.ResponseWriter, r *http.Request) {
	id, req, ok := t.ownerRequest(w, r)
	if !ok {
		return
	}
	if req.Role != "owner" && req.Role != "member" {
		respondError(w, http.StatusBadRequest, "invalid_role", "role must be \"owner\" or \"member\"")
		return
	}
	if memberChangeFailed(w, "change role", t.members.SetRole(r.Context(), req.OrgID, req.UserID, req.Role)) {
		return
	}
	log.Printf("tenant: %s: member %s is now %s (changed by %s)", req.OrgID, req.UserID, req.Role, id.UserID)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "role": req.Role})
}

// leave ends the caller's own membership. The workspace must be named in the
// body. The last owner cannot leave: they hand the role over first, or delete
// the workspace. The caller's session moves to another workspace of theirs.
func (t *TenantAPI) leave(w http.ResponseWriter, r *http.Request) {
	id, ok := t.sessions.Authenticate(w, r)
	if !ok {
		return
	}
	if id.Role == "demo" {
		demoReadOnly(w)
		return
	}
	var req memberReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.OrgID == "" {
		respondError(w, http.StatusBadRequest, "org_id_required", "org_id is required: name the workspace to leave")
		return
	}
	if memberChangeFailed(w, "leave workspace", t.members.Remove(r.Context(), req.OrgID, id.UserID)) {
		return
	}
	t.sessions.Invalidate()
	log.Printf("tenant: %s: member %s left", req.OrgID, id.UserID)
	t.moveSession(w, r, id)
}
