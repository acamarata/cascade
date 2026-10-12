// Tests for the identifier sweep's testdata coverage and its no-echo
// output. Testdata is where captured fixtures land, so the sweep scans it
// (only internal/build/testdata, the seeded-violation fixtures, stays
// exempt), and every message the sweep prints names file:line and a
// pattern index, never the pattern text or the matched line. All markers
// below are synthetic.
package build

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sweepTempHome points HOME and USERPROFILE at a fresh temp dir so no test
// here can read or write the operator's real home.
func sweepTempHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// plantedMarker is composed at runtime so this file never matches its own
// pattern (see the note in hook_test.go).
func plantedMarker() string { return "zz-planted-" + "canary-7731" }

func TestSweepScansTestdata(t *testing.T) {
	sweepTempHome(t)
	root := t.TempDir()
	marker := plantedMarker()
	files := map[string]string{
		"a/testdata/x.json":                  "{\n  \"host\": \"" + marker + "\"\n}\n",
		"internal/build/testdata/seeded.txt": "seeded " + marker + "\n",
		"a/clean.txt":                        "nothing here\n",
	}
	var rels []string
	for rel, body := range files {
		writeFileT(t, filepath.Join(root, filepath.FromSlash(rel)), body)
		rels = append(rels, rel)
	}
	pats := []*regexp.Regexp{regexp.MustCompile(`unrelated-pattern`), regexp.MustCompile(regexp.QuoteMeta(marker))}

	// Non-vacuous: the exempt fixture really contains a hit, so its absence
	// below proves the exemption and not an empty file.
	if v := SweepContent(pats, "seeded", []byte(files["internal/build/testdata/seeded.txt"])); len(v) != 1 {
		t.Fatalf("control: the exempt fixture should hold exactly one hit, got %d", len(v))
	}

	got, err := SweepFiles(pats, root, rels)
	if err != nil {
		t.Fatalf("SweepFiles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SweepFiles returned %d violation(s), want exactly the testdata hit:\n%s", len(got), FormatSweepViolations(got))
	}
	want := SweepViolation{Source: "a/testdata/x.json", Line: 2, PatternIndex: 1}
	if got[0].Source != want.Source || got[0].Line != want.Line || got[0].PatternIndex != want.PatternIndex {
		t.Errorf("violation = %s:%d #%d, want %s:%d #%d",
			got[0].Source, got[0].Line, got[0].PatternIndex, want.Source, want.Line, want.PatternIndex)
	}
}

func TestFormatSweepViolationsHidesMatch(t *testing.T) {
	marker := plantedMarker()
	line := "  \"base_url\": \"https://" + marker + ".example\""
	pattern := regexp.QuoteMeta(marker)
	pats := []*regexp.Regexp{regexp.MustCompile(`never-matches-x9`), regexp.MustCompile(pattern)}
	v := SweepContent(pats, "a/testdata/probe.json", []byte("{\n"+line+"\n}\n"))
	if len(v) != 1 {
		t.Fatalf("SweepContent found %d hit(s), want 1", len(v))
	}
	got := FormatSweepViolations(v)
	if want := "  - a/testdata/probe.json:2 matches pattern #1"; got != want {
		t.Errorf("FormatSweepViolations() = %q, want %q", got, want)
	}
	for _, leak := range []string{marker, pattern, strings.TrimSpace(line), "base_url"} {
		if strings.Contains(got, leak) {
			t.Errorf("FormatSweepViolations() = %q echoes %q", got, leak)
		}
	}
}

func TestIdentifierSweepSkipsPath(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"internal/build/testdata/seeded-violations/identifiers/leak.txt", true},
		{"internal/build/testdata/x.json", true},
		{"./internal/build/testdata/x.json", true},
		{`internal\build\testdata\x.json`, filepath.Separator == '\\'},
		{"internal/build/testdata", false},
		{"internal/build/sweep.go", false},
		{"providers/anthropic/testdata/gateway/probe.json", false},
		{"internal/ci/testdata/fixtures/runs_success.json", false},
		{"a/testdata/x.json", false},
		{"x/internal/build/testdata/y.json", false},
		{"internal/build/testdatax/y.json", false},
		{"internal/buildx/testdata/y.json", false},
		{"internal/build/sub/testdata/y.json", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IdentifierSweepSkipsPath(c.rel); got != c.want {
			t.Errorf("IdentifierSweepSkipsPath(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestParsePatternsErrorHidesPattern(t *testing.T) {
	secret := "zz-private-" + "host-4410"
	raw := "# comment\n\nok-pattern\n" + secret + "(\n"
	pats, err := ParsePatterns(raw)
	if err == nil {
		t.Fatalf("ParsePatterns accepted an invalid pattern (%d compiled)", len(pats))
	}
	if want := "identifier sweep: invalid pattern at line 4"; err.Error() != want {
		t.Errorf("ParsePatterns error = %q, want %q", err.Error(), want)
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("ParsePatterns error wraps %v; the regexp error quotes the pattern", errors.Unwrap(err))
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "missing closing") {
		t.Errorf("ParsePatterns error = %q leaks the pattern or the regexp error", err.Error())
	}

	allowRaw := "deny-a\n!" + secret + "[\n"
	_, aerr := ParseAllowPatterns(allowRaw)
	if aerr == nil {
		t.Fatal("ParseAllowPatterns accepted an invalid allow pattern")
	}
	if want := "identifier sweep: invalid allow pattern at line 2"; aerr.Error() != want {
		t.Errorf("ParseAllowPatterns error = %q, want %q", aerr.Error(), want)
	}
	if errors.Unwrap(aerr) != nil {
		t.Errorf("ParseAllowPatterns error = %q leaks the allow body or wraps the regexp error", aerr.Error())
	}
}
