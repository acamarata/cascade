// hygiene_home_test.go holds the redirected-HOME + clean-tree assertion
// tests. Split out of hygiene_test.go under R-14.117 (Art.10.3's 300-line
// cap) — filecap_test.go's own gate is what caught hygiene_test.go going
// to 308 lines and enforces the split stays honest.
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Env vars the CI redirected-HOME job sets to point this live check at
// what the actual suite run (a separate CI step, per hygiene.go's package
// doc — never nested inside this test binary) produced.
const (
	HygieneHomeDirEnvVar       = "CASCADE_HYGIENE_HOME_DIR"
	HygieneGitBeforeFileEnvVar = "CASCADE_HYGIENE_GIT_BEFORE_FILE"
	HygieneGitAfterFileEnvVar  = "CASCADE_HYGIENE_GIT_AFTER_FILE"
)

// TestHygieneEnvironment_Live is Art.7.1's actual live CI check: the
// redirected-HOME dir the real suite ran under must still be empty, and
// the git-status snapshot taken before the suite must match the one taken
// after. Skipped locally (no env vars set) — see hygiene.go's package doc
// for why this is env-var-gated rather than running the suite itself.
func TestHygieneEnvironment_Live(t *testing.T) {
	homeDir := os.Getenv(HygieneHomeDirEnvVar)
	beforeFile := os.Getenv(HygieneGitBeforeFileEnvVar)
	afterFile := os.Getenv(HygieneGitAfterFileEnvVar)
	if homeDir == "" || beforeFile == "" || afterFile == "" {
		t.Skipf("hygiene gate: set %s, %s, %s to run this check (see .github/workflows/ci.yml redirected-home job)",
			HygieneHomeDirEnvVar, HygieneGitBeforeFileEnvVar, HygieneGitAfterFileEnvVar)
	}

	entries, err := HomeDirEntries(homeDir)
	if err != nil {
		t.Fatalf("HomeDirEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("hygiene gate: the suite left %d entr(y/ies) under the redirected HOME: %v", len(entries), entries)
	}

	before, err := os.ReadFile(beforeFile)
	if err != nil {
		t.Fatalf("hygiene gate: reading %s: %v", beforeFile, err)
	}
	after, err := os.ReadFile(afterFile)
	if err != nil {
		t.Fatalf("hygiene gate: reading %s: %v", afterFile, err)
	}
	if ok, diff := AssertGitStatusUnchanged(string(before), string(after)); !ok {
		t.Errorf("hygiene gate: tree changed during the suite run:\n%s", diff)
	}
}

func TestHomeDirEntries_EmptyDirIsUntouched(t *testing.T) {
	dir := t.TempDir()
	entries, err := HomeDirEntries(dir)
	if err != nil {
		t.Fatalf("HomeDirEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected zero entries in a fresh t.TempDir(), got %v", entries)
	}
}

func TestHomeDirEntries_MissingDirIsUntouched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	entries, err := HomeDirEntries(dir)
	if err != nil {
		t.Fatalf("HomeDirEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected zero entries for a never-created dir, got %v", entries)
	}
}

// TestHomeDirEntries_SeededViolationRed materializes the dirty-home
// fixture (a leaked stray file) into t.TempDir() and proves HomeDirEntries
// reports it — the exact shape a redirected-HOME assertion step in CI
// checks after the real suite runs (this test proves the ASSERTION
// primitive, not a live suite run; see hygiene.go's package doc for why
// the live run itself is a separate CI step, not nested here).
func TestHomeDirEntries_SeededViolationRed(t *testing.T) {
	src := filepath.Join(hygieneFixtureDir(t), "dirty-home")
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("hygiene gate: materializing fixture: %v", err)
	}
	entries, err := HomeDirEntries(dst)
	if err != nil {
		t.Fatalf("HomeDirEntries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected the materialized leaked-cache-file.txt to be reported, got zero entries")
	}
}

// TestGitStatusPorcelain_ReadsRealTree is a smoke test, not a cleanliness
// assertion: the real repo tree, mid-ticket, legitimately carries this
// ticket's own new, not-yet-committed files (R-14.111: agents make no git
// operations, T0 commits once per ticket), so asserting "empty" here would
// fail on every active ticket by construction and prove nothing about
// Art.7.1. The actual "clean tree after the suite" assertion Art.7.1 asks
// for is a BEFORE/AFTER comparison — see AssertGitStatusUnchanged and
// docs/developer/quality-gates.md — this test only proves
// GitStatusPorcelain itself runs and returns without error against a real
// repo.
func TestGitStatusPorcelain_ReadsRealTree(t *testing.T) {
	if _, err := GitStatusPorcelain(hygieneModuleRoot(t)); err != nil {
		t.Fatalf("GitStatusPorcelain: %v", err)
	}
}

// TestAssertGitStatusUnchanged_NoDrift proves the before/after comparison
// Art.7.1's live CI check actually uses: two identical snapshots (a suite
// run that touched nothing beyond what was already dirty/clean before it
// started) report no drift.
func TestAssertGitStatusUnchanged_NoDrift(t *testing.T) {
	snapshot := " M some/already-dirty/file.go\n?? some/already-untracked/file.go"
	ok, diff := AssertGitStatusUnchanged(snapshot, snapshot)
	if !ok {
		t.Fatalf("expected identical before/after snapshots to report unchanged, got diff: %s", diff)
	}
}

// TestAssertGitStatusUnchanged_SeededViolationRed proves the drift case: a
// suite run that leaves ONE new dirty/untracked entry behind — exactly the
// v1 HOME-pollution failure mode Art.7.1 exists to catch — is reported.
func TestAssertGitStatusUnchanged_SeededViolationRed(t *testing.T) {
	before := " M some/already-dirty/file.go"
	after := " M some/already-dirty/file.go\n?? leaked/by/the/suite.tmp"
	ok, diff := AssertGitStatusUnchanged(before, after)
	if ok {
		t.Fatal("expected drift between before/after snapshots to be reported, got unchanged")
	}
	if !strings.Contains(diff, "leaked/by/the/suite.tmp") {
		t.Fatalf("expected the diff to name the leaked path, got: %s", diff)
	}
}

// TestGitStatusPorcelain_SeededViolationRed materializes the dirty-tree
// fixture into a throwaway git repo, commits it, then modifies the
// committed file — proving GitStatusPorcelain reports a non-empty status
// exactly as it would if a test had modified a tracked file in place.
func TestGitStatusPorcelain_SeededViolationRed(t *testing.T) {
	src := filepath.Join(hygieneFixtureDir(t), "dirty-tree")
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatalf("hygiene gate: materializing fixture: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hygiene gate: git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "gate@example.invalid")
	run("config", "user.name", "gate")
	run("add", "-A")
	run("commit", "-q", "-m", "seeded fixture")

	if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("modified after commit\n"), 0o644); err != nil {
		t.Fatalf("hygiene gate: modifying fixture file: %v", err)
	}

	out, err := GitStatusPorcelain(dir)
	if err != nil {
		t.Fatalf("GitStatusPorcelain: %v", err)
	}
	if out == "" {
		t.Fatal("expected a non-empty git status after modifying a tracked file, got empty")
	}
}
