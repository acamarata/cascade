package context

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Exercises Discover and its helpers against real filesystems and a real
// git binary (Art.2: no mocked git). Golden fixture tests live in
// discover_golden_test.go (Art.10.3's 300-line file cap forced the split;
// see this ticket's journal for the contradiction this created against the
// contract's single-file files_scope entry).

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return root
}

func fixedHome(dir string) HomeDirFunc {
	return func() (string, error) { return dir, nil }
}

// runGit runs a git subcommand in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=cascade-test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=cascade-test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestTierRole(t *testing.T) {
	cases := []struct {
		role TierRole
		want string
	}{
		{TierGCI, "GCI"}, {TierASI, "ASI"}, {TierPPI, "PPI"},
		{TierPRI, "PRI"}, {TierPAI, "PAI"}, {TierRole(0), "invalid-tier"}, {TierRole(99), "invalid-tier"},
	}
	for _, c := range cases {
		if got := c.role.String(); got != c.want {
			t.Errorf("%d.String() = %q, want %q", c.role, got, c.want)
		}
	}
	if TierRole(0).Valid() || TierRole(99).Valid() {
		t.Error("zero/out-of-range TierRole must be invalid")
	}
	got := allTierRoles()
	want := []TierRole{TierGCI, TierASI, TierPPI, TierPRI, TierPAI}
	if len(got) != len(want) {
		t.Fatalf("allTierRoles() has %d members, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allTierRoles()[%d] = %v, want %v", i, got[i], want[i])
		}
		if !got[i].Valid() {
			t.Errorf("allTierRoles()[%d] = %v reports invalid", i, got[i])
		}
	}
}

func TestDiscoverGitRoot(t *testing.T) {
	t.Run("inside a repo", func(t *testing.T) {
		root := resolvedTempDir(t)
		repo := filepath.Join(root, "repo", "nested")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, filepath.Join(root, "repo"), "init", "-q")
		if got, want := gitRoot(context.Background(), repo), filepath.Join(root, "repo"); got != want {
			t.Errorf("gitRoot() = %q, want %q", got, want)
		}
	})
	t.Run("not inside a repo falls back to cwd", func(t *testing.T) {
		dir := resolvedTempDir(t)
		if got := gitRoot(context.Background(), dir); got != dir {
			t.Errorf("gitRoot() = %q, want fallback %q", got, dir)
		}
	})
}

func TestDiscoverGitBinaryAbsent(t *testing.T) {
	empty := resolvedTempDir(t)
	t.Setenv("PATH", empty)
	if _, err := exec.LookPath("git"); err == nil {
		t.Skip("git still resolvable with an empty PATH on this platform; cannot exercise the absent-binary path")
	}
	dir := resolvedTempDir(t)
	if got := gitRoot(context.Background(), dir); got != dir {
		t.Errorf("gitRoot() with git absent = %q, want fallback %q", got, dir)
	}
}

func TestDiscoverEmptyCwd(t *testing.T) {
	dir := resolvedTempDir(t)
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	records, err := Discover(context.Background(), "", fixedHome(dir))
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if pri := records[TierPRI-1]; pri.Dir != dir {
		t.Errorf("PRI.Dir = %q, want the chdir'd cwd %q", pri.Dir, dir)
	}
}

func TestDiscoverAbsentTierFile(t *testing.T) {
	dir := resolvedTempDir(t)
	records, err := Discover(context.Background(), dir, fixedHome(dir))
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if pri := records[TierPRI-1]; !pri.Absent || pri.Content != "" {
		t.Errorf("PRI = %+v, want Absent=true Content=\"\"", pri)
	}
}

func TestDiscoverHomeBoundary(t *testing.T) {
	root := resolvedTempDir(t)
	anchor := filepath.Join(root, "repo")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := tierDirs(anchor, anchor, fixedHome(root))
	byRole := make(map[TierRole]tierDir, len(dirs))
	for _, d := range dirs {
		byRole[d.role] = d
	}
	if byRole[TierPPI].dir != "" {
		t.Errorf("PPI (== HOME) must be absent, got dir=%q", byRole[TierPPI].dir)
	}
	if byRole[TierASI].dir != "" {
		t.Errorf("ASI (overshoots past HOME) must be absent, got dir=%q", byRole[TierASI].dir)
	}
	if byRole[TierGCI].dir != root {
		t.Errorf("GCI must still be HOME, got dir=%q", byRole[TierGCI].dir)
	}
}

func TestLoadTierSymlinkNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on default Windows CI runners")
	}
	root := resolvedTempDir(t)
	decoy := filepath.Join(root, "decoy.md")
	if err := os.WriteFile(decoy, []byte("should never be read"), 0o644); err != nil {
		t.Fatal(err)
	}
	td := filepath.Join(root, "repo", tierDirName)
	if err := os.MkdirAll(td, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, filepath.Join(td, tierFileName)); err != nil {
		t.Fatal(err)
	}
	rec, err := loadTier(TierPRI, filepath.Join(root, "repo"), 3)
	if err != nil {
		t.Fatalf("loadTier() error = %v", err)
	}
	if !rec.Absent || rec.Content != "" {
		t.Errorf("symlinked tier file: got %+v, want Absent=true (never followed)", rec)
	}
}

func TestLoadTierFileIsDirectory(t *testing.T) {
	root := resolvedTempDir(t)
	tierPath := filepath.Join(root, "repo", tierDirName, tierFileName)
	if err := os.MkdirAll(tierPath, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := loadTier(TierPRI, filepath.Join(root, "repo"), 3)
	if err != nil {
		t.Fatalf("loadTier() error = %v", err)
	}
	if !rec.Absent {
		t.Error("tier file that is a directory: got Absent=false, want true")
	}
}

func TestLoadTierPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not model this on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	root := resolvedTempDir(t)
	td := filepath.Join(root, "repo", tierDirName)
	if err := os.MkdirAll(td, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(td, tierFileName)
	if err := os.WriteFile(file, []byte("secret"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	_, err := loadTier(TierPRI, filepath.Join(root, "repo"), 3)
	if err == nil {
		t.Fatal("loadTier() with an unreadable file: want an error, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || (kind != cascade.KindPermissionDenied && kind != cascade.KindUnavailable) {
		t.Errorf("loadTier() error kind = %v ok=%v, want KindPermissionDenied or KindUnavailable", kind, ok)
	}
}

func TestPathHelpers(t *testing.T) {
	sep := string(filepath.Separator)
	if got := parentDir(""); got != "" {
		t.Errorf("parentDir(\"\") = %q, want \"\"", got)
	}
	if got := parentDir(sep); got != "" {
		t.Errorf("parentDir(root) = %q, want \"\" (root has no parent)", got)
	}
	if got, want := parentDir(filepath.Join(sep, "a", "b")), filepath.Join(sep, "a"); got != want {
		t.Errorf("parentDir() = %q, want %q", got, want)
	}

	a := filepath.Join(sep, "a")
	ab := filepath.Join(a, "b")
	ancestorCases := []struct {
		ancestor, descendant string
		want                 bool
	}{{"", ab, false}, {a, "", false}, {a, a, false}, {a, ab, true}, {ab, a, false}}
	for _, c := range ancestorCases {
		if got := isProperAncestor(c.ancestor, c.descendant); got != c.want {
			t.Errorf("isProperAncestor(%q,%q) = %v, want %v", c.ancestor, c.descendant, got, c.want)
		}
	}

	home := filepath.Join(sep, "home")
	boundaryCases := []struct{ dir, home, want string }{
		{"", home, ""}, {"/x", "", "/x"}, {home, home, ""},
		{sep, home, ""}, {"/elsewhere", home, "/elsewhere"},
	}
	for _, c := range boundaryCases {
		if got := boundaryFilter(c.dir, c.home); got != c.want {
			t.Errorf("boundaryFilter(%q,%q) = %q, want %q", c.dir, c.home, got, c.want)
		}
	}
}

func TestResolveHomeAndWrapErr(t *testing.T) {
	if got := resolveHome(func() (string, error) { return "", fmt.Errorf("boom") }); got != "" {
		t.Errorf("resolveHome(erroring) = %q, want \"\"", got)
	}
	if got := resolveHome(func() (string, error) { return "", nil }); got != "" {
		t.Errorf("resolveHome(empty) = %q, want \"\"", got)
	}
	dir := resolvedTempDir(t)
	if got := resolveHome(fixedHome(dir)); got != dir {
		t.Errorf("resolveHome() = %q, want %q", got, dir)
	}

	permErr := &os.PathError{Op: "open", Path: "x", Err: os.ErrPermission}
	if kind, _ := cascade.KindOf(wrapTierFSErr(permErr, "boom")); kind != cascade.KindPermissionDenied {
		t.Errorf("wrapTierFSErr(permission) kind = %v, want KindPermissionDenied", kind)
	}
	if kind, _ := cascade.KindOf(wrapTierFSErr(fmt.Errorf("disk fell off"), "boom")); kind != cascade.KindUnavailable {
		t.Errorf("wrapTierFSErr(other) kind = %v, want KindUnavailable", kind)
	}
}
