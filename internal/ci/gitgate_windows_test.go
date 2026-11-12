//go:build windows

// Purpose: verify Windows refuses installation before any filesystem change.
package ci

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGitGateWindowsRefuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo := filepath.Join(home, "absent.git")
	if err := GitGateInstall(repo, "repo-1", "missing", "bad"); !errors.Is(err, ErrWindowsNotSupported) {
		t.Fatalf("install: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("install wrote files: %v, %v", entries, err)
	}
}

func TestGitGateRealPush(t *testing.T)                 { t.Skip(ErrWindowsNotSupported.Error()) }
func TestGitGateInstallIdempotent(t *testing.T)        { t.Skip(ErrWindowsNotSupported.Error()) }
func TestGitGateHookPinsBinary(t *testing.T)           { t.Skip(ErrWindowsNotSupported.Error()) }
func TestGitGateHookRejectsChangedBinary(t *testing.T) { t.Skip(ErrWindowsNotSupported.Error()) }
