package service

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

const syncedColumnsFile = "testdata/synced_columns.json"

// TestSyncedColumnsFile writes the server's column whitelist (entityCols) to
// testdata/synced_columns.json. app/src/lib/local/columns.test.ts compares it
// with the app's local schema: a column the app syncs but the server does not
// know would be dropped by Push without a word, and one the server sends but
// the app lacks would be skipped by Pull. UPDATE_GOLDEN=1 rewrites the file.
func TestSyncedColumnsFile(t *testing.T) {
	type entity struct {
		Cols    []string `json:"cols"`
		SetCols []string `json:"set_cols"`
	}
	out := map[string]entity{}
	for name, spec := range entityCols {
		var cols []string
		for _, c := range append(append([]string{}, spec.cols...), spec.setCols...) {
			if c != "id" {
				cols = append(cols, c)
			}
		}
		slices.Sort(cols)
		set := append([]string{}, spec.setCols...)
		slices.Sort(set)
		out[name] = entity{Cols: cols, SetCols: set}
	}
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(syncedColumnsFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(syncedColumnsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s is out of date: run this test with UPDATE_GOLDEN=1, then the app tests", syncedColumnsFile)
	}
}
