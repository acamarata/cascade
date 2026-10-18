//go:build unix

package topology

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func init() {
	unixHomeSubtests = append(unixHomeSubtests,
		struct {
			name string
			fn   func(*testing.T)
		}{"foreign_owner", ensureHomeForeignOwner},
		struct {
			name string
			fn   func(*testing.T)
		}{"profiles_dir", ensureHomeProfilesDir})
}

// ensureHomeForeignOwner: a home (and a profiles/) the effective user does
// not own is refused, even when its mode is already 0700.
func ensureHomeForeignOwner(t *testing.T) {
	t.Helper()
	root := isolatedRoot(t)
	if _, err := EnsureHome(root, "mine"); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	prev := effectiveUID
	t.Cleanup(func() { effectiveUID = prev })
	// profiles/ is checked first (the user owns it), then the home (foreign).
	calls := 0
	effectiveUID = func() int {
		calls++
		return os.Geteuid() + calls - 1
	}
	path, err := EnsureHome(root, "mine")
	requireSentinel(t, err, ErrProfileHomePermission, filepath.Join("profiles", "mine"))
	if path != "" || calls != 2 {
		t.Errorf("foreign home: path = %q after %d owner checks, want none after 2", path, calls)
	}
	effectiveUID = func() int { return os.Geteuid() + 1 }
	path, err = EnsureHome(root, "mine")
	requireSentinel(t, err, ErrProfileHomePermission, "profiles")
	if path != "" {
		t.Errorf("foreign profiles/: path = %q", path)
	}
}

// realChmod is the production chmod seam, restored inside subtests that run
// under a failing one.
func realChmod(f *os.File, m fs.FileMode) error { return f.Chmod(m) }

// ensureHomeProfilesDir: profiles/ must be a real directory the user owns;
// a loose one is tightened to 0700, a link, a file or an unfixable one is
// refused and no home is created.
func ensureHomeProfilesDir(t *testing.T) {
	t.Helper()
	loose := isolatedRoot(t)
	prevChmod := chmodHome
	chmodHome = realChmod
	t.Cleanup(func() { chmodHome = prevChmod })
	profiles := filepath.Join(loose, "profiles")
	if err := os.Mkdir(profiles, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profiles, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureHome(loose, "a"); err != nil || modeOf(t, profiles) != 0o700 {
		t.Fatalf("loose profiles/: err %v, mode %o, want nil and 700", err, modeOf(t, profiles))
	}
	for _, kind := range []string{"link", "file", "unfixable"} {
		root := isolatedRoot(t)
		profiles := filepath.Join(root, "profiles")
		switch kind {
		case "link":
			if err := os.Symlink(t.TempDir(), profiles); err != nil {
				t.Fatal(err)
			}
		case "file":
			if err := os.WriteFile(profiles, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.Mkdir(profiles, 0o777); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(profiles, 0o777); err != nil {
				t.Fatal(err)
			}
			chmodHome = func(*os.File, fs.FileMode) error { return errors.New("injected chmod failure") }
		}
		path, err := EnsureHome(root, "a")
		requireSentinel(t, err, ErrProfileHomePermission, "profiles")
		if path != "" {
			t.Errorf("%s: path = %q with its error", kind, path)
		}
		if _, statErr := os.Lstat(filepath.Join(profiles, "a")); kind != "link" && statErr == nil {
			t.Errorf("%s: a home was created under a refused profiles/", kind)
		}
		chmodHome = realChmod
	}
}

// TestEnsureHomeClearsSpecialBits: a home at 0700 plus a setuid, setgid or
// sticky bit is not "already 0700"; EnsureHome chmods it, which clears the
// extra bit. A chmod that leaves the bit behind is caught by the post-chmod
// verification, so the home is refused rather than reported private.
func TestEnsureHomeClearsSpecialBits(t *testing.T) {
	for _, bit := range []struct {
		name string
		mode fs.FileMode
	}{{"setuid", fs.ModeSetuid}, {"setgid", fs.ModeSetgid}, {"sticky", fs.ModeSticky}} {
		t.Run(bit.name, func(t *testing.T) {
			root := isolatedRoot(t)
			home, err := EnsureHome(root, "special")
			if err != nil {
				t.Fatalf("EnsureHome: %v", err)
			}
			if err := os.Chmod(home, 0o700|bit.mode); err != nil {
				t.Skipf("filesystem refuses the %s bit: %v", bit.name, err)
			}
			if info, err := os.Lstat(home); err != nil || info.Mode()&bit.mode == 0 {
				t.Skipf("filesystem dropped the %s bit (err %v)", bit.name, err)
			}
			prev := chmodHome
			t.Cleanup(func() { chmodHome = prev })
			chmodHome = func(*os.File, fs.FileMode) error { return nil }
			path, err := EnsureHome(root, "special")
			if !errors.Is(err, ErrProfileHomePermission) || path != "" {
				t.Errorf("a chmod that keeps the %s bit: path %q, err %v; want ErrProfileHomePermission", bit.name, path, err)
			}
			chmodHome = prev
			if _, err := EnsureHome(root, "special"); err != nil {
				t.Fatalf("EnsureHome with the %s bit: %v", bit.name, err)
			}
			info, err := os.Lstat(home)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); got != 0o700 {
				t.Errorf("mode after EnsureHome = %v, want only 0700", info.Mode())
			}
		})
	}
}
