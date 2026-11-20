package auth

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Sign-in throttling: failed attempts are counted per account and per client
// address in fixed windows. Counters live in memory, which is enough for the
// single server instance Openbeehive runs as.
const (
	throttleWindow      = 15 * time.Minute
	throttlePerAccount  = 10
	throttlePerIP       = 30
	throttleMaxCounters = 100_000
)

type attemptWindow struct {
	count int
	reset time.Time
}

// attemptLimiter blocks a key once it has collected max failures within one
// window; the block lifts when the window ends.
type attemptLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	hits   map[string]*attemptWindow
}

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: limit, window: window, now: time.Now, hits: map[string]*attemptWindow{}}
}

// blockedFor returns how long the key stays blocked; 0 means it may try.
func (l *attemptLimiter) blockedFor(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[key]
	if !ok {
		return 0
	}
	now := l.now()
	if !now.Before(w.reset) {
		delete(l.hits, key)
		return 0
	}
	if w.count < l.max {
		return 0
	}
	return w.reset.Sub(now)
}

func (l *attemptLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if w, ok := l.hits[key]; ok && now.Before(w.reset) {
		w.count++
		return
	}
	if len(l.hits) >= throttleMaxCounters {
		l.prune(now)
	}
	l.hits[key] = &attemptWindow{count: 1, reset: now.Add(l.window)}
}

func (l *attemptLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// prune drops finished windows; if the table is still full (a flood from
// more keys than it holds) it starts over rather than grow without bound.
func (l *attemptLimiter) prune(now time.Time) {
	for k, w := range l.hits {
		if !now.Before(w.reset) {
			delete(l.hits, k)
		}
	}
	if len(l.hits) >= throttleMaxCounters {
		l.hits = map[string]*attemptWindow{}
	}
}

// Throttle slows down password guessing and sign-up abuse: at most 10 failed
// attempts per account and 30 per client address in 15 minutes. One instance
// is shared by every sign-in method so the address budget is a single one.
type Throttle struct {
	accounts   *attemptLimiter
	addresses  *attemptLimiter
	trustProxy bool
}

// NewThrottle creates the shared limiter. trustProxy takes the client
// address from X-Forwarded-For (BEEHIVE_TRUST_PROXY_HEADERS).
func NewThrottle(trustProxy bool) *Throttle {
	return &Throttle{
		accounts:   newAttemptLimiter(throttlePerAccount, throttleWindow),
		addresses:  newAttemptLimiter(throttlePerIP, throttleWindow),
		trustProxy: trustProxy,
	}
}

// Allow reports whether the request may attempt to sign in as account (empty
// when the method has no account name up front). When the account or the
// address is blocked it answers 429 with Retry-After and returns false.
func (t *Throttle) Allow(w http.ResponseWriter, r *http.Request, account string) bool {
	wait := t.addresses.blockedFor(ClientIP(r, t.trustProxy))
	if account != "" {
		wait = max(wait, t.accounts.blockedFor(account))
	}
	if wait <= 0 {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	respondError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
	return false
}

// Fail records a failed attempt against the client address and the account.
func (t *Throttle) Fail(r *http.Request, account string) {
	t.addresses.fail(ClientIP(r, t.trustProxy))
	if account != "" {
		t.accounts.fail(account)
	}
}

// Count records an attempt against the client address only (successful
// sign-ups: creating accounts in bulk is abuse even when each one succeeds).
func (t *Throttle) Count(r *http.Request) { t.addresses.fail(ClientIP(r, t.trustProxy)) }

// Succeed clears the account's failures after a correct sign-in.
func (t *Throttle) Succeed(account string) { t.accounts.clear(account) }
