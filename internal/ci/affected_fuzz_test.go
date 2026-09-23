// Purpose (this file): FuzzAffectedGoList (06 §5.7's decoder-fuzz
// requirement), driving parseImportGraph -- the real go list subprocess
// output's line parser (affected_go.go) -- with malformed/truncated/
// adversarial byte input. Seed corpus at
// internal/ci/testdata/fuzz/FuzzAffectedGoList/ (package-local per
// R-21.266, this check names exactly this package, never a /... pattern).
// SPORT: internal.ci.parseImportGraph/FUZZED (P1-E32-W6-S65-T1).
package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzAffectedGoList's property under test: never panic, and every input
// either parses to a graph whose every key is a non-empty import path, or
// returns a non-nil error with a nil graph -- no third outcome.
func FuzzAffectedGoList(f *testing.F) {
	seedDir := filepath.Join("testdata", "fuzz", "FuzzAffectedGoList")
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		f.Add(data)
	}
	f.Add([]byte(nil))
	f.Add([]byte("example.com/fixture/pkg/a|\n"))
	f.Add([]byte("example.com/fixture/pkg/b|example.com/fixture/pkg/a,fmt\n"))
	f.Add([]byte("not go list output at all"))
	f.Add([]byte("|fmt"))
	f.Add([]byte("a|b|c"))

	f.Fuzz(func(t *testing.T, data []byte) {
		graph, err := parseImportGraph(data)
		if err != nil {
			if graph != nil {
				t.Fatalf("a refused input produced a non-nil graph alongside an error: %v", err)
			}
			return
		}
		for pkg := range graph {
			if strings.TrimSpace(pkg) == "" {
				t.Fatalf("parseImportGraph accepted an all-whitespace import path key: %+v", graph)
			}
		}
	})
}
