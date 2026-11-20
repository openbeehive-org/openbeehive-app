package auth

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// reauthTTL is how long a proof of a fresh passkey assertion can be spent.
const reauthTTL = 5 * time.Minute

// reauthMax caps the stored proofs; past it the oldest state is dropped.
const reauthMax = 10_000

// ReauthStore holds short-lived, single-use proofs that an account has just
// confirmed its identity with a passkey. Accounts without a password spend
// one to authorise what a password would otherwise authorise (deleting the
// account). Proofs live in memory: they are worthless after five minutes.
type ReauthStore struct {
	mu     sync.Mutex
	now    func() time.Time
	proofs map[string]reauthProof // token -> proof
}

type reauthProof struct {
	userID  string
	expires time.Time
}

func NewReauthStore() *ReauthStore {
	return &ReauthStore{now: time.Now, proofs: map[string]reauthProof{}}
}

// issue returns a new proof for the account.
func (s *ReauthStore) issue(userID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for token, p := range s.proofs {
		if !now.Before(p.expires) {
			delete(s.proofs, token)
		}
	}
	if len(s.proofs) >= reauthMax {
		s.proofs = map[string]reauthProof{}
	}
	token := randomToken()
	s.proofs[token] = reauthProof{userID: userID, expires: now.Add(reauthTTL)}
	return token
}

// consume spends a proof: true exactly once, for the account it was issued
// to, within its lifetime.
func (s *ReauthStore) consume(userID, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.proofs[token]
	if !ok {
		return false
	}
	delete(s.proofs, token)
	return p.userID == userID && s.now().Before(p.expires)
}

// reauthBegin starts a passkey assertion for the signed-in account, limited
// to the passkeys enrolled for it.
func (a *WebAuthn) reauthBegin(w http.ResponseWriter, r *http.Request) {
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	u, err := a.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		notSignedIn(w) // the account behind the session is gone
		return
	}
	user := &waUser{u: u, creds: a.credentials(r, u.ID)}
	if len(user.creds) == 0 {
		respondError(w, http.StatusConflict, "passkey_none", "this account has no passkey")
		return
	}
	options, session, err := a.wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		internalError(w, "passkey confirmation", err)
		return
	}
	a.storeChallenge(w, &waChallenge{data: session})
	respondJSON(w, http.StatusOK, options)
}

// reauthFinish verifies the assertion and returns a proof that is good for
// one confirmation within five minutes.
func (a *WebAuthn) reauthFinish(w http.ResponseWriter, r *http.Request) {
	id, ok := a.identity(w, r)
	if !ok {
		return
	}
	ch, ok := a.loadChallenge(r)
	if !ok {
		respondError(w, http.StatusBadRequest, "passkey_no_ceremony", "no passkey confirmation in progress; start again")
		return
	}
	// The challenge must belong to the session that finishes it.
	if string(ch.data.UserID) != id.UserID {
		respondError(w, http.StatusForbidden, "passkey_session_mismatch", "this confirmation was started by a different account")
		return
	}
	u, err := a.users.GetByID(r.Context(), id.UserID)
	if err != nil {
		notSignedIn(w)
		return
	}
	cred, err := a.wa.FinishLogin(&waUser{u: u, creds: a.credentials(r, u.ID)}, *ch.data, r)
	if err != nil {
		log.Printf("webauthn: confirmation rejected: %v", err)
		respondError(w, http.StatusForbidden, "passkey_failed", "the passkey did not confirm your identity; try again")
		return
	}
	a.touchCredential(r, cred)
	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "reauth": a.reauth.issue(u.ID)})
}
