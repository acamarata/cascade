// Purpose: fixture helpers shared by every _test.go file in this package,
// plus unit tests for term-file loading/validation and Run()'s input
// gates. walk.go's file-list/matching mechanics have their own tests in
// walk_test.go, mirroring the sweep.go/walk.go split.
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
)

// fixture builds an isolated, synthetic v2-tree root, so every test drives
// Run()/run() against real files on disk without touching this repo's own
// tree (C13: no hardcoded classification, only real files the code
// actually reads).
type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir()}
	f.writeProvenance(nil)
	return f
}

func (f *fixture) write(relPath, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, relPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", p, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", p, err)
	}
}

func (f *fixture) writeTerms(terms []Term) {
	data, err := json.Marshal(struct {
		V1Source string `json:"v1_source"`
		Terms    []Term `json:"terms"`
	}{"fixture-sha", terms})
	if err != nil {
		f.t.Fatalf("marshal terms: %v", err)
	}
	f.write("internal/migration/sweep/testdata/v1-terms.json", string(data))
}

func (f *fixture) writeProvenance(refs []ProvenanceRef) {
	if refs == nil {
		refs = []ProvenanceRef{}
	}
	data, err := json.Marshal(struct {
		Refs []ProvenanceRef `json:"refs"`
	}{refs})
	if err != nil {
		f.t.Fatalf("marshal provenance refs: %v", err)
	}
	f.write("internal/migration/sweep/testdata/provenance-refs.json", string(data))
}

// source writes one v2-tree source file under a declared walk root.
func (f *fixture) source(relPath, content string) { f.write(relPath, content) }

// filesList writes rels (already root-relative, slash-separated) as a
// NUL-separated file and returns its path, mimicking `git ls-files -z`.
func (f *fixture) filesList(rels ...string) string {
	f.t.Helper()
	p := filepath.Join(f.root, "files.list")
	var buf []byte
	for _, r := range rels {
		buf = append(buf, []byte(r)...)
		buf = append(buf, 0)
	}
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		f.t.Fatalf("write files list: %v", err)
	}
	return p
}

// run invokes Run() with --root/--files and any extra args.
func (f *fixture) run(filesPath string, args ...string) int {
	f.t.Helper()
	full := append([]string{"--root", f.root, "--files", filesPath}, args...)
	return Run(full)
}

// runCaptured is run over a buffer-backed Writer; it returns the exit code
// and everything the sweep wrote to stderr.
func (f *fixture) runCaptured(filesPath string, args ...string) (int, string) {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"--root", f.root, "--files", filesPath}, args...)
	rc := runArgs(full, output.New(&stdout, &stderr, false, false, false, true))
	return rc, stderr.String()
}

// --- LoadTermFile ---

func TestLoadTermFile_ValidatesShapeAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing":                    "",
		"malformed":                  "{not json",
		"empty list":                 `{"terms":[]}`,
		"blank term":                 `{"terms":[{"term":"  ","class":"command","replacement":[],"state":""}]}`,
		"bad class":                  `{"terms":[{"term":"x","class":"nope","replacement":[],"state":""}]}`,
		"duplicate term":             `{"terms":[{"term":"x","class":"command","replacement":[],"state":""},{"term":"x","class":"marker","replacement":[],"state":""}]}`,
		"state-replacement mismatch": `{"terms":[{"term":"x","class":"command","replacement":["a.go"],"state":""}]}`,
		"done without anchor":        `{"terms":[{"term":"x","class":"command","replacement":["a.go"],"anchor":"","state":"done"}]}`,
		"absolute replacement path":  `{"terms":[{"term":"x","class":"command","replacement":["/etc/passwd"],"anchor":"a","state":"done"}]}`,
		"dotdot replacement path":    `{"terms":[{"term":"x","class":"command","replacement":["../a.go"],"anchor":"a","state":"done"}]}`,
		"rooted backslash path":      `{"terms":[{"term":"x","class":"command","replacement":["\\x.go"],"anchor":"a","state":"done"}]}`,
		"drive relative path":        `{"terms":[{"term":"x","class":"command","replacement":["C:x.go"],"anchor":"a","state":"done"}]}`,
		"drive absolute path":        `{"terms":[{"term":"x","class":"command","replacement":["C:\\x.go"],"anchor":"a","state":"done"}]}`,
		"backslash dotdot path":      `{"terms":[{"term":"x","class":"command","replacement":["..\\a.go"],"anchor":"a","state":"done"}]}`,
		"claude replacement path":    `{"terms":[{"term":"x","class":"command","replacement":[".claude/a.go"],"anchor":"a","state":"done"}]}`,
		"testdata replacement path":  `{"terms":[{"term":"x","class":"command","replacement":["internal/x/testdata/a.go"],"anchor":"a","state":"done"}]}`,
		"marker with replacement":    `{"terms":[{"term":"x","class":"marker","replacement":["a.go"],"anchor":"a","state":"done"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".json")
			if content != "" {
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadTermFile(p); err == nil {
				t.Fatalf("case %q: expected an error, got nil", name)
			}
		})
	}
}

func TestLoadTermFile_ParsesValidTerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "terms.json")
	body := `{"v1_source":"abc","terms":[{"term":"zz-fixture-marker","class":"marker","replacement":[],"anchor":"","state":""}]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	terms, err := LoadTermFile(p)
	if err != nil || len(terms) != 1 || terms[0].Term != "zz-fixture-marker" {
		t.Fatalf("terms=%v err=%v", terms, err)
	}
}

func TestSweep_UntrackedFileIgnored(t *testing.T) {
	f := newFixture(t)
	f.writeTerms([]Term{{Term: "zz-fixture-marker", Class: "marker"}})
	f.source("internal/tracked.go", "nothing of interest\n")
	// On disk and holding the term, but absent from the tracked list.
	f.source("internal/untracked.go", "zz-fixture-marker\n")
	list := f.filesList("internal/tracked.go")
	if rc := f.run(list, "--fail-on-unresolved"); rc != 0 {
		t.Fatalf("rc=%d, want 0 (the untracked file's UNRESOLVED hit must never be seen)", rc)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "docs", "migration", "v1-pointer-inventory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "internal/untracked.go") || strings.Contains(string(data), "zz-fixture-marker") {
		t.Fatalf("inventory lists the untracked file:\n%s", data)
	}
}

func TestSweep_NULFileSkipped(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "internal", "bin.dat")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("zz-fixture-marker\x00binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hits, err := (Sweeper{Root: root, Terms: []Term{{Term: "zz-fixture-marker", Class: "marker"}}, Files: []string{"internal/bin.dat"}}).Scan()
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits=%v err=%v, want the NUL-containing file skipped", hits, err)
	}
}

// --- Run(): input gates ---

func TestSweep_FilesListRequired(t *testing.T) {
	f := newFixture(t)
	f.writeTerms([]Term{{Term: "zz-fixture-marker", Class: "marker"}})
	if rc := Run([]string{"--root", f.root}); rc != 2 {
		t.Fatalf("rc=%d, want 2 (no --files)", rc)
	}
	empty := filepath.Join(f.root, "empty.list")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := f.run(empty); rc != 2 {
		t.Fatalf("rc=%d, want 2 (empty --files list)", rc)
	}
	if rc := f.run(filepath.Join(f.root, "does-not-exist.list")); rc != 2 {
		t.Fatalf("rc=%d, want 2 (unreadable --files list)", rc)
	}
}

func TestSweepRequiresTermsFile(t *testing.T) {
	f := newFixture(t) // no v1-terms.json written
	list := f.filesList("README.md")
	if rc := f.run(list, "--dry-run"); rc != 2 {
		t.Fatalf("rc=%d, want 2 (missing term file)", rc)
	}
	f.writeTerms(nil) // present but declares zero terms
	if rc := f.run(list, "--dry-run"); rc != 2 {
		t.Fatalf("rc=%d, want 2 (empty term file)", rc)
	}
}

func TestSweep_IgnoresPrivatePlanningTree(t *testing.T) {
	f := newFixture(t)
	// A private planning tree that WOULD have mapped term X to a closed
	// ticket under the old inventory/ID-map/closed-manifest resolver. The
	// new resolver reads none of this: X stays UNRESOLVED regardless.
	f.write(".claude/planning/p1/01-FEATURE-INVENTORY.md", "| X feature | CORE -- B/S-02.T1 |\n")
	f.write(".claude/planning/p1/13-ID-MAP.tsv", "ticket_id\tepic\tsprint\tticket\nPX-E02-W1-S02-T1\tB\tS-02\tT1\n")
	f.source("internal/a.go", "zz-fixture-X mention\n")
	f.writeTerms([]Term{{Term: "zz-fixture-X", Class: "command"}}) // declared with empty replacement
	list := f.filesList("internal/a.go")
	if rc := f.run(list, "--fail-on-unresolved"); rc != 3 {
		t.Fatalf("rc=%d, want 3 (X stays UNRESOLVED; the private planning tree is never consulted)", rc)
	}
}

func TestSweep_PlanningFlagRefused(t *testing.T) {
	f := newFixture(t)
	f.writeTerms([]Term{{Term: "zz-fixture-marker", Class: "marker"}})
	list := f.filesList("README.md")
	if rc := Run([]string{"--root", f.root, "--files", list, "--planning", "/nonexistent"}); rc != 2 {
		t.Fatalf("rc=%d, want 2 (--planning is not a recognized flag)", rc)
	}
}

// TestSweep_HelpPrintsUsage: -h exits 2 and prints the flag usage through
// the injected Writer, not only flag's bare "help requested" error.
func TestSweep_HelpPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rc := runArgs([]string{"-h"}, output.New(&stdout, &stderr, false, false, false, true))
	if rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
	for _, want := range []string{"usage: go run ./internal/migration/sweep --files FILE", "-files", "-fail-on-unresolved"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q lacks %q", stderr.String(), want)
		}
	}
}
