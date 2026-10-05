//go:build !windows

// Purpose: the startup identity and CheckSkew (upgrade_skew.go). The digest
//   is captured once from the file the daemon started from; CheckSkew
//   compares it with the installed file read at check time, and fails
//   closed when either side cannot be read.
// Constraints: every binary is a file in t.TempDir(); the executable
//   resolver is swapped through the package's startup pointer, never by
//   touching the real executable.

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// pinResolver installs a fresh, not yet captured startup identity built on
// resolve for one test, and restores the previous one afterwards.
func pinResolver(t *testing.T, resolve func() (string, error)) {
	t.Helper()
	prev := startup.Swap(newStartupIdentity(resolve))
	t.Cleanup(func() { startup.Store(prev) })
}

// pinStartup makes path the running binary: the identity is captured from
// it on first use, the same lazy-once path production takes.
func pinStartup(t *testing.T, path string) {
	t.Helper()
	pinResolver(t, func() (string, error) { return path, nil })
}

// pinStartupDigest pins an already captured identity whose installed file
// is path and whose recorded digest is digest, for tests that need the
// startup digest to differ from the file without writing two files.
func pinStartupDigest(t *testing.T, path, digest string) {
	t.Helper()
	id := newStartupIdentity(nil)
	id.once.Do(func() { id.installPath, id.path, id.digest = path, path, digest })
	prev := startup.Swap(id)
	t.Cleanup(func() { startup.Store(prev) })
}

func writeFileAt(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// replaceFileAt swaps new bytes in at path by rename, the way installers
// replace a binary, so the inode changes as well as the content.
func replaceFileAt(t *testing.T, path, contents string) {
	t.Helper()
	tmp := path + ".new"
	writeFileAt(t, tmp, contents)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename over %s: %v", path, err)
	}
}

func wantSkew(t *testing.T, m *UpgradeManager, want bool) {
	t.Helper()
	skew, err := m.CheckSkew()
	if err != nil {
		t.Fatalf("CheckSkew: %v", err)
	}
	if skew != want {
		t.Fatalf("CheckSkew = %v; want %v", skew, want)
	}
}

// swapInstallLink atomically publishes a symlink to target at install.
func swapInstallLink(t *testing.T, install, target string) {
	t.Helper()
	if err := os.Symlink(target, install+".next"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(install+".next", install); err != nil {
		t.Fatal(err)
	}
}

// TestRelaunchPathFallsBackToVerifiedTarget covers a final symlink change.
func TestRelaunchPathFallsBackToVerifiedTarget(t *testing.T) {
	bin := writeTempBinary(t, "verified bytes")
	target, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "installed")
	swapInstallLink(t, link, bin)
	if got := relaunchPath(link, target); got != link {
		t.Fatalf("stable link: got %q; want %q", got, link)
	}
	swapInstallLink(t, link, writeTempBinary(t, "other bytes"))
	if got := relaunchPath(link, target); got != target {
		t.Fatalf("changed link: got %q; want %q", got, target)
	}
}

// TestSkewFalseWhenBinaryUnchanged: an untouched startup file reports no
// skew, and StartupPath names the resolved file that was hashed.
func TestSkewFalseWhenBinaryUnchanged(t *testing.T) {
	bin := writeTempBinary(t, "release-1 bytes")
	pinStartup(t, bin)
	m := &UpgradeManager{}
	if got := BuildHash(); len(got) != 64 {
		t.Fatalf("BuildHash = %q; want a 64-hex sha256", got)
	}
	wantSkew(t, m, false)
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if m.StartupPath() != resolved {
		t.Fatalf("StartupPath = %q; want %q", m.StartupPath(), resolved)
	}
}

// TestSkewTrueWhenBinaryReplaced: new bytes at the same path report skew,
// which needs the startup digest captured before the replacement. Writing
// back identical bytes reports no skew.
func TestSkewTrueWhenBinaryReplaced(t *testing.T) {
	bin := writeTempBinary(t, "release-1 bytes")
	pinStartup(t, bin)
	m := &UpgradeManager{}
	before := BuildHash()

	replaceFileAt(t, bin, "release-2 bytes")
	wantSkew(t, m, true)
	if BuildHash() != before {
		t.Fatal("BuildHash changed after the file was replaced; the startup digest must be captured once")
	}

	replaceFileAt(t, bin, "release-1 bytes")
	wantSkew(t, m, false)
}

// TestSkewTrueWhenSymlinkedInstallSwapped: a daemon started through a
// symlink records the target's digest; re-pointing the symlink at another
// file reports skew, and the relaunch target is that new file.
func TestSkewTrueWhenSymlinkedInstallSwapped(t *testing.T) {
	dir := t.TempDir()
	v1, v2, link := filepath.Join(dir, "v1"), filepath.Join(dir, "v2"), filepath.Join(dir, "cascade")
	writeFileAt(t, v1, "release-1 bytes")
	writeFileAt(t, v2, "release-2 bytes")
	if err := os.Symlink(v1, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	pinStartup(t, link)
	m := &UpgradeManager{}
	wantSkew(t, m, false)

	if err := os.Symlink(v2, link+".new"); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Rename(link+".new", link); err != nil {
		t.Fatalf("swap symlink: %v", err)
	}
	skew, target, _, _, err := m.checkSkew()
	if err != nil || !skew {
		t.Fatalf("checkSkew after symlink swap = %v, %v; want skew", skew, err)
	}
	wantTarget, _ := filepath.EvalSymlinks(v2)
	if target != wantTarget {
		t.Fatalf("relaunch target = %q; want the swapped-in file %q", target, wantTarget)
	}
}

// TestCheckSkewErrorsWhenInstalledFileUnreadable: a missing or unreadable
// installed file is a typed KindUnavailable error, never a quiet no-skew.
func TestCheckSkewErrorsWhenInstalledFileUnreadable(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		bin := writeTempBinary(t, "release-1 bytes")
		pinStartup(t, bin)
		m := &UpgradeManager{}
		_ = BuildHash()
		if err := os.Remove(bin); err != nil {
			t.Fatalf("remove: %v", err)
		}
		wantUnavailable(t, m)
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file; the removed leg covers the read failure")
		}
		bin := writeTempBinary(t, "release-1 bytes")
		pinStartup(t, bin)
		m := &UpgradeManager{}
		_ = BuildHash()
		if err := os.Chmod(bin, 0); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(bin, 0o700) })
		wantUnavailable(t, m)
	})
}

func wantUnavailable(t *testing.T, m *UpgradeManager) {
	t.Helper()
	skew, err := m.CheckSkew()
	if err == nil {
		t.Fatalf("CheckSkew = %v, nil; want a KindUnavailable error", skew)
	}
	if skew {
		t.Fatal("CheckSkew returned skew=true alongside an error")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("CheckSkew error = %v; want KindUnavailable", err)
	}
}

// TestBuildHashDevOnResolverError: a failing executable resolver yields the
// dev sentinel, an empty StartupPath, and CheckSkew false with an error.
func TestBuildHashDevOnResolverError(t *testing.T) {
	pinResolver(t, func() (string, error) { return "", errors.New("executable unknown") })
	m := &UpgradeManager{}
	if got := BuildHash(); got != unstampedBuildHash {
		t.Fatalf("BuildHash = %q; want the dev sentinel", got)
	}
	if m.StartupPath() != "" {
		t.Fatalf("StartupPath = %q; want empty", m.StartupPath())
	}
	wantUnavailable(t, m)
}

// TestBuildHashDevWhenStartupFileUnreadable: a resolved path that cannot
// be hashed at start is the same sentinel, not a digest of nothing.
func TestBuildHashDevWhenStartupFileUnreadable(t *testing.T) {
	pinStartup(t, filepath.Join(t.TempDir(), "missing"))
	if got := BuildHash(); got != unstampedBuildHash {
		t.Fatalf("BuildHash = %q; want the dev sentinel", got)
	}
	wantUnavailable(t, &UpgradeManager{})
}
