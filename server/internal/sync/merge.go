package sync

import (
	"encoding/json"
	"errors"
	"sort"
)

// The merge rules in this file are mirrored by app/src/lib/local/merge.ts.
// Both sides run the cases in testdata/merge_vectors.json; a rule changed
// here must change there, with a vector that shows it.

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

// Accept decides per field: if the incoming HLC is strictly newer than the
// field's stamp, the field is applied and the field clock updated. An equal
// stamp does not overwrite.
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

// MarshalJSON writes empty tag lists as [] rather than null, the shape the
// app stores too.
func (s ORSet) MarshalJSON() ([]byte, error) {
	type elem struct {
		A []string `json:"a"`
		R []string `json:"r"`
	}
	out := make(map[string]elem, len(s))
	for k, e := range s {
		out[k] = elem{A: append([]string{}, e.A...), R: append([]string{}, e.R...)}
	}
	return json.Marshal(out)
}

// Tombstone returns the fields a delete change carries: deleting a row is
// a write to its "deleted" column, merged like any other scalar.
func Tombstone() map[string]any { return map[string]any{"deleted": int64(1)} }

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

// SetDelta is the change to one set column carried in a sync payload:
// {"add": [...], "remove": [...], "removed_tags": {"<element>": ["<tag>", ...]}}.
type SetDelta struct {
	Add    []string
	Remove []string
	// RemovedTags lists, per removed element, the add tags the origin device
	// had observed. nil means the payload had no removed_tags at all, which is
	// how older apps send removes.
	RemovedTags map[string][]string
}

// ErrSetDelta reports a set-column value that is not a well-formed delta.
var ErrSetDelta = errors.New("set column needs {add, remove, removed_tags} with string lists")

// ParseSetDelta reads a decoded JSON value as a set delta. Anything but
// string lists in the three known keys is refused; other keys are ignored.
func ParseSetDelta(v any) (SetDelta, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return SetDelta{}, ErrSetDelta
	}
	var d SetDelta
	var err error
	if d.Add, err = stringList(obj["add"]); err != nil {
		return SetDelta{}, err
	}
	if d.Remove, err = stringList(obj["remove"]); err != nil {
		return SetDelta{}, err
	}
	raw, present := obj["removed_tags"]
	if !present || raw == nil {
		return d, nil
	}
	tags, ok := raw.(map[string]any)
	if !ok {
		return SetDelta{}, ErrSetDelta
	}
	d.RemovedTags = make(map[string][]string, len(tags))
	for elem, list := range tags {
		if d.RemovedTags[elem], err = stringList(list); err != nil {
			return SetDelta{}, err
		}
	}
	return d, nil
}

func stringList(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, ErrSetDelta
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, ErrSetDelta
		}
		out = append(out, s)
	}
	return out, nil
}

// MarshalJSON writes the wire form. removed_tags is written whenever the
// delta had one, even when empty: its presence changes what a remove means.
func (d SetDelta) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	if len(d.Add) > 0 {
		out["add"] = d.Add
	}
	if len(d.Remove) > 0 {
		out["remove"] = d.Remove
	}
	if d.RemovedTags != nil {
		out["removed_tags"] = d.RemovedTags
	}
	return json.Marshal(out)
}

// ApplyDelta merges a delta stamped hlc into the set. Adds are tagged with
// the stamp. A remove tombstones the tags listed for its element in
// removed_tags: an element that is missing there, or has an empty list,
// loses nothing. Only a delta without any removed_tags falls back to
// tombstoning every tag this replica has observed.
func (s ORSet) ApplyDelta(d SetDelta, hlc string) {
	for _, e := range d.Add {
		s.Add(e, hlc)
	}
	for _, e := range d.Remove {
		if d.RemovedTags == nil {
			s.Remove(e)
			continue
		}
		s.RemoveTags(e, d.RemovedTags[e])
	}
}

// Row is the merge state of one row: the clock of its scalar columns and
// the content of its set columns.
type Row struct {
	Clock FieldClock
	Sets  map[string]ORSet
}

// Apply merges the fields of one change stamped hlc into the row and returns
// the columns that have to be written back, sorted: scalars whose stamp won
// (their new value is fields[column]) and every set column the change carries
// (its new value is r.Sets[column]). A SetDelta value marks a set column;
// everything else is a scalar. "id" and "field_hlc" are never merged.
func (r *Row) Apply(fields map[string]any, hlc string) []string {
	if r.Clock == nil {
		r.Clock = FieldClock{}
	}
	if r.Sets == nil {
		r.Sets = map[string]ORSet{}
	}
	var changed []string
	for col, v := range fields {
		if col == "id" || col == "field_hlc" {
			continue
		}
		if d, isSet := v.(SetDelta); isSet {
			set := r.Sets[col]
			if set == nil {
				set = ORSet{}
				r.Sets[col] = set
			}
			set.ApplyDelta(d, hlc)
			changed = append(changed, col)
			continue
		}
		if r.Clock.Accept(col, hlc) {
			changed = append(changed, col)
		}
	}
	sort.Strings(changed)
	return changed
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
