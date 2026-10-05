package main

// Purpose: the CLI surface, the write phase's refusals and the README
// provenance merge.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestRun_ArgumentRefusals(t *testing.T) {
	newModule(t)
	for name, args := range map[string][]string{
		"unknown-flag":   {"--bogus"},
		"positional":     {"extra"},
		"unknown-domain": {"--domain", "recall"},
	} {
		if code, _, _ := runHarvest(t, args...); code != exitRefused {
			t.Errorf("%s: exit %d, want %d", name, code, exitRefused)
		}
	}
}

func TestRun_DryRunWritesNothing(t *testing.T) {
	root := newModule(t)
	code, stdout, stderr := runHarvest(t, "--dry-run")
	if code != exitOK || !strings.Contains(stdout, "8 fixture(s) computed, 0 written") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := readOutputs(t, root); len(got) != 0 {
		t.Fatalf("dry run wrote %d files", len(got))
	}
}

func TestRun_DomainFilter(t *testing.T) {
	root := newModule(t)
	if code, _, stderr := runHarvest(t, "--domain", "vault"); code != exitOK {
		t.Fatalf("harvest failed: %s", stderr)
	}
	for path := range readOutputs(t, root) {
		if !strings.HasPrefix(path, outputDirs[v1.DomainVault]+"/") {
			t.Errorf("--domain vault wrote %s", path)
		}
	}
}

func TestRun_ModuleRootFailure(t *testing.T) {
	sealEnv(t)
	prev := moduleRootFor
	moduleRootFor = func() (string, error) { return findModuleRoot() }
	t.Cleanup(func() { moduleRootFor = prev })
	root, err := moduleRootFor()
	if err != nil || filepath.Base(root) == "" {
		t.Fatalf("findModuleRoot = %q, %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root has no go.mod: %v", err)
	}
	t.Chdir(t.TempDir())
	if code, _, _ := runHarvest(t); code != exitRefused {
		t.Errorf("a run with no module root exited %d", code)
	}
}

func TestApply_RefusesForeignOutputFile(t *testing.T) {
	root := newModule(t)
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(outputDirs[v1.DomainVault]), "notes.txt"), []byte("x"))
	code, _, stderr := runHarvest(t)
	if code != exitRefused || !strings.Contains(stderr, `unexpected file "notes.txt"`) {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestApply_RefusesChangedFixtureBytes(t *testing.T) {
	root := newModule(t)
	if code, _, _ := runHarvest(t); code != exitOK {
		t.Fatal("first run failed")
	}
	dir := filepath.Join(root, filepath.FromSlash(outputDirs[v1.DomainVault]))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != readmeName {
			writeTestFile(t, filepath.Join(dir, entry.Name()), []byte("{}\n"))
		}
	}
	if code, _, stderr := runHarvest(t); code != exitRefused || !strings.Contains(stderr, "exists with different bytes") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestReadme_RowsMergeOnce(t *testing.T) {
	digest := strings.Repeat("b", 64)
	row := provenanceRow{Domain: "vault", Input: "vault/vault.env", Digest: digest, Format: "1"}
	merged, err := mergeRows(nil, []provenanceRow{row, row}, "2026-10-05")
	if err != nil || len(merged) != 1 || merged[0].Captured != "2026-10-05" {
		t.Fatalf("merge = %v, %v", merged, err)
	}
	text := string(renderReadme(v1.DomainVault, merged))
	parsed := parseRows(text)
	again, err := mergeRows(parsed, []provenanceRow{row}, "2027-01-01")
	if err != nil || len(again) != 1 || again[0].Captured != "2026-10-05" {
		t.Fatalf("re-merge = %v, %v", again, err)
	}
	if _, err := mergeRows(append(parsed, parsed...), nil, "x"); err == nil {
		t.Error("a README with a duplicated provenance row was accepted")
	}
	if got := parseRows("| a | b | not-a-digest | 1 | d |\n| only | two |\n"); len(got) != 0 {
		t.Errorf("malformed rows parsed: %v", got)
	}
}

func TestReadme_GuardRefusesPrivateText(t *testing.T) {
	if err := guardReadme([]byte("see /Users/someone/x\n")); err == nil {
		t.Error("a README with a home path passed the guard")
	}
}

func TestApply_ClockStampsNewRowsOnly(t *testing.T) {
	root := newModule(t)
	if code, _, _ := runHarvest(t); code != exitOK {
		t.Fatal("first run failed")
	}
	clockFor = func() runtime.Clock { return runtime.NewFixedClock(testInstant.AddDate(1, 0, 0)) }
	if code, _, _ := runHarvest(t); code != exitOK {
		t.Fatal("second run failed")
	}
	readme := readOutputs(t, root)[outputDirs[v1.DomainConfig]+"/"+readmeName]
	if !strings.Contains(string(readme.data), " | 2026-10-05 |") || strings.Contains(string(readme.data), "2027") {
		t.Error("a repeat run restamped an existing provenance row")
	}
}
