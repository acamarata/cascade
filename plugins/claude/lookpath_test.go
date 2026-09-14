package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// The PATH-resolution half of mcp.go's tests. Split out of mcp_test.go to
// stay under the 300-line file cap, which counts test files too.
// TestDefaultLookPathFindsARealExecutable exercises the real PATH resolver
// (the os/exec-free reimplementation) against a real file on a real PATH,
// rather than only ever running through the injected test double.
func TestDefaultLookPathFindsARealExecutable(t *testing.T) {
	dir := t.TempDir()
	name := "cascade-lookpath-probe"
	if runtimeIsWindows() {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // deliberately executable: this is what the resolver must find.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := defaultLookPath("cascade-lookpath-probe")
	if err != nil {
		t.Fatalf("defaultLookPath: %v", err)
	}
	if got != target {
		t.Fatalf("resolved %q, want %q", got, target)
	}
}

// TestDefaultLookPathReportsMissing proves an absent binary is a real
// error, and that an empty PATH entry is skipped rather than resolving to
// a relative path in the working directory.
func TestDefaultLookPathReportsMissing(t *testing.T) {
	t.Setenv("PATH", string(filepath.ListSeparator)+t.TempDir())
	if _, err := defaultLookPath("definitely-not-installed-anywhere"); err == nil {
		t.Fatal("defaultLookPath resolved a binary that does not exist")
	}
}

// TestDefaultLookPathIgnoresDirectories proves a directory whose name
// matches is not mistaken for an executable.
func TestDefaultLookPathIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "shaped-like-a-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := defaultLookPath("shaped-like-a-binary"); err == nil {
		t.Fatal("defaultLookPath resolved a directory as an executable")
	}
}

// TestCandidateNamesCoversBothPlatforms asserts the PATHEXT branch on
// Windows and the single-name branch elsewhere.
func TestCandidateNamesCoversBothPlatforms(t *testing.T) {
	got := candidateNames("cascade")
	if runtimeIsWindows() {
		if len(got) < 2 {
			t.Fatalf("candidateNames on windows = %v, want the PATHEXT suffixes plus the bare name", got)
		}
		return
	}
	if len(got) != 1 || got[0] != "cascade" {
		t.Fatalf("candidateNames = %v, want exactly the bare name", got)
	}
}

// TestIsExecutableFileRejectsAMissingPath covers the stat-error branch.
func TestIsExecutableFileRejectsAMissingPath(t *testing.T) {
	if isExecutableFile(filepath.Join(t.TempDir(), "nothing-here")) {
		t.Fatal("isExecutableFile accepted a path that does not exist")
	}
}
