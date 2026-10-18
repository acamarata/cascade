//go:build !windows

// Purpose: tests for where the helper may live and run: directories other
//
//	users can change (the helper's own and every ancestor), the temp
//	directory that hosts the reply listener, and the empty environment the
//	helper is started with.
//
// SPORT: internal/elevation helper path tests/ADDED (P1-SHIP-15).
package elevation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// resolvedTempDir returns t.TempDir with symlinks resolved (macOS keeps
// /var/folders behind /private/var), as the production chain check sees it.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// launches counts launcher calls per helper directory. Counting at the
// launcher (not in the fake helper) also sees an exec that dies before the
// fake writes anything.
var (
	launchMu sync.Mutex
	launches = map[string]int{}
)

// execCount is the larger of the launcher count and the fake helper's own
// "execs" sidecar, which also sees launches through helperSource.
func execCount(dir string) int {
	launchMu.Lock()
	n := launches[dir]
	launchMu.Unlock()
	if b, err := os.ReadFile(filepath.Join(dir, "execs")); err == nil && len(b) > n {
		n = len(b)
	}
	return n
}

// countingRun is platformHelperRun with a per-directory launch counter.
func countingRun(ctx context.Context, path string, req []byte, timeout time.Duration, vet func(pid int) error) ([]byte, error) {
	launchMu.Lock()
	launches[filepath.Dir(path)]++
	launchMu.Unlock()
	return platformHelperRun(ctx, path, req, timeout, vet)
}

func TestHelperKeystoreRefusesWorldWritableDirectory(t *testing.T) {
	dir, exe := install(t, true)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	ks := newKS(exe, OKVerifier{})
	if ks.IsAvailable() || execCount(dir) != 0 {
		t.Fatalf("a helper in a world-writable directory was used (execs %d)", execCount(dir))
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !ks.IsAvailable() {
		t.Fatal("control: the same helper in a 0755 directory was refused")
	}
}

func TestHelperKeystoreStartsTheHelperWithAnEmptyEnvironment(t *testing.T) {
	dir, exe := install(t, true) // built first: the variables below would break `go build`
	t.Setenv("CASCADE_TEST_ENV_LEAK", "must-not-reach-the-helper")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/nonexistent/inject.dylib")
	if !newKS(exe, OKVerifier{}).IsAvailable() {
		t.Fatal("control: the helper was not used")
	}
	b, err := os.ReadFile(filepath.Join(dir, "envcount"))
	if err != nil || string(b) != "0" {
		t.Fatalf("the helper started with %q environment entries (err %v), want 0", b, err)
	}
}

func TestHelperKeystoreRefusesAnUnsafeTempDirectory(t *testing.T) {
	_, exe := install(t, true)
	ks := newKS(exe, OKVerifier{})
	good, _ := os.MkdirTemp("", "ce") // before TMPDIR changes
	t.Cleanup(func() { _ = os.RemoveAll(good) })
	open, err := os.MkdirTemp("", "ce")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(open) })
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", open)
	if ks.IsAvailable() {
		t.Fatal("a world-writable, non-sticky temp directory hosted the listener")
	}
	long := filepath.Join(open, strings.Repeat("d", 120))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	if ks.IsAvailable() {
		t.Fatal("a temp directory too long for a unix socket path was accepted")
	}
	t.Setenv("TMPDIR", good)
	if !ks.IsAvailable() {
		t.Fatal("control: a private temp directory was refused")
	}
}

func TestHelperKeystoreRefusesDirectoriesOtherUsersCanChange(t *testing.T) {
	for name, build := range map[string]func(t *testing.T, root string) string{
		"group-writable helper directory": func(t *testing.T, root string) string {
			d := filepath.Join(root, "d")
			mkdirMode(t, d, 0o775)
			return d
		},
		"world-writable ancestor": func(t *testing.T, root string) string {
			mkdirMode(t, filepath.Join(root, "up"), 0o777)
			d := filepath.Join(root, "up", "d")
			mkdirMode(t, d, 0o755)
			return d
		},
	} {
		t.Run(name, func(t *testing.T) {
			src, _ := install(t, true)
			root := resolvedTempDir(t)
			dir := build(t, root)
			exe := filepath.Join(dir, "cascade")
			writeFileForTest(t, exe)
			copyMode(t, filepath.Join(src, HelperName), filepath.Join(dir, HelperName), 0o755)
			ks := newKS(exe, OKVerifier{})
			if ks.IsAvailable() || execCount(dir) != 0 {
				t.Fatalf("a helper in a directory others can change was used (execs %d)", execCount(dir))
			}
			mkdirMode(t, dir, 0o755)
			if err := os.Chmod(filepath.Dir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if !ks.IsAvailable() {
				t.Fatal("control: the same helper in a 0755 chain was refused")
			}
		})
	}
}

func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeACLListings(t *testing.T) {
	const mode = "drwxr-xr-x+ 5 u staff 160 Oct  1 00:00 /x\n"
	for name, c := range map[string]struct {
		listing string
		unsafe  bool
	}{
		"no ACL":                      {"drwxr-xr-x 5 u staff 160 Oct  1 00:00 /x\n", false},
		"deny delete (macOS home)":    {mode + " 0: group:everyone deny delete\n", false},
		"read-only allow":             {mode + " 0: user:other allow list,search,readattr\n", false},
		"allow write":                 {mode + " 0: user:other allow write\n", true},
		"allow add_file,delete_child": {mode + " 0: group:staff allow list,add_file,search,delete_child\n", true},
		"allow after a deny":          {mode + " 0: group:everyone deny delete\n 1: user:other allow append\n", true},
		"allow writesecurity":         {mode + " 0: user:other allow writesecurity\n", true},
		"unparsable allow entry":      {mode + " 0: allow\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := unsafeACL(c.listing); (err != nil) != c.unsafe {
				t.Fatalf("unsafeACL = %v, want unsafe=%v", err, c.unsafe)
			}
		})
	}
	want := []string{"write", "add_file", "append", "add_subdirectory", "delete", "delete_child", "writeattr", "writeextattr", "writesecurity", "chown"}
	if len(aclWriteRights) != len(want) {
		t.Fatalf("aclWriteRights has %d rights, the table lists %d", len(aclWriteRights), len(want))
	}
	for _, r := range want {
		if unsafeACL(mode+" 0: user:other allow "+r+"\n") == nil {
			t.Errorf("an ACL entry allowing %s was not refused", r)
		}
	}
}

func TestHelperLocationReadsACLsOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("extended ACLs are read on darwin only")
	}
	dir, exe := install(t, true)
	old := lsACL
	t.Cleanup(func() { lsACL = old })
	const unsafe, safe = " 0: user:other allow add_file,delete_child\n", " 0: group:everyone deny delete\n"
	ks := newKS(exe, OKVerifier{})
	// The listing is unsafe for one path only, so each check is tested alone.
	for name, isUnsafe := range map[string]func(p string) bool{
		"the helper file": func(p string) bool { return filepath.Base(p) == HelperName },
		"its directory":   func(p string) bool { return p == filepath.Dir(exe) || p == dir },
	} {
		lsACL = func(p string) ([]byte, error) {
			if isUnsafe(p) {
				return []byte(unsafe), nil
			}
			return []byte(safe), nil
		}
		if ks.IsAvailable() || execCount(dir) != 0 {
			t.Fatalf("a helper whose %s grants an ACL write entry was used (execs %d)", name, execCount(dir))
		}
	}
	lsACL = func(string) ([]byte, error) { return nil, errors.New("ls failed") }
	if ks.IsAvailable() {
		t.Fatal("an unreadable ACL listing was trusted")
	}
	lsACL = func(string) ([]byte, error) { return []byte(safe), nil }
	if !ks.IsAvailable() {
		t.Fatal("control: a deny-only ACL was refused")
	}
}

func TestHelperLocationRefusesUntrustedOwnersAndVolumes(t *testing.T) {
	dir, exe := install(t, true)
	oldOwner, oldVolume := platformFileOwnerTrusted, platformVolumeTrusted // oldVolume: install()'s "always trusted" stand-in
	t.Cleanup(func() { platformFileOwnerTrusted, platformVolumeTrusted = oldOwner, oldVolume })
	ks := newKS(exe, OKVerifier{})
	// Files are ours, directories are not: only the directory rule can refuse.
	platformFileOwnerTrusted = func(fi os.FileInfo) bool { return !fi.IsDir() }
	if ks.IsAvailable() || execCount(dir) != 0 {
		t.Fatalf("a helper in a directory owned by another user was used (execs %d)", execCount(dir))
	}
	platformFileOwnerTrusted = oldOwner
	platformVolumeTrusted = func(path string) bool { return path != dir }
	if ks.IsAvailable() || execCount(dir) != 0 {
		t.Fatalf("a helper on a volume that ignores ownership was used (execs %d)", execCount(dir))
	}
	platformVolumeTrusted = oldVolume
	if !ks.IsAvailable() {
		t.Fatal("control: the same helper with real ownership was refused")
	}
	if runtime.GOOS == "darwin" {
		if !volumeHonoursOwnership("/") {
			t.Fatal("the boot volume reports ignored ownership")
		}
		if volumeHonoursOwnership(filepath.Join(dir, "missing")) {
			t.Fatal("an unreadable volume was trusted")
		}
	}
}

func TestHelperLocationRefusesPathsWithControlCharacters(t *testing.T) {
	src, _ := install(t, true)
	dir := filepath.Join(resolvedTempDir(t), "a\nCodeDirectory v=1 flags=0x10000(runtime)\nb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skipf("this filesystem refuses a newline in a name: %v", err)
	}
	exe := filepath.Join(dir, "cascade")
	writeFileForTest(t, exe)
	copyMode(t, filepath.Join(src, HelperName), filepath.Join(dir, HelperName), 0o755)
	if newKS(exe, OKVerifier{}).IsAvailable() || execCount(dir) != 0 {
		t.Fatal("a helper whose path carries a newline was used")
	}
}
