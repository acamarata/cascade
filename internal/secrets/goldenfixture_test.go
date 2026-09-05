package secrets

// Purpose: the loader for the rewriter's golden fixtures. Split from
//
//	rewriter_test.go under the repo's 300-line file cap; it holds only
//	the fixture type and its reader, so the test file next door reads as
//	assertions rather than as parsing.
//
// Constraints: the fixture format is a deliberately small YAML subset -
//
//	flat scalars, one string list, one list of mappings - read by the
//	reader below rather than by a dependency, because the module has no
//	YAML decoder and a golden loader is not a reason to add one. Strings
//	are Go-quoted so a fixture pins bytes exactly: an invisible trailing
//	newline is the classic way a golden stops proving anything.

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// goldenDirPath is the shared testdata home the fixtures live in.
const goldenDirPath = "../testdata/secrets/goldens"

// goldenFixture is one rewrite case: an input turn, the hits over it, and
// the exact bytes the rewriter must produce.
type goldenFixture struct {
	Name string
	// Provenance is "detector" when Hits are the real Detector's
	// ScanCertain output for Input (the fixture test re-derives them and
	// fails if they have drifted), or "authored" when the hits are
	// hand-written to reach a case the detector resolves before the
	// rewriter ever sees it.
	Provenance     string
	Input          string
	ExpectedOutput string
	// Canaries are substrings of Input that must not survive anywhere:
	// not in the output, not in a Replacement, not in a diagnostic.
	Canaries []string
	Hits     []DetectionHit
}

// loadGoldenFixtures reads every fixture, sorted by name so the table runs
// in a fixed order.
func loadGoldenFixtures(t *testing.T) []goldenFixture {
	t.Helper()
	entries, err := os.ReadDir(goldenDirPath)
	if err != nil {
		t.Fatalf("reading the golden fixtures: %v", err)
	}
	var out []goldenFixture
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		out = append(out, loadGoldenFixture(t, filepath.Join(goldenDirPath, entry.Name())))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) == 0 {
		t.Fatal("no golden fixtures found")
	}
	return out
}

// loadGoldenFixture reads one fixture file.
func loadGoldenFixture(t *testing.T, path string) goldenFixture {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	fixture := goldenFixture{}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
		case strings.HasPrefix(line, "  - ") && section == "canaries":
			fixture.Canaries = append(fixture.Canaries, unquoteFixture(t, path, line[4:]))
		case strings.HasPrefix(line, "  - ") && section == "hits":
			fixture.Hits = append(fixture.Hits, DetectionHit{})
			applyHitField(t, path, &fixture, line[4:])
		case strings.HasPrefix(line, "    ") && section == "hits":
			applyHitField(t, path, &fixture, strings.TrimSpace(line))
		case !strings.HasPrefix(line, " "):
			section = applyTopLevel(t, path, &fixture, line)
		default:
			t.Fatalf("%s: cannot read fixture line %q", path, line)
		}
	}
	return fixture
}

// applyTopLevel reads one `key: value` line and returns the section name
// a following indented block belongs to.
func applyTopLevel(t *testing.T, path string, fixture *goldenFixture, line string) string {
	t.Helper()
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		t.Fatalf("%s: fixture line %q is not key: value", path, line)
	}
	value = strings.TrimSpace(value)
	switch key {
	case "name":
		fixture.Name = value
	case "provenance":
		fixture.Provenance = value
	case "input":
		fixture.Input = unquoteFixture(t, path, value)
	case "expected_output":
		fixture.ExpectedOutput = unquoteFixture(t, path, value)
	case "canaries", "hits":
		if value != "[]" && value != "" {
			t.Fatalf("%s: %s must be [] or an indented list", path, key)
		}
		return key
	default:
		t.Fatalf("%s: unknown fixture key %q", path, key)
	}
	return ""
}

// applyHitField sets one field on the fixture's most recent hit.
func applyHitField(t *testing.T, path string, fixture *goldenFixture, line string) {
	t.Helper()
	if len(fixture.Hits) == 0 {
		t.Fatalf("%s: hit field %q before any list entry", path, line)
	}
	hit := &fixture.Hits[len(fixture.Hits)-1]
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		t.Fatalf("%s: hit line %q is not key: value", path, line)
	}
	value = strings.TrimSpace(value)
	switch key {
	case "class":
		hit.Class = Class(value)
	case "pattern":
		hit.Pattern = value
	case "name":
		hit.SuggestedName = value
	case "offset":
		hit.Offset = fixtureInt(t, path, value)
	case "len":
		hit.Len = fixtureInt(t, path, value)
	case "confidence":
		hit.Confidence = Confidence(fixtureFloat(t, path, value))
	default:
		t.Fatalf("%s: unknown hit key %q", path, key)
	}
}

// unquoteFixture decodes a Go-quoted fixture scalar.
func unquoteFixture(t *testing.T, path, value string) string {
	t.Helper()
	decoded, err := strconv.Unquote(value)
	if err != nil {
		t.Fatalf("%s: %q is not a quoted string: %v", path, value, err)
	}
	return decoded
}

// fixtureInt decodes an integer fixture scalar.
func fixtureInt(t *testing.T, path, value string) int {
	t.Helper()
	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s: %q is not an integer: %v", path, value, err)
	}
	return n
}

// fixtureFloat decodes a float fixture scalar.
func fixtureFloat(t *testing.T, path, value string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("%s: %q is not a number: %v", path, value, err)
	}
	return f
}

// checkFixtureProvenance re-derives a detector-provenance fixture's hits
// from the live detector, so a fixture cannot drift into fiction.
func checkFixtureProvenance(t *testing.T, detector *Detector, fixture goldenFixture) {
	t.Helper()
	live := detector.ScanCertain([]byte(fixture.Input))
	if len(live) != len(fixture.Hits) {
		t.Fatalf("the detector now reports %d hits, the fixture records %d", len(live), len(fixture.Hits))
	}
	for i, hit := range live {
		if hit != fixture.Hits[i] {
			t.Fatalf("hit %d has drifted: detector %+v, fixture %+v", i, hit, fixture.Hits[i])
		}
	}
}

// assertNoRewriteCanary checks that no canary reaches the output, the
// account, the formatted result or an error, in any of three encodings.
func assertNoRewriteCanary(t *testing.T, fixture goldenFixture, result RewriteResult, err error) {
	t.Helper()
	surfaces := []string{string(result.Text), fmt.Sprintf("%+v", result), fmt.Sprintf("%v", result.Replacements)}
	for _, r := range result.Replacements {
		surfaces = append(surfaces, r.Tag.String(), string(r.Class), r.Pattern)
	}
	if err != nil {
		surfaces = append(surfaces, err.Error(), fmt.Sprintf("%+v", err))
	}
	for _, canary := range fixture.Canaries {
		forms := []string{
			canary,
			hex.EncodeToString([]byte(canary)),
			base64.StdEncoding.EncodeToString([]byte(canary)),
			base64.RawURLEncoding.EncodeToString([]byte(canary)),
		}
		for _, surface := range surfaces {
			for _, form := range forms {
				if strings.Contains(surface, form) {
					t.Fatalf("%s: a canary survived into %q", fixture.Name, surface)
				}
			}
		}
	}
}

// singleHit is the turn and hit the non-fixture tests share.
func singleHit() (string, DetectionHit) {
	text := "OPENAI_API_KEY=sk-Canary0000AAAA1111BBBB2222CCCC3333\n"
	return text, DetectionHit{
		Class: ClassAPIKey, Pattern: "vendor-api-key-prefix", Offset: 15, Len: 37,
		Confidence: ConfidenceCertain, SuggestedName: "OPENAI_API_KEY",
	}
}
