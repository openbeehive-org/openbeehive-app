package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openbeehive-org/openbeehive-app/server/internal/storage"
)

// inviteInfo serves GET /auth/invite?token=…: what the sign-in page shows
// before anyone signs in, so the invited person knows which workspace the
// link is for and with which address to sign in. The token is the
// credential; whoever holds the link learns the workspace name and a masked
// address, nothing more. Unknown and expired tokens get the same error codes
// as accepting them.
func (t *TenantAPI) inviteInfo(w http.ResponseWriter, r *http.Request) {
	inv, ok := usableInvite(w, r, t.invites, r.URL.Query().Get("token"))
	if !ok {
		return
	}
	org, err := t.orgs.Get(r.Context(), inv.OrgID)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		// The workspace was deleted; the invite can no longer be used.
		respondError(w, http.StatusBadRequest, "invite_invalid", "this invite is not valid; ask for a new one")
		return
	case err != nil:
		internalError(w, "look up invite workspace", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"workspace":  org.Name,
		"email":      maskEmail(inv.Email),
		"expires_at": inv.Expiry(),
	})
}

// maskEmail keeps enough of an address to recognise it and hides the rest:
// "beekeeper@example.org" -> "b…@e….org". A name without a dot keeps only its
// first letter.
func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return "…"
	}
	local, domain := email[:at], email[at+1:]
	masked := firstRune(local) + "…@" + firstRune(domain) + "…"
	if dot := strings.LastIndex(domain, "."); dot > 0 && dot < len(domain)-1 {
		masked += domain[dot:]
	}
	return masked
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}
