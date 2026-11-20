package auth

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// The suite signs up and signs in hundreds of times; at the production work
// factor that is minutes under the race detector.
func TestMain(m *testing.M) {
	passwordHashCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func TestAttemptLimiterWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := newAttemptLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if l.blockedFor("k") != 0 {
			t.Fatalf("blocked after %d failures, limit is 3", i)
		}
		l.fail("k")
		now = now.Add(time.Second)
	}
	if got := l.blockedFor("k"); got != 57*time.Second {
		t.Fatalf("blocked for %v, want the rest of the window (57s)", got)
	}
	if l.blockedFor("other") != 0 {
		t.Fatal("keys are independent")
	}
	l.fail("k") // more failures while blocked do not extend the block
	now = now.Add(57 * time.Second)
	if l.blockedFor("k") != 0 {
		t.Fatal("the block must lift when the window ends")
	}
	l.fail("k")
	l.clear("k")
	if len(l.hits) != 0 {
		t.Fatalf("clear must drop the counter: %v", l.hits)
	}
}

func TestAttemptLimiterStaysBounded(t *testing.T) {
	l := newAttemptLimiter(3, time.Minute)
	for i := range throttleMaxCounters + 10 {
		l.fail(strconv.Itoa(i))
	}
	if len(l.hits) > throttleMaxCounters {
		t.Fatalf("limiter holds %d counters, cap is %d", len(l.hits), throttleMaxCounters)
	}
}

func signin(f *apiFixture, t *testing.T, email, password string, headers ...string) response {
	t.Helper()
	return f.call(t, http.MethodPost, "/auth/signin", "", map[string]string{"email": email, "password": password}, headers...)
}

func TestSigninIsThrottledPerAccount(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	f.signup(t, "victim@test.dev")
	f.signup(t, "bystander@test.dev")

	for i := range throttlePerAccount {
		if res := signin(f, t, "Victim@test.dev", "wrong"); res.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i+1, res.status, res.raw)
		}
	}
	// The eleventh attempt is refused before the password is even looked at.
	res := signin(f, t, "victim@test.dev", "password123")
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
	retry, err := strconv.Atoi(res.header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > int(throttleWindow.Seconds())+1 {
		t.Fatalf("Retry-After %q, want seconds within the window", res.header.Get("Retry-After"))
	}
	// Other accounts from the same address still sign in.
	if res := signin(f, t, "bystander@test.dev", "password123"); res.status != http.StatusOK {
		t.Fatalf("bystander: %d %s", res.status, res.raw)
	}
}

func TestSuccessfulSigninClearsTheAccountCounter(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	f.signup(t, "u@test.dev")
	for range throttlePerAccount - 1 {
		signin(f, t, "u@test.dev", "wrong")
	}
	if res := signin(f, t, "u@test.dev", "password123"); res.status != http.StatusOK {
		t.Fatalf("correct password below the limit: %d", res.status)
	}
	for range throttlePerAccount - 1 {
		signin(f, t, "u@test.dev", "wrong")
	}
	if res := signin(f, t, "u@test.dev", "password123"); res.status != http.StatusOK {
		t.Fatalf("the counter was not cleared by the successful sign-in: %d", res.status)
	}
}

func TestSigninIsThrottledPerAddress(t *testing.T) {
	cfg := loginConfig()
	cfg.TrustProxyHeaders = true
	f := newAPIFixture(t, cfg)
	f.signup(t, "u@test.dev")
	attacker := []string{"X-Forwarded-For", "198.51.100.66"}

	// Spraying many accounts stays under every per-account limit.
	for i := range throttlePerIP {
		res := signin(f, t, fmt.Sprintf("guess%d@test.dev", i), "wrong", attacker...)
		if res.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i+1, res.status, res.raw)
		}
	}
	res := signin(f, t, "u@test.dev", "password123", attacker...)
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
	if res.header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Sign-up and sign-in share the address budget.
	res = f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "new@test.dev", "password": "password123"}, attacker...)
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
	// Another client behind the same proxy is not affected.
	if res := signin(f, t, "u@test.dev", "password123", "X-Forwarded-For", "203.0.113.5"); res.status != http.StatusOK {
		t.Fatalf("other address: %d %s", res.status, res.raw)
	}
}

// Without BEEHIVE_TRUST_PROXY_HEADERS a client cannot dodge the address
// limit by sending a different X-Forwarded-For with every request.
func TestForwardedHeaderIsIgnoredUnlessTrusted(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	for i := range throttlePerIP {
		signin(f, t, fmt.Sprintf("guess%d@test.dev", i), "wrong", "X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
	}
	res := signin(f, t, "guess@test.dev", "wrong", "X-Forwarded-For", "203.0.113.200")
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
}

func TestSignupIsThrottled(t *testing.T) {
	f := newAPIFixture(t, loginConfig())
	f.signup(t, "taken@test.dev")
	// Probing whether an address has an account counts as a failure.
	for i := range throttlePerAccount {
		res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "taken@test.dev", "password": "password123"})
		if res.status != http.StatusConflict {
			t.Fatalf("attempt %d: %d", i+1, res.status)
		}
	}
	res := f.call(t, http.MethodPost, "/auth/signup", "", map[string]string{"email": "taken@test.dev", "password": "password123"})
	wantError(t, res, http.StatusTooManyRequests, "rate_limited")
}

// Signing in to an account that does not exist must cost a password hash
// comparison like a real one; otherwise the response time tells an attacker
// which email addresses are registered.
func TestSigninUnknownAccountTakesAsLongAsKnown(t *testing.T) {
	// At the production work factor, where a comparison dwarfs everything
	// else in the request.
	passwordHashCost = bcrypt.DefaultCost
	dummyHash = sync.OnceValue(newDummyHash)
	t.Cleanup(func() {
		passwordHashCost = bcrypt.MinCost
		dummyHash = sync.OnceValue(newDummyHash)
	})
	f := newAPIFixture(t, loginConfig())
	f.signup(t, "known@test.dev")
	dummyHash() // built once, at the first sign-in; keep that out of the timing

	measure := func(email string) time.Duration {
		best := time.Hour
		for range 3 {
			start := time.Now()
			if res := signin(f, t, email, "wrong-password"); res.status != http.StatusUnauthorized {
				t.Fatalf("%s: %d", email, res.status)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	known, unknown := measure("known@test.dev"), measure("nobody@test.dev")
	if unknown < known/3 {
		t.Fatalf("unknown account answered in %v, known in %v: no hash comparison ran", unknown, known)
	}
}
