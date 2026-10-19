package sync

import "encoding/json"

// FieldClock is a row's field clock: field name -> HLC of the last write.
type FieldClock map[string]string

func ParseFieldClock(s string) FieldClock {
	fc := FieldClock{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &fc)
	}
	return fc
}

func (fc FieldClock) Marshal() string {
	b, _ := json.Marshal(fc)
	return string(b)
}

// Accept decides per field: if the incoming HLC wins, the field is
// applied and the field clock updated.
func (fc FieldClock) Accept(field, incomingHLC string) bool {
	if Compare(incomingHLC, fc[field]) > 0 {
		fc[field] = incomingHLC
		return true
	}
	return false
}

// --- OR-Set (Observed-Remove Set, add-wins) ---
//
// JSON representation: { "<element>": { "a": ["<tag>",...], "r": ["<tag>",...] } }
// An element is visible while it has an add tag that has not been removed
// (observed).

type ORSet map[string]struct {
	A []string `json:"a"`
	R []string `json:"r"`
}

func ParseORSet(s string) ORSet {
	set := ORSet{}
	if s != "" && s != "null" {
		_ = json.Unmarshal([]byte(s), &set)
	}
	return set
}

func (s ORSet) Marshal() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Add inserts an element with a unique tag (HLC).
func (s ORSet) Add(elem, tag string) {
	e := s[elem]
	e.A = appendUnique(e.A, tag)
	s[elem] = e
}

// Remove only deletes the currently observed add tags (add-wins against
// concurrent, not-yet-observed adds).
func (s ORSet) Remove(elem string) {
	e := s[elem]
	for _, t := range e.A {
		e.R = appendUnique(e.R, t)
	}
	s[elem] = e
}

// RemoveTags tombstones exactly the tags the ORIGIN device had observed
// (carried in the sync delta as removed_tags). Tags added concurrently by
// other devices stay visible: true add-wins across replicas.
func (s ORSet) RemoveTags(elem string, tags []string) {
	e := s[elem]
	for _, t := range tags {
		e.R = appendUnique(e.R, t)
	}
	s[elem] = e
}

// Merge unions two OR-Sets (commutative, idempotent).
func (s ORSet) Merge(other ORSet) {
	for elem, oe := range other {
		e := s[elem]
		for _, t := range oe.A {
			e.A = appendUnique(e.A, t)
		}
		for _, t := range oe.R {
			e.R = appendUnique(e.R, t)
		}
		s[elem] = e
	}
}

// Values returns the currently visible elements.
func (s ORSet) Values() []string {
	var out []string
	for elem, e := range s {
		rm := map[string]bool{}
		for _, t := range e.R {
			rm[t] = true
		}
		for _, t := range e.A {
			if !rm[t] {
				out = append(out, elem)
				break
			}
		}
	}
	return out
}

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}
