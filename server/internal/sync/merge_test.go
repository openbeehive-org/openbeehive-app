package sync

import (
	"sort"
	"testing"
)

// These tests mirror app/src/lib/local/merge.test.ts; the client and server
// merge logic must stay behaviorally identical.

const (
	t1 = "000000000000001:00000:devA"
	t2 = "000000000000002:00000:devB"
	t3 = "000000000000003:00000:devA"
)

func TestFieldClockAcceptNewerWins(t *testing.T) {
	fc := ParseFieldClock("{}")
	if !fc.Accept("name", t2) {
		t.Fatal("newer stamp must be accepted")
	}
	if fc["name"] != t2 {
		t.Fatalf("clock not recorded: %v", fc)
	}
	if fc.Accept("name", t1) {
		t.Fatal("older stamp must be rejected")
	}
	if fc.Accept("name", t2) {
		t.Fatal("equal stamp must be rejected")
	}
}

func TestFieldClockFieldsIndependent(t *testing.T) {
	fc := FieldClock{"name": t3}
	if !fc.Accept("note", t1) {
		t.Fatal("an older stamp on a different field must be accepted")
	}
}

func TestFieldClockTolerantParsing(t *testing.T) {
	if got := ParseFieldClock("not json"); len(got) != 0 {
		t.Fatalf("malformed clock: %v", got)
	}
	if got := ParseFieldClock(""); len(got) != 0 {
		t.Fatalf("empty clock: %v", got)
	}
}

func TestORSetAddRemove(t *testing.T) {
	s := ORSet{}
	s.Add("p1", t1)
	if got := s.Values(); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("after add: %v", got)
	}
	s.Remove("p1")
	if got := s.Values(); len(got) != 0 {
		t.Fatalf("after remove: %v", got)
	}
	s.Add("p1", t3) // re-add with a new tag becomes visible again
	if got := s.Values(); len(got) != 1 {
		t.Fatalf("after re-add: %v", got)
	}
}

func TestORSetAddWinsAgainstConcurrentRemove(t *testing.T) {
	// Device A removes what it observed; device B's concurrent add carries an
	// unobserved tag. After merge the element must stay visible.
	a := ORSet{}
	a.Add("p1", t1)
	a.Remove("p1")

	b := ORSet{}
	b.Add("p1", t2)

	a.Merge(b)
	if got := a.Values(); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("add must win: %v", got)
	}
}

func TestORSetRemoveTagsKeepsUnlistedTags(t *testing.T) {
	// The server row has two adds; the delta says the origin observed only t1.
	s := ORSet{}
	s.Add("p1", t1)
	s.Add("p1", t2)
	s.RemoveTags("p1", []string{t1})
	if got := s.Values(); len(got) != 1 {
		t.Fatalf("tag t2 was not observed by the remover, element must stay: %v", got)
	}
	s.RemoveTags("p1", []string{t2})
	if got := s.Values(); len(got) != 0 {
		t.Fatalf("all tags tombstoned, element must vanish: %v", got)
	}
}

func TestORSetMergeCommutative(t *testing.T) {
	mk := func() ORSet {
		s := ORSet{}
		s.Add("x", t1)
		s.Remove("x")
		return s
	}
	other := ORSet{}
	other.Add("x", t2)
	other.Add("y", t3)

	ab := mk()
	ab.Merge(other)

	ba := ORSet{}
	ba.Merge(other)
	ba.Merge(mk())

	va, vb := ab.Values(), ba.Values()
	sort.Strings(va)
	sort.Strings(vb)
	if len(va) != len(vb) {
		t.Fatalf("orders diverge: %v vs %v", va, vb)
	}
	for i := range va {
		if va[i] != vb[i] {
			t.Fatalf("orders diverge: %v vs %v", va, vb)
		}
	}
}

func TestORSetTolerantParsing(t *testing.T) {
	if got := ParseORSet("nope"); len(got) != 0 {
		t.Fatalf("malformed set: %v", got)
	}
	if got := ParseORSet("null"); len(got) != 0 {
		t.Fatalf("null set: %v", got)
	}
}
