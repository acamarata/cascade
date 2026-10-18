//go:build windows

package topology

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureHomeWindowsRefusal: windows returns an existing directory and
// refuses a missing path, a file or a symlink; it never creates one.
func TestEnsureHomeWindowsRefusal(t *testing.T) {
	root := isolatedRoot(t)
	dir := filepath.Join(root, "profiles", "acct-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureHome(root, "acct-1")
	if err != nil || got != dir {
		t.Fatalf("existing dir: EnsureHome = %q, %v", got, err)
	}
	path, err := EnsureHome(root, "missing")
	requireSentinel(t, err, ErrProfileHomePermission, "missing")
	if path != "" {
		t.Errorf("missing: path = %q", path)
	}
	if _, statErr := os.Stat(filepath.Join(root, "profiles", "missing")); statErr == nil {
		t.Error("windows EnsureHome created a directory")
	}
	file := filepath.Join(root, "profiles", "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureHome(root, "afile")
	requireSentinel(t, err, ErrProfileHomePermission, "afile")
	link := filepath.Join(root, "profiles", "alink")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("cannot create a symlink on this windows host: %v", err)
	}
	path, err = EnsureHome(root, "alink")
	requireSentinel(t, err, ErrProfileHomePermission, "alink")
	if path != "" {
		t.Errorf("symlink: path = %q", path)
	}
}
