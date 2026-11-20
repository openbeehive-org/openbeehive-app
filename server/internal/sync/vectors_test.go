package sync

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// The shared merge cases, also executed by app/src/lib/local/merge.test.ts.
type vectorFile struct {
	Cases []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name    string   `json:"name"`
	SetCols []string `json:"set_cols"`
	Row     *struct {
		Fields   map[string]any   `json:"fields"`
		FieldHLC FieldClock       `json:"field_hlc"`
		Sets     map[string]ORSet `json:"sets"`
	} `json:"row"`
	Changes []struct {
		Op      string         `json:"op"`
		HLC     string         `json:"hlc"`
		Payload map[string]any `json:"payload"`
	} `json:"changes"`
	Want struct {
		Fields   map[string]any      `json:"fields"`
		FieldHLC FieldClock          `json:"field_hlc"`
		Sets     map[string][]string `json:"sets"`
		Tags     map[string]ORSet    `json:"tags"`
	} `json:"want"`
}

// canon round-trips through JSON so numbers and nil slices compare equal no
// matter how they were built.
func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func runVector(t *testing.T, c vectorCase) {
	row := Row{Clock: FieldClock{}, Sets: map[string]ORSet{}}
	values := map[string]any{}
	if c.Row != nil {
		for k, v := range c.Row.Fields {
			values[k] = v
		}
		for k, v := range c.Row.FieldHLC {
			row.Clock[k] = v
		}
		for k, v := range c.Row.Sets {
			row.Sets[k] = v
		}
	}
	isSet := map[string]bool{}
	for _, sc := range c.SetCols {
		isSet[sc] = true
	}

	for _, ch := range c.Changes {
		fields := ch.Payload
		if ch.Op == "delete" {
			fields = Tombstone()
		}
		for k, v := range fields {
			if !isSet[k] {
				continue
			}
			d, err := ParseSetDelta(v)
			if err != nil {
				t.Fatalf("set delta %s: %v", k, err)
			}
			fields[k] = d
		}
		for _, col := range row.Apply(fields, ch.HLC) {
			if !isSet[col] {
				values[col] = fields[col]
			}
		}
	}

	if got, want := canon(t, values), canon(t, c.Want.Fields); !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	if got, want := canon(t, row.Clock), canon(t, c.Want.FieldHLC); !reflect.DeepEqual(got, want) {
		t.Errorf("field_hlc = %v, want %v", got, want)
	}
	for col, want := range c.Want.Sets {
		got := row.Sets[col].Values()
		sort.Strings(got)
		if !reflect.DeepEqual(canon(t, got), canon(t, want)) {
			t.Errorf("set %s shows %v, want %v", col, got, want)
		}
	}
	for col, want := range c.Want.Tags {
		if got := canon(t, row.Sets[col]); !reflect.DeepEqual(got, canon(t, want)) {
			t.Errorf("set %s stores %v, want %v", col, got, canon(t, want))
		}
	}
}

func TestMergeVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/merge_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) { runVector(t, c) })
	}
}

func TestSetDeltaKeepsEmptyRemovedTagsOnTheWire(t *testing.T) {
	// Presence of removed_tags changes what a remove means, so a delta that
	// carried an empty one must not lose it when the server stores it.
	d, err := ParseSetDelta(map[string]any{"remove": []any{"p1"}, "removed_tags": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if string(b) != `{"remove":["p1"],"removed_tags":{}}` {
		t.Fatalf("wire form = %s", b)
	}
	legacy, _ := ParseSetDelta(map[string]any{"remove": []any{"p1"}})
	if b, _ := json.Marshal(legacy); string(b) != `{"remove":["p1"]}` {
		t.Fatalf("legacy wire form = %s", b)
	}
}

func TestParseSetDeltaRejectsMalformed(t *testing.T) {
	for name, v := range map[string]any{
		"not an object":        "p1",
		"add not a list":       map[string]any{"add": "p1"},
		"non-string element":   map[string]any{"add": []any{1.0}},
		"removed_tags a list":  map[string]any{"removed_tags": []any{}},
		"removed_tags content": map[string]any{"removed_tags": map[string]any{"p1": "t"}},
	} {
		if _, err := ParseSetDelta(v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
