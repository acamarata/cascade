package generate

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// goldenNames maps each rendered path to its golden under
// testdata/golden/rules/.
var goldenNames = map[string]string{
	".claude/CLAUDE.md":         "CLAUDE.md",
	".claude/rules/root.md":     "rules-root.md",
	".cascade/policy-kernel.md": "policy-kernel.md",
}

// timestampShape matches a calendar date or a clock time.
var timestampShape = regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{2}:\d{2}:\d{2}|\b20\d{2}\b`)

func TestByteStability(t *testing.T) {
	isolate(t)
	v := fixtureView(t)
	a, err := RenderRules(v)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("render counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].GeneratorID != b[i].GeneratorID || !bytes.Equal(a[i].Content, b[i].Content) {
			t.Fatalf("render %d differs between calls: %s", i, a[i].Path)
		}
		if loc := timestampShape.Find(a[i].Content); loc != nil {
			t.Fatalf("%s carries a timestamp-shaped value %q", a[i].Path, loc)
		}
		for _, stamp := range []string{fixedNow.Format("2006-01-02"), fixedNow.Format("15:04")} {
			if bytes.Contains(a[i].Content, []byte(stamp)) {
				t.Fatalf("%s carries the clock value %q", a[i].Path, stamp)
			}
		}
	}
}

func TestRulesGoldens(t *testing.T) {
	isolate(t)
	rs, err := RenderRules(fixtureView(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != len(goldenNames) {
		t.Fatalf("rendered %d files, want %d", len(rs), len(goldenNames))
	}
	update := os.Getenv("CASCADE_UPDATE_RULES_GOLDENS") == "1"
	for _, r := range rs {
		name, ok := goldenNames[r.Path]
		if !ok {
			t.Fatalf("no golden for %s", r.Path)
		}
		p := filepath.Join("testdata", "golden", "rules", name)
		if update {
			if err := os.WriteFile(p, r.Content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r.Content, want) {
			t.Errorf("%s differs from %s:\n%s", r.Path, p, r.Content)
		}
	}
}
