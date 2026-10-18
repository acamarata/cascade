package topology

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// isolatedRoot points HOME, USERPROFILE and CASCADE_HOME at a fresh temp dir
// and returns it, refusing (via isolationViolation) a root that is the
// operator's real home, its default runtime root, or an ancestor of it.
func isolatedRoot(t *testing.T) string {
	t.Helper()
	realHome, _ := os.UserHomeDir()
	root := t.TempDir()
	if err := isolationViolation(root, realHome); err != nil {
		t.Fatalf("test root refused: %v", err)
	}
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("CASCADE_HOME", root)
	return root
}

// isolationViolation is the guard behind isolatedRoot.
func isolationViolation(root, realHome string) error {
	if realHome == "" {
		return nil
	}
	root, realHome = filepath.Clean(root), filepath.Clean(realHome)
	if root == realHome || root == filepath.Join(realHome, ".cascade") {
		return errors.New("root is the operator home or its runtime root: " + root)
	}
	if rel, err := filepath.Rel(root, realHome); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("root contains the operator home: " + root)
	}
	return nil
}

// TestIsolationGuardRefusesRealHome proves the guard is not vacuous: the
// real home, its default runtime root and an ancestor are refused; a
// separate temp root is accepted.
func TestIsolationGuardRefusesRealHome(t *testing.T) {
	for _, root := range []string{"/home/op", "/home/op/.cascade", "/home", "/"} {
		if err := isolationViolation(root, "/home/op"); err == nil {
			t.Errorf("root %q was accepted", root)
		}
	}
	if err := isolationViolation("/scratch/x/root", "/home/op"); err != nil {
		t.Errorf("a separate root was refused: %v", err)
	}
}

// requireSentinel asserts err's chain holds exactly sentinel (pointer
// identity, since errors.Is compares Kind only) and its text names want.
func requireSentinel(t *testing.T, err error, sentinel *cascade.Error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %q naming %q", sentinel.Msg, want)
	}
	found := false
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(sentinel) {
			found = true
		}
	}
	if !found {
		t.Errorf("err %q does not wrap sentinel %q by identity", err, sentinel.Msg)
	}
	if !strings.Contains(err.Error(), sentinel.Msg) || !strings.Contains(err.Error(), strings.Trim(strconv.Quote(want), `"`)) {
		t.Errorf("err message %q lacks %q and %q", err, sentinel.Msg, want)
	}
}

func TestHomePathValidIDs(t *testing.T) {
	root := isolatedRoot(t)
	ids := []string{"a", "acct-1", "work.main", "p_1", "0abc", "a.b-c_d", "console", "com10", "lpt", "aux-1", "nulx", "a.con", strings.Repeat("a", 64)}
	for _, id := range ids {
		got, err := HomePath(root, RuntimeProfileID(id))
		if err != nil {
			t.Errorf("HomePath(%q): %v", id, err)
			continue
		}
		if want := filepath.Join(root, "profiles", id); got != want {
			t.Errorf("HomePath(%q) = %q, want %q", id, got, want)
		}
	}
	assertEmptyDir(t, root)
}

func TestHomePathRejectsTraversal(t *testing.T) {
	root := isolatedRoot(t)
	bad := []string{"", "a/b", `a\b`, ".hidden", "..", "a..b", "a.", "Upper", "a b", "../x", "/abs", strings.Repeat("a", 65),
		"con", "prn", "aux", "nul", "com0", "com1", "com9", "lpt0", "lpt1", "lpt9", "con.txt", "nul.tar.gz", "com1.x"}
	for _, id := range bad {
		got, err := HomePath(root, RuntimeProfileID(id))
		requireSentinel(t, err, ErrTopologyInvariant, id)
		if got != "" {
			t.Errorf("HomePath(%q) returned path %q with its error", id, got)
		}
	}
	if _, err := HomePath("", "ok"); err == nil {
		t.Error("an empty root was accepted")
	}
	for _, rel := range []string{"relroot", "./root", "../root", "a/b"} {
		got, err := HomePath(rel, "ok")
		requireSentinel(t, err, ErrTopologyInvariant, "ok")
		if got != "" {
			t.Errorf("HomePath(%q, ok) returned %q for a relative root", rel, got)
		}
	}
	assertEmptyDir(t, root)
}

// assertEmptyDir fails when dir holds anything (HomePath must not touch disk).
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	var seen []string
	_ = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != dir {
			seen = append(seen, p)
		}
		return nil
	})
	if len(seen) != 0 {
		t.Errorf("%s is not empty: %v", dir, seen)
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("0700 modes are a unix contract; windows declares a refusal (profile_home_windows_test.go)")
	}
}

func modeOf(t *testing.T, p string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("lstat %s: %v", p, err)
	}
	return info.Mode().Perm()
}

func TestEnsureHomeModes(t *testing.T) {
	skipOnWindows(t)
	root := isolatedRoot(t)
	calls := 0
	prev := chmodHome
	chmodHome = func(f *os.File, m fs.FileMode) error {
		calls++
		if info, err := f.Stat(); err != nil || !info.IsDir() {
			t.Errorf("chmod seam got a non-directory descriptor %s: %v", f.Name(), err)
		}
		return prev(f, m)
	}
	t.Cleanup(func() { chmodHome = prev })

	path, err := EnsureHome(root, "acct-1")
	if err != nil || path != filepath.Join(root, "profiles", "acct-1") {
		t.Fatalf("EnsureHome = %q, %v", path, err)
	}
	if m := modeOf(t, path); m != 0o700 {
		t.Errorf("new home mode = %o, want 700", m)
	}
	if m := modeOf(t, filepath.Dir(path)); m != 0o700 {
		t.Errorf("profiles dir mode = %o, want 700", m)
	}

	loose := filepath.Join(root, "profiles", "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	before := calls
	got, err := EnsureHome(root, "loose")
	if err != nil || got != loose {
		t.Fatalf("EnsureHome(loose) = %q, %v", got, err)
	}
	if m := modeOf(t, loose); m != 0o700 {
		t.Errorf("tightened mode = %o, want 700", m)
	}
	if calls != before+1 {
		t.Errorf("chmod calls = %d, want one to tighten the 0755 directory", calls-before)
	}

	again, err := EnsureHome(root, "loose")
	if err != nil || again != got || calls != before+1 || modeOf(t, loose) != 0o700 {
		t.Errorf("second call = %q, %v, chmods %d: want the same path and no mode change", again, err, calls-before)
	}
	t.Run("refusals", ensureHomeRefusals)
}

// ensureHomeRefusals is the refusal half of TestEnsureHomeModes.
func ensureHomeRefusals(t *testing.T) {
	t.Helper()
	root := isolatedRoot(t)
	profiles := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profiles, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(profiles, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(profiles, "loose"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(profiles, "loose"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := chmodHome
	chmodHome = func(*os.File, fs.FileMode) error { return errors.New("injected chmod failure") }
	t.Cleanup(func() { chmodHome = prev })

	for _, id := range []string{"file", "link", "loose"} {
		path, err := EnsureHome(root, RuntimeProfileID(id))
		requireSentinel(t, err, ErrProfileHomePermission, id)
		if path != "" {
			t.Errorf("EnsureHome(%q) reported path %q with its error", id, path)
		}
	}
	if _, err := EnsureHome(root, "../x"); err == nil || errors.Unwrap(err) != error(ErrTopologyInvariant) {
		t.Errorf("a bad id must surface ErrTopologyInvariant, got %v", err)
	}
	for _, sub := range unixHomeSubtests {
		t.Run(sub.name, sub.fn)
	}
}

// unixHomeSubtests are the refusal subtests that need unix ownership and
// descriptor semantics; profile_home_unix_test.go registers them.
var unixHomeSubtests []struct {
	name string
	fn   func(*testing.T)
}
