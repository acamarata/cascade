// Purpose: `cascade context sync --project-list <file>`'s own CLI-layer
//
//	tests (P1-E26-W10-S53-T3): the R-14.166 reachability proof for the
//	--project-list flag itself, plus end-to-end coverage of the
//	--check/--yes/CASCADE_NO_INPUT truth table driven through the real
//	cobra tree (never a direct call into internal/migration, which
//	already has its own unit tests) — no real socket, no "net" import
//	(Art.7.2's default unit lane).
//
// SPORT: cli/context-sync-projects/ADD (P1-E26-W10-S53-T3 sport_updates).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContextSyncProjectListFlagIsMounted is the R-14.166 reachability
// proof for --project-list specifically: Find alone cannot see a flag, so
// this drives Execute() and asserts cobra recognizes the flag rather than
// rejecting it as unknown.
func TestContextSyncProjectListFlagIsMounted(t *testing.T) {
	dir := t.TempDir()
	listPath := filepath.Join(dir, "empty-list.txt")
	if err := os.WriteFile(listPath, nil, 0o600); err != nil {
		t.Fatalf("write list: %v", err)
	}
	out, err := execRootContextSyncProjects(t, dir, nil, "context", "sync", "--check", "--project-list", listPath)
	if err != nil {
		t.Fatalf("--project-list was not accepted: %v (output: %s)", err, out)
	}
}

// execRootContextSyncProjects mirrors execRootContextScope's established
// technique (context_scope_test.go, same package) for driving the real
// root tree end to end, but injects a real, test-controlled Getenv (so
// CASCADE_NO_INPUT is testable) and a stdin reader (so the TTY confirm
// path is testable) — execRootContextScope hardcodes both away, which is
// correct for its own tests but not for this file's.
func execRootContextSyncProjects(t *testing.T, dir string, stdin *strings.Reader, args ...string) (string, error) {
	t.Helper()
	t.Setenv("CASCADE_HOME", dir)
	t.Setenv("CASCADE_SOCKET", dir+"/daemon.sock")
	t.Setenv("HOME", t.TempDir())
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	existing, _, err := root.Find([]string{"context"})
	if err != nil {
		t.Fatalf("find context command: %v", err)
	}
	root.RemoveCommand(existing)
	deps := contextScopeDeps{
		Paths:   fakeContextScopePaths{dir: dir},
		Getenv:  os.Getenv,
		Environ: os.Environ,
	}
	cmd := newContextCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	if stdin != nil {
		root.SetIn(stdin)
	}
	root.SetArgs(args)
	execErr := root.Execute()
	return buf.String(), execErr
}

// syncProjectsFixture seeds one real, hermetic project directory (a
// `.cascade/CASCADE.md` tier source, no `.claude/CLAUDE.md` output yet —
// so it has genuine drift) plus a --project-list file naming it, and
// returns the list file's path.
func syncProjectsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, ".cascade"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, ".cascade", "CASCADE.md"), []byte("## Style\n\nShort sentences.\n"), 0o600); err != nil {
		t.Fatalf("write tier file: %v", err)
	}
	listPath := filepath.Join(root, "projects.txt")
	if err := os.WriteFile(listPath, []byte(project+"\n"), 0o600); err != nil {
		t.Fatalf("write project list: %v", err)
	}
	return listPath
}

// TestContextSyncProjectsCheckNeverWrites drives --check --project-list
// end to end and asserts the drift table is printed, exit is nil (0)
// regardless of drift (this ticket's own --check contract), and nothing
// on disk changed.
func TestContextSyncProjectsCheckNeverWrites(t *testing.T) {
	dir := t.TempDir()
	listPath := syncProjectsFixture(t)
	project := strings.TrimSpace(readFileT(t, listPath))
	out, err := execRootContextSyncProjects(t, dir, nil, "context", "sync", "--check", "--project-list", listPath)
	if err != nil {
		t.Fatalf("--check must exit 0 regardless of drift, got: %v", err)
	}
	if !strings.Contains(out, project) {
		t.Fatalf("output does not mention the scanned project:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(project, ".claude", "CLAUDE.md")); !os.IsNotExist(statErr) {
		t.Fatalf("--check must not write; stat err=%v", statErr)
	}
}

// TestContextSyncProjectsNoInputWithoutYes drives bulk apply mode under
// CASCADE_NO_INPUT=1 without --yes: the acceptance criterion is "report
// printed, no files written, exit 0".
func TestContextSyncProjectsNoInputWithoutYes(t *testing.T) {
	dir := t.TempDir()
	listPath := syncProjectsFixture(t)
	project := strings.TrimSpace(readFileT(t, listPath))
	t.Setenv("CASCADE_NO_INPUT", "1")
	out, err := execRootContextSyncProjects(t, dir, nil, "context", "sync", "--project-list", listPath)
	if err != nil {
		t.Fatalf("NoInput without --yes must exit 0, got: %v", err)
	}
	if !strings.Contains(out, project) {
		t.Fatalf("output does not mention the scanned project:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(project, ".claude", "CLAUDE.md")); !os.IsNotExist(statErr) {
		t.Fatalf("NoInput without --yes must not write; stat err=%v", statErr)
	}
}

// TestContextSyncProjectsNoInputWithYesWrites drives the same run WITH
// --yes: the file must be written, matching internal/migration's own unit
// test for this truth-table cell, but end to end through the real cobra
// command this time.
func TestContextSyncProjectsNoInputWithYesWrites(t *testing.T) {
	dir := t.TempDir()
	listPath := syncProjectsFixture(t)
	project := strings.TrimSpace(readFileT(t, listPath))
	t.Setenv("CASCADE_NO_INPUT", "1")
	if _, err := execRootContextSyncProjects(t, dir, nil, "context", "sync", "--project-list", listPath, "--yes"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(project, ".claude", "CLAUDE.md")); statErr != nil {
		t.Fatalf("want the file written under --yes, stat err=%v", statErr)
	}
}

// TestContextSyncProjectsInteractiveConfirmWrites drives an interactive
// (no CASCADE_NO_INPUT, no --yes) run with stdin answering "y": the
// confirm prompt this ticket's confirmProjectApply writes must appear on
// stderr/stdout and the file must land.
func TestContextSyncProjectsInteractiveConfirmWrites(t *testing.T) {
	dir := t.TempDir()
	listPath := syncProjectsFixture(t)
	project := strings.TrimSpace(readFileT(t, listPath))
	stdin := strings.NewReader("y\n")
	out, err := execRootContextSyncProjects(t, dir, stdin, "context", "sync", "--project-list", listPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "apply drift to "+project) {
		t.Fatalf("confirmation prompt missing from output:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(project, ".claude", "CLAUDE.md")); statErr != nil {
		t.Fatalf("want the file written after a 'y' answer, stat err=%v", statErr)
	}
}

// readFileT is a tiny t.Fatal-on-error os.ReadFile wrapper so the fixture
// helpers above stay one line each.
func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test-owned temp-dir path.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
