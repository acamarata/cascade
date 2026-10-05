package eval_test

// Purpose: TestEvalPairHasNoPrivatePatternHits_Live sweeps the eval fixture
//   pair (testdata/corpus.jsonl and testdata/recorded/real-embedder.jsonl)
//   against the private deny patterns, with allow lines applied the way
//   build.FilterAllowed applies them. The repo-wide sweep skips every
//   testdata path (build.SweepSkipsPath), so without this test nothing
//   guards the pair: build.SweepFiles over these paths would silently scan
//   nothing and report zero hits. Here the content goes through
//   build.SweepContent directly.
// Inputs: CASCADE_HYGIENE_RUN=1 and CASCADE_IDENTIFIER_PATTERNS_FILE (or the
//   masked CI variable) naming the private pattern source; the two fixtures.
// Outputs: n/a (test-only). A failure names path:line and the pattern index
//   only, never a matched value, a pattern, or the line text.
// Constraints: skips without CASCADE_HYGIENE_RUN=1 (a run that must prove the
//   pair clean treats a SKIP as a failure); fails closed on a missing or
//   unparsable pattern source and on an empty fixture; nothing is written.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-GWY-12).

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/acamarata/cascade/internal/build"
)

// pairDir holds the fixture pair, relative to this package's directory.
const pairDir = "testdata"

// pairFiles are the two fixtures that carry corpus text and its vectors.
var pairFiles = []string{"corpus.jsonl", filepath.Join("recorded", "real-embedder.jsonl")}

func TestEvalPairHasNoPrivatePatternHits_Live(t *testing.T) {
	if os.Getenv("CASCADE_HYGIENE_RUN") != "1" {
		t.Skip("CASCADE_HYGIENE_RUN not set; live eval-pair sweep not requested")
	}
	patterns, err := build.LoadPatterns()
	if err != nil {
		t.Fatal(err) // fail closed: a missing pattern source blocks, never skips
	}
	allows, err := build.LoadAllowPatterns()
	if err != nil {
		t.Fatal(err) // fail closed: an unreadable allow list blocks, never skips
	}
	var all []build.SweepViolation
	for _, name := range pairFiles {
		rel := filepath.ToSlash(filepath.Join(pairDir, name))
		data, err := os.ReadFile(filepath.Join(pairDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty: an empty file cannot prove the pair clean", rel)
		}
		all = append(all, build.SweepContent(patterns, rel, data)...)
	}
	violations, exempted := build.FilterAllowed(all, allows)
	t.Logf("eval pair sweep: %d pattern(s), %d allow line(s), %d hit(s) exempted", len(patterns), len(allows), exempted)
	for _, v := range violations {
		t.Errorf("%s:%d matches pattern #%d", v.Source, v.Line, patternIndex(patterns, v.Pattern))
	}
	if len(violations) > 0 {
		t.Fatalf("eval pair sweep: %d violation(s) (path:line and pattern index above)", len(violations))
	}
}

// patternIndex returns the position of the pattern whose source text is src,
// or -1. Only the index is ever reported, never the pattern.
func patternIndex(patterns []*regexp.Regexp, src string) int {
	for i, p := range patterns {
		if p.String() == src {
			return i
		}
	}
	return -1
}
