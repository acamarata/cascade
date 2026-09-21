package conversation

// Purpose: the loader for scrub.go's golden fixtures under
//   testdata/v1-goldens/scrub/ -- see testdata/README.md's "Scrub
//   pipeline fixture provenance" section for where the bytes come from.
//   Split out under Art.10.3's 300-line cap, matching
//   internal/secrets/goldenfixture_test.go's own precedent of a
//   dedicated loader file next to the assertions that use it.
// SPORT: internal.conversation.scrub/ADDED (tests) (P1-E20-W5-S44-T1).

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scrubGolden is one fixture: an input turn carrying real credential
// shapes, the substrings that must never survive a scrub, and what the
// scrub is expected to produce. ExpectedOutput/ExpectedTag are copied
// from the H/S-15.T3 corpus, so the assertions come from the fixture
// rather than from a literal retyped into a test.
type scrubGolden struct {
	Name           string
	Input          string
	Canaries       []string
	ExpectedTag    string
	ExpectedOutput string
}

// loadScrubGolden reads one fixture file. The format is a small,
// dependency-free line scanner (no YAML decoder in this module, matching
// goldenfixture_test.go's own reasoning): "input:", "canary:",
// "canaries:" (with "  - " list items), "expected_tag:" and
// "expected_output:", each a Go-quoted string so the fixture pins bytes
// exactly.
func loadScrubGolden(t testing.TB, name string) scrubGolden {
	t.Helper()
	path := filepath.Join("testdata", "v1-goldens", "scrub", name)
	raw, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	g := scrubGolden{Name: name}
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "input:"):
			g.Input = mustUnquote(t, fieldValue(line, "input:"))
		case strings.HasPrefix(line, "canary:"):
			g.Canaries = []string{mustUnquote(t, fieldValue(line, "canary:"))}
		case strings.HasPrefix(line, "expected_tag:"):
			g.ExpectedTag = mustUnquote(t, fieldValue(line, "expected_tag:"))
		case strings.HasPrefix(line, "expected_output:"):
			g.ExpectedOutput = mustUnquote(t, fieldValue(line, "expected_output:"))
		case strings.HasPrefix(strings.TrimSpace(line), "- "):
			item := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			g.Canaries = append(g.Canaries, mustUnquote(t, item))
		}
	}
	if g.Input == "" || len(g.Canaries) == 0 || g.ExpectedOutput == "" {
		t.Fatalf("golden %s: missing input, canary(ies) or expected_output", path)
	}
	return g
}

// fieldValue returns the quoted remainder of a "key: value" line.
func fieldValue(line, key string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, key))
}

// mustUnquote decodes a Go-quoted fixture field.
func mustUnquote(t testing.TB, quoted string) string {
	t.Helper()
	s, err := strconv.Unquote(quoted)
	if err != nil {
		t.Fatalf("unquote %q: %v", quoted, err)
	}
	return s
}

// assertGoldenOutput asserts the scrub produced exactly what the fixture
// says it should: the expected rewritten bytes, and (when the fixture
// names one) the expected tag inside them. Both come from the golden
// file, never from a literal in this package.
func assertGoldenOutput(t *testing.T, g scrubGolden, got string) {
	t.Helper()
	if got != g.ExpectedOutput {
		t.Fatalf("%s: scrubbed output = %q, want the fixture's expected_output %q", g.Name, got, g.ExpectedOutput)
	}
	if g.ExpectedTag != "" && !strings.Contains(got, g.ExpectedTag) {
		t.Fatalf("%s: scrubbed output = %q, want the fixture's expected_tag %q", g.Name, got, g.ExpectedTag)
	}
	assertNoCanary(t, g.Name+" scrubbed output", got, g.Canaries)
}

// assertNoCanary fails if any canary substring survives in got.
func assertNoCanary(t *testing.T, what, got string, canaries []string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(got, canary) {
			t.Fatalf("%s carries a raw secret: %q", what, got)
		}
	}
}
