//go:build spike

package syncmerge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureFile is the B-S03.T3 per-domain export envelope this spike's
// fixtures are written in. sideA and sideB are the two divergent copies a
// merge reconciles. Expected is populated ONLY for the phase-git domain,
// whose real counterpart (git itself) is independent of this spike's
// author; config, memory and blob fixtures leave it empty and are asserted
// via the algebraic properties instead (R-21.219).
type fixtureFile struct {
	Domain      string          `json:"domain"`
	Case        string          `json:"case"`
	Description string          `json:"description"`
	SideA       json.RawMessage `json:"sideA"`
	SideB       json.RawMessage `json:"sideB"`
	Expected    json.RawMessage `json:"expected,omitempty"`
}

// loadFixture reads and decodes one fixture JSON file from
// internal/syncmerge/testdata/fixtures/.
func loadFixture(t *testing.T, name string) fixtureFile {
	t.Helper()
	path := filepath.Join("testdata", "fixtures", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("loadFixture(%s): %v", name, err)
	}
	var f fixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("loadFixture(%s): decode: %v", name, err)
	}
	return f
}

// allFixtureNames lists every fixture file under testdata/fixtures,
// sorted, so property tests can iterate the whole corpus without
// hardcoding a count.
func allFixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", "fixtures"))
	if err != nil {
		t.Fatalf("allFixtureNames: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	return names
}

// decodeSide decodes one side's raw JSON into the given slice type.
func decodeSide[T any](t *testing.T, raw json.RawMessage) []T {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decodeSide: %v", err)
	}
	return out
}

// toMap indexes a slice of sensitive records by their record id, matching
// the map-keyed shape every merge function in this package operates on.
func toMap[T sensitive](records []T) map[string]T {
	out := make(map[string]T, len(records))
	for _, r := range records {
		out[r.recordID()] = r
	}
	return out
}
