package context

import (
	"os"
	"path/filepath"
	"testing"
)

// Fuzz target for the instruction merge model. Kept in its own file per the
// module's existing convention (internal/runtime/fuzz_test.go,
// internal/secrets/fuzz_env_test.go) and to stay under Art.10.3's 300-line
// file cap.

// FuzzMergeTiers drives the section splitter and the precedence pass with
// arbitrary tier bodies. Beyond "does not panic" it re-derives the expected
// winner for every surviving heading from the SPEC rule (lowest ordinal
// among the tiers that define it) and requires the implementation to agree.
// Seeds come from internal/testdata/fuzz/context/ per 06-FORGE-SPEC.md §5.7.
func FuzzMergeTiers(f *testing.F) {
	for _, seed := range fuzzMergeSeeds(f) {
		f.Add(seed, seed, "")
		f.Add("", seed, seed)
	}
	f.Add("## A\n1\n", "## A\n2\n", "## B\n3\n")

	f.Fuzz(func(t *testing.T, gci, ppi, pai string) {
		tiers := []TierRecord{rec(TierGCI, 0, gci), rec(TierPPI, 2, ppi), rec(TierPAI, 4, pai)}
		merged, err := MergeTiers(tiers)
		if err != nil {
			return // rejected input: fail-closed is a valid outcome.
		}
		for _, s := range merged.Sections {
			if s.Heading == "" {
				continue
			}
			want := TierRole(0)
			for _, r := range tiers {
				for _, blk := range splitSections(r.Content) {
					if blk.heading == s.Heading && (want == 0 || r.Role < want) {
						want = r.Role
					}
				}
			}
			if s.Role != want {
				t.Fatalf("heading %q won by %s, spec says %s", s.Heading, s.Role, want)
			}
		}
	})
}

// fuzzMergeSeeds loads the curated seed bodies for FuzzMergeTiers.
func fuzzMergeSeeds(f *testing.F) []string {
	f.Helper()
	dir := filepath.Join("..", "testdata", "fuzz", "context")
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.Fatalf("reading fuzz seed corpus %s: %v", dir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README.md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), err)
		}
		seeds = append(seeds, string(raw))
	}
	if len(seeds) == 0 {
		f.Fatal("fuzz seed corpus is empty (fail closed: a silently empty corpus fuzzes nothing)")
	}
	return seeds
}
