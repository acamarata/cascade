package context

// Purpose: the committed golden corpus half of the cross-harness suite
//   (P1-E16-W4-S35-T4). Split from cross_harness_test.go so that file
//   stays the CONVENTIONS every writer must share and this stays the
//   captured bytes — and so both meet Art.10.3's 300-line cap.
// SPORT: internal/context cross-harness conformance (ADD) — P1-E16-W4-S35-T4.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGoldens regenerates the committed corpus from a real run instead
// of asserting against it. The corpus is CAPTURED, never hand-authored
// (Art.2): a format change is re-captured with this flag and the diff is
// reviewed, which is what makes the goldens evidence rather than a second
// copy of the code's opinion.
var updateGoldens = flag.Bool("update-cross-harness", false,
	"re-capture internal/context/testdata/cross-harness from a real generator run")

// crossHarnessCorpus is the committed corpus root.
const crossHarnessCorpus = "testdata/cross-harness"

// TestCrossHarnessGoldenCorpus pins each writer's real output against the
// committed corpus.
//
// Re-capture with -update-cross-harness when a format change is intended;
// the diff is the review. An unintended change fails here, which is the
// only reason to commit the bytes at all.
func TestCrossHarnessGoldenCorpus(t *testing.T) {
	_, mc, _ := crossHarnessFixture(t)

	for _, w := range harnessWriters() {
		name := harnessName(w)
		files, err := w.Generate(mc)
		if err != nil {
			t.Fatalf("%s: Generate: %v", name, err)
		}
		var b strings.Builder
		for _, f := range files {
			b.WriteString("=== role=" + roleSlug(f.Role) + " name=" + f.Name + "\n")
			b.Write(f.Content)
		}
		golden := filepath.Join(crossHarnessCorpus, name, "instruction.golden")
		if *updateGoldens {
			if err := os.WriteFile(golden, []byte(b.String()), 0o600); err != nil {
				t.Fatalf("re-capturing %s: %v", golden, err)
			}
			continue
		}
		want, err := os.ReadFile(golden) //nolint:gosec // fixed corpus path.
		if err != nil {
			t.Fatalf("reading %s: %v (re-capture with -update-cross-harness)", golden, err)
		}
		if got := b.String(); got != string(want) {
			t.Errorf("%s output differs from its committed golden.\n--- got ---\n%s\n--- want ---\n%s",
				name, got, want)
		}
	}
}

// roleSlug renders a TierRole for the corpus header. The corpus is read by
// people, and a bare integer would make a role rename invisible in a diff.
func roleSlug(role TierRole) string {
	for name, r := range map[string]TierRole{
		"gci": TierGCI, "asi": TierASI, "ppi": TierPPI, "pri": TierPRI, "pai": TierPAI,
	} {
		if r == role {
			return name
		}
	}
	return "unknown"
}
