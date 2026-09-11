package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestScanLayoutBasic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub"), "b.go", "package a")

	facts, err := ScanLayout(dir)
	if err != nil {
		t.Fatalf("ScanLayout: %v", err)
	}
	if facts.FileCount != 2 || facts.DirCount != 1 {
		t.Fatalf("facts = %+v, want FileCount=2 DirCount=1", facts)
	}
	if facts.Truncated {
		t.Error("Truncated = true for a small tree")
	}
}

// TestScanLayoutSymlinkLoop proves a self-referential symlink never hangs
// or escapes the walk: a symlinked directory is skipped, never followed.
func TestScanLayoutSymlinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privilege on windows CI")
	}
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(dir, loop); err != nil {
		t.Fatal(err)
	}
	facts, err := ScanLayout(dir)
	if err != nil {
		t.Fatalf("ScanLayout: %v", err)
	}
	if facts.DirCount != 0 {
		t.Errorf("DirCount = %d, want 0 (the symlink is never followed)", facts.DirCount)
	}
}

// TestScanLayoutSymlinkEscape proves a symlink pointing outside root is
// never followed into the escape target.
func TestScanLayoutSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privilege on windows CI")
	}
	outside := t.TempDir()
	writeFile(t, outside, "secret.go", "package secret")

	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	facts, err := ScanLayout(root)
	if err != nil {
		t.Fatalf("ScanLayout: %v", err)
	}
	if facts.FileCount != 0 {
		t.Errorf("FileCount = %d, want 0 (escape/secret.go must never be counted)", facts.FileCount)
	}
}

func TestScanLayoutUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.go")
	writeFile(t, dir, "locked.go", "package a")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if _, err := ScanLayout(dir); err != nil {
		t.Fatalf("ScanLayout: want no error for one unreadable file (skip, don't fail), got %v", err)
	}
}

func TestScanLayoutDepthBound(t *testing.T) {
	dir := t.TempDir()
	cur := dir
	for i := 0; i < walkMaxDepth+10; i++ {
		cur = filepath.Join(cur, "d")
		if err := os.MkdirAll(cur, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := ScanLayout(dir)
	if err != nil {
		t.Fatalf("ScanLayout: %v", err)
	}
	if !facts.Truncated {
		t.Error("Truncated = false for a tree exceeding walkMaxDepth")
	}
}

func TestCaseClashes(t *testing.T) {
	got := caseClashes([]string{"README.md", "readme.md", "a.go"})
	if len(got) != 2 {
		t.Fatalf("caseClashes = %v, want 2 entries", got)
	}
}

func TestScanCIAndHarness(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "CLAUDE.md", "# claude")

	ci, err := ScanCI(dir)
	if err != nil {
		t.Fatalf("ScanCI: %v", err)
	}
	if !ci.GitHubActions {
		t.Error("GitHubActions = false, want true")
	}

	harness, err := ScanHarness(dir)
	if err != nil {
		t.Fatalf("ScanHarness: %v", err)
	}
	if !harness.ClaudeMD || !harness.ClaudeDir {
		t.Errorf("harness = %+v, want ClaudeMD and ClaudeDir true", harness)
	}
}

func TestScanLayoutEmptyRootRefused(t *testing.T) {
	if _, err := ScanLayout(""); err == nil {
		t.Fatal("ScanLayout(\"\"): want error, got nil")
	}
}

func TestScanCIGitLabAndCircle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitlab-ci.yml", "stages: []")
	if err := os.MkdirAll(filepath.Join(dir, ".circleci"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".circleci"), "config.yml", "version: 2.1")

	ci, err := ScanCI(dir)
	if err != nil {
		t.Fatalf("ScanCI: %v", err)
	}
	if !ci.GitLabCI || !ci.CircleCI {
		t.Errorf("ci = %+v, want GitLabCI and CircleCI true", ci)
	}
	if ci.GitHubActions {
		t.Error("GitHubActions = true, want false (no .github/workflows present)")
	}
}

func TestScanHarnessAgentsMD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "# agents")
	harness, err := ScanHarness(dir)
	if err != nil {
		t.Fatalf("ScanHarness: %v", err)
	}
	if !harness.AgentsMD || harness.ClaudeMD || harness.ClaudeDir {
		t.Errorf("harness = %+v, want only AgentsMD true", harness)
	}
}

func TestEvidenceDirExistsPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := evidenceDirExists(dir, ".claude"); err == nil {
		t.Fatal("evidenceDirExists: want error for an unreadable parent, got nil")
	}
}

func TestWalkRepoEntryCountBound(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		writeFile(t, dir, fmt.Sprintf("f%d.go", i), "package a")
	}
	_, fileCount, _, truncated, _, err := walkRepoWithBudget(dir, 10)
	if err != nil {
		t.Fatalf("walkRepoWithBudget: %v", err)
	}
	if !truncated {
		t.Error("Truncated = false, want true (entry count exceeds the injected budget)")
	}
	if fileCount == 0 {
		t.Error("fileCount = 0, want some files counted before truncation")
	}
}
