package context

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDiscoverGolden runs every harvested v1-goldens/scenario-*.json
// fixture (see testdata/v1-goldens/README.md for provenance) and asserts
// byte-for-byte parity between the rendered expectation and the rendered
// Discover() output. Split from discover_test.go solely to stay under
// Art.10.3's 300-line file cap.
func TestDiscoverGolden(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("testdata", "v1-goldens", "scenario-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no scenario fixtures found under testdata/v1-goldens")
	}
	sort.Strings(matches)

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			runGoldenScenario(t, path)
		})
	}
}

type goldenExpect struct {
	Role    string `json:"role"`
	Present bool   `json:"present"`
	Dir     string `json:"dir"`
}

type goldenScenario struct {
	Description string         `json:"description"`
	Layout      []string       `json:"layout"`
	GitRepo     string         `json:"git_repo"`
	HomeOffset  string         `json:"home_offset"`
	CwdOffset   string         `json:"cwd_offset"`
	Expect      []goldenExpect `json:"expect"`
}

func runGoldenScenario(t *testing.T, fixturePath string) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var sc goldenScenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	root := resolvedTempDir(t)
	for _, rel := range sc.Layout {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if sc.GitRepo != "" {
		runGit(t, filepath.Join(root, sc.GitRepo), "init", "-q")
	}
	home := filepath.Join(root, sc.HomeOffset)
	cwd := filepath.Join(root, sc.CwdOffset)

	records, err := Discover(context.Background(), cwd, fixedHome(home))
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	got := renderRecords(root, records)
	want := renderExpect(sc.Expect)
	if got != want {
		t.Errorf("%s\n--- want ---\n%s--- got ---\n%s", sc.Description, want, got)
	}
}

// renderRecords and renderExpect produce identical canonical text for a
// resolved []TierRecord and a fixture's expected outcome respectively, so
// the golden comparison is a plain byte-for-byte string equality.
//
// "present" here means the tier has a resolved candidate DIRECTORY (the
// discovery decision this ticket delivers), not that a tier instruction
// file was found inside it — the fixtures never write a tier file, so
// TierRecord.Absent (which tracks file presence) is deliberately not what
// is compared.
func renderRecords(root string, records []TierRecord) string {
	var b strings.Builder
	for _, rec := range records {
		dir := ""
		present := rec.Dir != ""
		if present {
			rel, err := filepath.Rel(root, rec.Dir)
			if err == nil && rel != "." {
				dir = filepath.ToSlash(rel)
			}
		}
		fmt.Fprintf(&b, "%s present=%t dir=%q\n", rec.Role, present, dir)
	}
	return b.String()
}

func renderExpect(expect []goldenExpect) string {
	var b strings.Builder
	for _, e := range expect {
		dir := ""
		if e.Present {
			dir = e.Dir
		}
		fmt.Fprintf(&b, "%s present=%t dir=%q\n", e.Role, e.Present, dir)
	}
	return b.String()
}
