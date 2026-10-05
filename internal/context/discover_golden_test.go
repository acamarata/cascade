package context

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
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
		role := e.Role // harvested fixtures carry the retired labels: map by role
		if r, ok := legacyTierName(role); ok {
			role = r.String()
		}
		fmt.Fprintf(&b, "%s present=%t dir=%q\n", role, e.Present, dir)
	}
	return b.String()
}

// TestDiscoverNonDirCascadeEntryAbsent: a `.cascade` that is a regular file or
// a FIFO, at the repo root or a middle PAC dir, leaves that tier Absent with
// no finding and no error, and never stalls the walk or hides other tiers.
func TestDiscoverNonDirCascadeEntryAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs are a POSIX feature")
	}
	kinds := map[string]func(*testing.T, string){
		"file": func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte("not a dir"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"fifo": mkfifo,
	}
	for _, at := range []string{"root", "mid"} {
		for kind, plant := range kinds {
			t.Run(at+"/"+kind, func(t *testing.T) { checkNonDirCascade(t, at == "root", plant) })
		}
	}
}

// checkNonDirCascade plants the non-directory .cascade at the repo root (or
// the middle dir), seeds real tiers elsewhere, and asserts the outcome.
func checkNonDirCascade(t *testing.T, atRoot bool, plant func(*testing.T, string)) {
	root := resolvedTempDir(t)
	repo := filepath.Join(root, "repo")
	mid, cwd := filepath.Join(repo, "a"), filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	bad, kept := mid, repo
	if atRoot {
		bad, kept = repo, mid
	}
	plantTier(t, kept, "kept")
	plantTier(t, cwd, "leaf")
	plant(t, filepath.Join(bad, tierDirName))

	var records []TierRecord
	var err error
	if !returnsWithin(t, "Discover", func() {
		records, err = Discover(context.Background(), cwd, fixedHome(filepath.Join(root, "home")))
	}) {
		return
	}
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	seen := map[string]TierRecord{}
	for _, r := range records {
		seen[r.Dir] = r
	}
	// A middle dir with no readable tier file gets no record at all
	// (chainBelowRoot); the root always gets an Absent one.
	r, ok := seen[bad]
	if !ok && atRoot {
		t.Errorf("no record for the repo root %s", bad)
	}
	if ok && (!r.Absent || r.Content != "" || r.Findings != nil) {
		t.Errorf("tier at %s = %+v, want Absent with no finding", bad, r)
	}
	if r := seen[kept]; r.Absent || r.Content != "kept" {
		t.Errorf("tier at %s = %+v, want the seeded content kept", kept, r)
	}
	if r := seen[cwd]; r.Absent || r.Content != "leaf" {
		t.Errorf("cwd tier = %+v, want the seeded content leaf", r)
	}
}

func skipNoSymlinkOrRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
}

func TestLoadTierFileAbsentUnderRealDir(t *testing.T) {
	dir := resolvedTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, tierDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := loadTier(TierPRC, dir, 3)
	want := TierRecord{Role: TierPRC, Ordinal: 3, Dir: dir, Path: filepath.Join(dir, tierDirName, tierFileName), Absent: true}
	if err != nil || rec.Absent != want.Absent || rec.Path != want.Path || rec.Content != "" || len(rec.Findings) != 0 {
		t.Fatalf("loadTier = %+v, %v; want %+v with no error", rec, err, want)
	}
}

func TestLoadTierUnreadableTierFileIsPermissionDenied(t *testing.T) {
	skipNoSymlinkOrRoot(t)
	root := resolvedTempDir(t)
	repo := filepath.Join(root, "repo")
	plantTier(t, repo, "x")
	runGit(t, repo, "init", "-q")
	td := filepath.Join(repo, tierDirName)
	if err := os.Chmod(td, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(td, 0o755) })
	path := filepath.Join(td, tierFileName)
	wantMsg := "context: stat tier file " + path
	_, err := loadTier(TierPRC, repo, 3)
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || err == nil || !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("loadTier = %v, want KindPermissionDenied containing %q", err, wantMsg)
	}
	_, err = Discover(context.Background(), repo, fixedHome(filepath.Join(root, "home")))
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || err == nil || !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("Discover = %v, want the same KindPermissionDenied containing %q", err, wantMsg)
	}
}

func TestLoadTierSwappedForAnotherFileAbsent(t *testing.T) {
	dir := resolvedTempDir(t)
	plantTier(t, dir, "original")
	afterTierLstat = func(path string) {
		other := path + ".new"
		if err := os.WriteFile(other, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterTierLstat = func(string) {} })
	rec, err := loadTier(TierPRC, dir, 3)
	if err != nil || !rec.Absent || rec.Content != "" || len(rec.Findings) != 0 {
		t.Fatalf("loadTier = %+v, %v; want Absent, unread, no findings", rec, err)
	}
}
