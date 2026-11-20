package sync

import (
	"testing"
	"time"
)

func TestParseAcceptsOnlyTheFixedFormat(t *testing.T) {
	ms, c, node, ok := Parse("000001700000000:00042:dev-A.1_x")
	if !ok || ms != 1700000000 || c != 42 || node != "dev-A.1_x" {
		t.Fatalf("parse = %d %d %q %v", ms, c, node, ok)
	}
	for _, bad := range []string{
		"",
		"zzzz",
		"1700000000:00042:dev",               // ms not padded
		"000001700000000:42:dev",             // counter not padded
		"000001700000000:000420:dev",         // counter too wide
		"000001700000000:00042:",             // no node
		"000001700000000:00042:dev:extra",    // ':' is not a node character
		"00000170000000a:00042:dev",          // non-digit
		"000001700000000-00042:dev",          // wrong separator
		"000001700000000:00042:dev'; DROP--", // punctuation
		"999999999999999:99999:~",
	} {
		if Valid(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCompareRanksMalformedBelowWellFormed(t *testing.T) {
	good := Format(1000, 0, "dev")
	if Compare(good, "zzzz") != 1 || Compare("zzzz", good) != -1 {
		t.Fatal("a malformed stamp must sort below a well-formed one")
	}
	if Compare("zzzz", "") != 0 || Compare("", "") != 0 {
		t.Fatal("two malformed stamps are equal")
	}
	if Compare(Format(1000, 0, "a"), Format(1000, 1, "a")) != -1 {
		t.Fatal("counter orders within a millisecond")
	}
}

func TestNowIsStrictlyMonotonic(t *testing.T) {
	h := NewHLC("n")
	prev := h.Now()
	for i := 0; i < 1000; i++ {
		next := h.Now()
		if Compare(prev, next) >= 0 {
			t.Fatalf("%s then %s", prev, next)
		}
		prev = next
	}
}

func TestCounterOverflowAdvancesTheMillisecond(t *testing.T) {
	// A clock pinned in the future cannot fall back on the wall clock, so
	// every Now ticks the counter. At 99999 the millisecond must move on:
	// a six-digit counter would sort below the stamp before it.
	future := time.Now().Add(time.Hour).UnixMilli()
	h := &HLC{nodeID: "n", lastMS: future, counter: maxCounter - 1}
	a, b, c := h.Now(), h.Now(), h.Now()
	if a != Format(future, maxCounter, "n") {
		t.Fatalf("a = %s", a)
	}
	if b != Format(future+1, 0, "n") {
		t.Fatalf("overflow must roll into the next millisecond, got %s", b)
	}
	if !Valid(b) || Compare(a, b) >= 0 || Compare(b, c) >= 0 {
		t.Fatalf("order broken: %s %s %s", a, b, c)
	}
}

func TestRecvAdvancesPastTheRemoteStamp(t *testing.T) {
	h := NewHLC("n")
	remote := Format(time.Now().Add(time.Minute).UnixMilli(), 7, "other")
	if err := h.Recv(remote); err != nil {
		t.Fatal(err)
	}
	if got := h.Now(); Compare(got, remote) <= 0 {
		t.Fatalf("%s must be after %s", got, remote)
	}
}

func TestRecvAtFullCounterStaysWellFormed(t *testing.T) {
	h := NewHLC("n")
	ms := time.Now().Add(time.Minute).UnixMilli()
	remote := Format(ms, maxCounter, "other")
	if err := h.Recv(remote); err != nil {
		t.Fatal(err)
	}
	got := h.Now()
	if !Valid(got) || Compare(got, remote) <= 0 {
		t.Fatalf("%s must be well-formed and after %s", got, remote)
	}
}

func TestRecvRefusesMalformedStamps(t *testing.T) {
	h := NewHLC("n")
	before := h.Now()
	// Before validation this parsed as ms=999999999999999999 and dragged the
	// clock centuries ahead.
	for _, bad := range []string{"999999999999999999:0:x", "zzzz", ""} {
		if err := h.Recv(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	after := h.Now()
	if ahead, _ := AheadBy(after, time.Now()); ahead > time.Second {
		t.Fatalf("clock was poisoned: %s is %v ahead (before: %s)", after, ahead, before)
	}
}

func TestAheadBy(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	d, ok := AheadBy(Format(now.Add(11*time.Minute).UnixMilli(), 0, "n"), now)
	if !ok || d != 11*time.Minute {
		t.Fatalf("ahead = %v %v", d, ok)
	}
	if d, _ := AheadBy(Format(now.Add(-time.Hour).UnixMilli(), 0, "n"), now); d != -time.Hour {
		t.Fatalf("behind = %v", d)
	}
	if _, ok := AheadBy("nope", now); ok {
		t.Fatal("malformed stamp has no distance")
	}
}
