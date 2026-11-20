package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// AccountStore is the storage side of account deletion.
type AccountStore interface {
	DeleteAccount(ctx context.Context, userID string) error
	PasskeyCount(ctx context.Context, userID string) (int, error)
}

// AccountAPI serves what a signed-in user does with their own account
// besides signing in: look at what deleting it would do, delete it, and get
// the session back on its feet after losing its workspace.
type AccountAPI struct {
	t        *TenantAPI
	store    AccountStore
	throttle *Throttle
	reauth   *ReauthStore
}

func NewAccountAPI(t *TenantAPI, store AccountStore, throttle *Throttle, reauth *ReauthStore) *AccountAPI {
	return &AccountAPI{t: t, store: store, throttle: throttle, reauth: reauth}
}

func (a *AccountAPI) Routes(rt *Router) {
	rt.Get("/auth/account", a.info)
	rt.PostJSON("/auth/account/delete", a.remove)
	rt.Post("/auth/session/recover", a.recoverSession)
}

type workspaceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Members is the number of members including the account itself.
	Members int `json:"members"`
}

// deletionPlan says what deleting an account would do to each workspace it
// belongs to, and whether it is possible at all.
type deletionPlan struct {
	// Protected names why the account cannot be deleted through the API:
	// "instance_admin" (defined by the server configuration) or "demo".
	Protected string `json:"protected"`
	// Deleted with the account: workspaces it is the only member of.
	Deletes []workspaceRef `json:"deletes"`
	// Blocking: workspaces with other members where it is the only owner.
	Blocked []workspaceRef `json:"blocked"`
	// Left behind: the account's records there stay, without its name.
	Leaves []workspaceRef `json:"leaves"`
	// Confirm is how the deletion must be confirmed: "password", "passkey",
	// or, for an account that has neither (it signs in with a provider),
	// "login": a sign-in within the last five minutes.
	Confirm string `json:"confirm"`
}

func (a *AccountAPI) plan(ctx context.Context, u *storage.User) (deletionPlan, error) {
	p := deletionPlan{Deletes: []workspaceRef{}, Blocked: []workspaceRef{}, Leaves: []workspaceRef{}}
	switch {
	case isDemoAccount(a.t.cfg, u.Email):
		p.Protected = "demo"
	case isEnvAdmin(a.t.cfg, u.Email):
		p.Protected = "instance_admin"
	}
	memberships, err := a.t.members.ListByUser(ctx, u.ID)
	if err != nil {
		return p, err
	}
	for _, m := range memberships {
		members, err := a.t.members.ListByOrg(ctx, m.OrgID)
		if err != nil {
			return p, err
		}
		otherOwner := false
		for _, other := range members {
			if other.UserID != u.ID && other.Role == "owner" {
				otherOwner = true
			}
		}
		ref := workspaceRef{ID: m.OrgID, Name: m.Name, Members: len(members)}
		switch {
		case len(members) <= 1:
			p.Deletes = append(p.Deletes, ref)
		case m.Role == "owner" && !otherOwner:
			p.Blocked = append(p.Blocked, ref)
		default:
			p.Leaves = append(p.Leaves, ref)
		}
	}
	p.Confirm = "password"
	if u.PasswordHash == "" {
		n, err := a.store.PasskeyCount(ctx, u.ID)
		if err != nil {
			return p, err
		}
		p.Confirm = "login"
		if n > 0 {
			p.Confirm = "passkey"
		}
	}
	return p, nil
}

// account loads the signed-in account; on failure it writes the error
// response and returns false.
func (a *AccountAPI) account(w http.ResponseWriter, r *http.Request) (Identity, *storage.User, bool) {
	id, ok := a.t.sessions.Authenticate(w, r)
	if !ok {
		return Identity{}, nil, false
	}
	u, err := a.t.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		notSignedIn(w) // the account behind the session is gone
		return Identity{}, nil, false
	}
	return id, u, true
}

// info describes the signed-in account for the Settings page: how its
// password is managed and what deleting it would do.
func (a *AccountAPI) info(w http.ResponseWriter, r *http.Request) {
	_, u, ok := a.account(w, r)
	if !ok {
		return
	}
	plan, err := a.plan(r.Context(), u)
	if err != nil {
		internalError(w, "plan account deletion", err)
		return
	}
	// "set": can be changed with the current one. "none": no password yet.
	// "managed": the server configuration defines it. "off": no password
	// sign-in on this instance.
	password := "set"
	switch {
	case !a.t.cfg.Auth.PasswordEnabled:
		password = "off"
	case plan.Protected != "":
		password = "managed"
	case u.PasswordHash == "":
		password = "none"
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"user_id": u.ID, "email": u.Email, "name": u.Name,
		"password": password, "deletion": plan,
	})
}

type deleteAccountReq struct {
	Password string `json:"password"`
	// Reauth is the proof from /auth/webauthn/reauth/finish, for accounts
	// that have no password.
	Reauth string `json:"reauth"`
}

// remove deletes the caller's account.
//
//   - Workspaces the account is the only member of are deleted with it.
//   - It is refused while the account is the only owner of a workspace that
//     has other members: they would be left with a workspace nobody can
//     administer.
//   - Its records in other workspaces stay. They keep the account's id as
//     author, which then names no one; everything that identifies the person
//     (email, name, passkeys, provider link, API keys, personal tasks) goes.
//   - The instance admin from the server configuration and the demo account
//     cannot be deleted here.
//
// The caller confirms with the account's password, or with a fresh passkey
// assertion when the account has none, or, when it has neither, by having
// signed in within the last five minutes.
func (a *AccountAPI) remove(w http.ResponseWriter, r *http.Request) {
	id, u, ok := a.account(w, r)
	if !ok {
		return
	}
	var req deleteAccountReq
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := a.plan(r.Context(), u)
	if err != nil {
		internalError(w, "plan account deletion", err)
		return
	}
	switch plan.Protected {
	case "demo":
		demoReadOnly(w)
		return
	case "instance_admin":
		respondError(w, http.StatusForbidden, "account_protected", "the instance admin account is defined by the server configuration and cannot be deleted here")
		return
	}
	if !a.confirmed(w, r, id, u, plan.Confirm, req) {
		return
	}
	if len(plan.Blocked) > 0 {
		soleOwner(w, plan)
		return
	}
	for _, ws := range plan.Deletes {
		err := a.t.removeWorkspace(r.Context(), ws.ID, u.ID, true)
		if errors.Is(err, storage.ErrNotOnlyMember) {
			// Somebody joined after the plan was made. The workspace and
			// their records stay; the account now has to hand it over.
			if plan, err = a.plan(r.Context(), u); err != nil {
				internalError(w, "plan account deletion", err)
				return
			}
			soleOwner(w, plan)
			return
		}
		if err != nil {
			internalError(w, "delete workspace with account", err)
			return
		}
	}
	if err := a.store.DeleteAccount(r.Context(), u.ID); err != nil {
		internalError(w, "delete account", err)
		return
	}
	a.t.sessions.Invalidate() // every session of the account ends now
	clearSessionCookie(w)
	log.Printf("account: %s deleted on its owner's request (%d workspaces deleted with it, %d left)", id.UserID, len(plan.Deletes), len(plan.Leaves))
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// soleOwner refuses the deletion and names the workspaces that have to be
// handed over or deleted first.
func soleOwner(w http.ResponseWriter, plan deletionPlan) {
	respondJSON(w, http.StatusConflict, map[string]any{
		"error":      "you are the only admin of a workspace that has other members; make someone else an admin or delete the workspace first",
		"code":       "sole_owner",
		"workspaces": plan.Blocked,
	})
}

// confirmed checks the proof that the account's owner is the one asking. On
// failure it writes the error response and returns false.
func (a *AccountAPI) confirmed(w http.ResponseWriter, r *http.Request, id Identity, u *storage.User, method string, req deleteAccountReq) bool {
	switch method {
	case "password":
		account := storage.NormalizeEmail(u.Email)
		if !a.throttle.Allow(w, r, account) {
			return false
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
			a.throttle.Fail(r, account)
			respondError(w, http.StatusForbidden, "wrong_password", "the password is not correct")
			return false
		}
		return true
	case "passkey":
		if a.reauth.consume(u.ID, req.Reauth) {
			return true
		}
		respondError(w, http.StatusForbidden, "reauth_required", "confirm with one of your passkeys first")
		return false
	}
	// Neither password nor passkey: the account signs in with a provider.
	// The session of a sign-in that has just happened is the confirmation;
	// one that was reissued since (another workspace, a recovery) is not,
	// because nobody proved their identity for it.
	if RecentLogin(id, time.Now()) {
		return true
	}
	respondError(w, http.StatusForbidden, "login_required", "sign in again to confirm: deleting this account needs a sign-in from the last five minutes")
	return false
}

// recoverSession gives a session whose workspace is gone for this account (removed
// from it, or it was deleted) a new one for another workspace the account
// belongs to, or for a fresh personal workspace when none is left. A session
// that is still good is answered as it is. Everything else that invalidates
// a session (expiry, revocation, deleted account) stays a 401.
func (a *AccountAPI) recoverSession(w http.ResponseWriter, r *http.Request) {
	id, member, ok := a.t.sessions.authenticateAccount(w, r)
	if !ok {
		return
	}
	if member {
		respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "active_org": id.OrgID, "switched": false})
		return
	}
	next := a.t.fallbackOrg(r.Context(), id.UserID)
	if next == "" {
		notSignedIn(w)
		return
	}
	a.t.sessions.Invalidate() // a personal workspace may have just been created
	tok, ok := a.t.reissue(w, id, next)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "active_org": next, "token": tok, "switched": true})
}
