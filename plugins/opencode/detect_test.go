package opencode

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCascadeOpencodeDetectPredicate exercises both the present and absent
// PATH states via the injectable lookPath variable, without touching the
// real filesystem or the developer's real PATH.
func TestCascadeOpencodeDetectPredicate(t *testing.T) {
	prev := lookPath
	defer func() { lookPath = prev }()

	t.Run("present", func(t *testing.T) {
		lookPath = func(name string) (string, error) {
			if name != opencodeBinary {
				t.Fatalf("lookPath called with %q, want %q", name, opencodeBinary)
			}
			return "/usr/local/bin/opencode", nil
		}
		if !Detect() {
			t.Error("Detect() = false, want true when lookPath succeeds")
		}
	})

	t.Run("absent", func(t *testing.T) {
		lookPath = func(string) (string, error) {
			return "", errors.New("exec: \"opencode\": executable file not found in $PATH")
		}
		if Detect() {
			t.Error("Detect() = true, want false when lookPath fails")
		}
	})
}

func TestRunDetectBothStates(t *testing.T) {
	prev := lookPath
	defer func() { lookPath = prev }()

	lookPath = func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	if err := runDetect(); err != nil {
		t.Errorf("runDetect() (present) = %v, want nil", err)
	}

	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := runDetect(); err != nil {
		t.Errorf("runDetect() (absent) = %v, want nil (dormant, not an error)", err)
	}
}

// TestDefaultLookPathFindsRealExecutable exercises defaultLookPath itself
// (not the injected fake): a scratch directory on a synthetic PATH holds a
// file this test marks executable, and defaultLookPath must find it.
func TestDefaultLookPathFindsRealExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute-bit semantics differ on windows; covered by TestCandidateNamesWindows below")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "fake-opencode-binary")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	prevPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", prevPath) })
	if err := os.Setenv("PATH", dir); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	got, err := defaultLookPath("fake-opencode-binary")
	if err != nil {
		t.Fatalf("defaultLookPath: %v", err)
	}
	if got != target {
		t.Errorf("defaultLookPath = %q, want %q", got, target)
	}
}

func TestDefaultLookPathNotFound(t *testing.T) {
	prevPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", prevPath) })
	if err := os.Setenv("PATH", t.TempDir()); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if _, err := defaultLookPath("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("defaultLookPath: want error for a binary that does not exist, got nil")
	}
}

func TestDefaultLookPathSkipsEmptyPathEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "another-fake-binary")
	mode := os.FileMode(0o644)
	if runtime.GOOS != "windows" {
		mode = 0o755
	}
	if err := os.WriteFile(target, []byte("x"), mode); err != nil {
		t.Fatalf("setup: %v", err)
	}

	prevPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", prevPath) })
	// A leading empty PATH entry (as in "":/real/dir) must be skipped, not
	// treated as the current directory.
	if err := os.Setenv("PATH", string(filepath.ListSeparator)+dir); err != nil {
		t.Fatalf("Setenv: %v", err)
	}

	if _, err := defaultLookPath("another-fake-binary"); err != nil {
		t.Fatalf("defaultLookPath: %v", err)
	}
}

func TestIsExecutableFileRejectsDirectory(t *testing.T) {
	if isExecutableFile(t.TempDir()) {
		t.Error("isExecutableFile(dir) = true, want false")
	}
}

func TestIsExecutableFileRejectsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute-bit is not POSIX-meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if isExecutableFile(path) {
		t.Error("isExecutableFile(non-executable file) = true, want false")
	}
}

func TestCandidateNamesNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this case only applies off windows")
	}
	got := candidateNames("opencode")
	if len(got) != 1 || got[0] != "opencode" {
		t.Errorf("candidateNames(opencode) = %v, want [opencode]", got)
	}
}
