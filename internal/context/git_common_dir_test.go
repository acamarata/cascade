package context

// Builds real repositories with the real git binary under t.TempDir()
// (Art.2) and checks every scope.GitDirs field GitCommonDir reports.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// isolateGit pins git to no global/system config and stops discovery above
// base's parent (runGit, discover_test.go, is reused for setup).
func isolateGit(t *testing.T, base string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(base))
}

// newRealRepo inits a repository with one commit at base/name and returns
// its resolved path.
func newRealRepo(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// TestGitCommonDirRealRepo checks every GitDirs field for a main checkout,
// a path inside .git (no work tree, Toplevel ""), a linked worktree,
// a bare repository, a submodule (raw relative
// core.worktree) and a `--separate-git-dir` repository whose core.worktree
// the test sets, plus KindInvalidInput for a non-repository.
func TestGitCommonDirRealRepo(t *testing.T) {
	skipWithoutGit(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	isolateGit(t, base)

	root := newRealRepo(t, base, "main")
	wt := filepath.Join(base, "wt")
	runGit(t, root, "worktree", "add", "-q", wt, "-b", "wtbranch")
	bare := filepath.Join(base, "bare.git")
	runGit(t, bare, "init", "-q", "--bare")
	src := newRealRepo(t, base, "src")
	runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, "subA")
	sep := filepath.Join(base, "ssep")
	sepMain, sepWt := newSepDirRepo(t, base, "s", true)
	projGit, projWt := filepath.Join(base, "proj", ".git"), filepath.Join(base, "proj", "main")
	runGit(t, base, "clone", "-q", "--bare", src, projGit)
	runGit(t, projGit, "worktree", "add", "-q", projWt, "-b", "projbranch")

	dotGit, modA := filepath.Join(root, ".git"), filepath.Join(root, ".git", "modules", "subA")
	cases := []struct {
		name, dir string
		want      scope.GitDirs
	}{
		{"main checkout", root, scope.GitDirs{CommonDir: dotGit, GitDir: dotGit, Toplevel: root}},
		{"inside .git", dotGit, scope.GitDirs{CommonDir: dotGit, GitDir: dotGit}},
		{"linked worktree", wt, scope.GitDirs{CommonDir: dotGit, GitDir: filepath.Join(dotGit, "worktrees", "wt"), Toplevel: wt}},
		{"bare", bare, scope.GitDirs{CommonDir: bare, GitDir: bare, Bare: true}},
		{"submodule", filepath.Join(root, "subA"), scope.GitDirs{CommonDir: modA, GitDir: modA,
			CoreWorktree: "../../../subA", Toplevel: filepath.Join(root, "subA")}},
		{"separate git dir", sepMain, scope.GitDirs{CommonDir: sep, GitDir: sep, CoreWorktree: sepMain, Toplevel: sepMain}},
		{"separate git dir worktree", sepWt, scope.GitDirs{CommonDir: sep, GitDir: filepath.Join(sep, "worktrees", "swt"),
			CoreWorktree: sepMain, Toplevel: sepWt}},
		{"bare clone worktree", projWt, scope.GitDirs{CommonDir: projGit,
			GitDir: filepath.Join(projGit, "worktrees", "main"), Bare: true}},
	}
	for _, c := range cases {
		got, err := GitCommonDir(context.Background(), c.dir)
		if err != nil || got != c.want {
			t.Errorf("GitCommonDir(%s) = %+v, %v\n want %+v", c.name, got, err, c.want)
		}
	}

	_, err = GitCommonDir(context.Background(), t.TempDir())
	if !hasKind(err, cascade.KindInvalidInput, "is not a git repository") {
		t.Errorf("GitCommonDir(non-repository) err = %v, want KindInvalidInput \"is not a git repository\"", err)
	}
}

// hasKind reports whether err is a cascade error of kind whose message
// contains every substr (identity plus message, never errors.Is alone).
func hasKind(err error, kind cascade.Kind, substrs ...string) bool {
	k, ok := cascade.KindOf(err)
	if err == nil || !ok || k != kind {
		return false
	}
	for _, sub := range substrs {
		if !strings.Contains(err.Error(), sub) {
			return false
		}
	}
	return true
}

// TestGitCommonDirIgnoresGitDirEnv proves no `git rev-parse
// --local-env-vars` variable in the caller's environment (GIT_DIR plus
// GIT_WORK_TREE, or GIT_COMMON_DIR) replaces dir's own repository.
func TestGitCommonDirIgnoresGitDirEnv(t *testing.T) {
	skipWithoutGit(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	isolateGit(t, base)
	repoA, repoB := newRealRepo(t, base, "a"), newRealRepo(t, base, "b")
	for name, env := range map[string][]string{
		"GIT_DIR+GIT_WORK_TREE": {"GIT_DIR", filepath.Join(repoB, ".git"), "GIT_WORK_TREE", repoB},
		"GIT_COMMON_DIR":        {"GIT_COMMON_DIR", filepath.Join(repoB, ".git")},
	} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < len(env); i += 2 {
				t.Setenv(env[i], env[i+1])
			}
			got, err := GitCommonDir(context.Background(), repoA)
			if want := filepath.Join(repoA, ".git"); err != nil || got.CommonDir != want || got.Toplevel != repoA {
				t.Errorf("GitCommonDir(a) = %+v, %v; want CommonDir %q, Toplevel %q", got, err, want, repoA)
			}
			if root, err := scope.CanonicalRepoRoot(context.Background(), repoA, GitCommonDir); err != nil || root != repoA {
				t.Errorf("CanonicalRepoRoot(a) = %q, %v; want %q", root, err, repoA)
			}
		})
	}
}

// TestGitCommonDirBounded proves a hung git is cut off by gitExecTimeout
// (5s by contract; shortened here) and reported as KindUnavailable.
func TestGitCommonDirBounded(t *testing.T) {
	fakeHungGit(t, hungGitScript)
	start := time.Now()
	_, err := GitCommonDir(context.Background(), t.TempDir())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("GitCommonDir with a hung git took %v, want it bounded by %v", elapsed, gitExecTimeout)
	}
	if !hasKind(err, cascade.KindUnavailable, "git rev-parse failed", "signal: killed") {
		t.Errorf("GitCommonDir with a hung git err = %v, want KindUnavailable rev-parse killed", err)
	}
}

// TestGitCommonDirGitMissing proves a git that cannot be found at all is
// KindUnavailable, never KindInvalidInput.
func TestGitCommonDirGitMissing(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := GitCommonDir(context.Background(), t.TempDir())
	if !hasKind(err, cascade.KindUnavailable, "run git in", "executable file not found") {
		t.Errorf("GitCommonDir with no git on PATH err = %v, want KindUnavailable run git", err)
	}
}

// newSepDirRepo makes `git init --separate-git-dir` at base/<name>main with
// a `git worktree add` worktree, setting core.worktree to the main worktree
// when coreWorktree is true (git never sets it for this layout).
func newSepDirRepo(t *testing.T, base, name string, coreWorktree bool) (mainDir, wt string) {
	t.Helper()
	sep := filepath.Join(base, name+"sep")
	mainDir, wt = filepath.Join(base, name+"main"), filepath.Join(base, name+"wt")
	runGit(t, base, "init", "-q", "--separate-git-dir", sep, mainDir)
	runGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, mainDir, "worktree", "add", "-q", wt, "-b", name+"branch")
	if coreWorktree {
		runGit(t, base, "--git-dir="+sep, "config", "core.worktree", mainDir)
	}
	return mainDir, wt
}

// TestCanonicalRepoRootProductionLayouts runs scope.CanonicalRepoRoot with
// the production GitCommonDir (not the scope tests' copy of it) over each
// layout the resolution order distinguishes: submodules, a separate git
// dir with and without core.worktree, a bare repository, a bare clone with
// a worktree (with core.bare set and unset), a bare repository with
// core.bare unset, and a `.git` repository whose core.worktree names
// elsewhere. Only an explicit core.bare=true makes a common dir bare.
func TestCanonicalRepoRootProductionLayouts(t *testing.T) {
	skipWithoutGit(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	isolateGit(t, base)
	super := newRealRepo(t, base, "super")
	for _, n := range []string{"subA", "subB"} {
		runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", newRealRepo(t, base, "src"+n), n)
	}
	sepMain, sepWt := newSepDirRepo(t, base, "s", true)
	ambMain, ambWt := newSepDirRepo(t, base, "amb", false)
	bare := filepath.Join(base, "bare.git")
	runGit(t, bare, "init", "-q", "--bare")
	projGit, projWt := filepath.Join(base, "proj", ".git"), filepath.Join(base, "proj", "main")
	runGit(t, base, "clone", "-q", "--bare", super, projGit)
	runGit(t, projGit, "worktree", "add", "-q", projWt, "-b", "projbranch")
	uGit, uWt := filepath.Join(base, "u", ".git"), filepath.Join(base, "u", "main")
	runGit(t, base, "clone", "-q", "--bare", super, uGit)
	runGit(t, uGit, "worktree", "add", "-q", uWt, "-b", "ubranch")
	runGit(t, base, "--git-dir="+uGit, "config", "--unset", "core.bare")
	unsetBare := filepath.Join(base, "x.git")
	runGit(t, unsetBare, "init", "-q", "--bare")
	runGit(t, base, "--git-dir="+unsetBare, "config", "--unset", "core.bare")
	plain := newRealRepo(t, base, "plain")
	runGit(t, plain, "config", "core.worktree", sepMain)

	subA, subB := filepath.Join(super, "subA"), filepath.Join(super, "subB")
	cases := []struct{ dir, want, refusal string }{
		{subA, subA, ""}, {subB, subB, ""},
		{sepMain, sepMain, ""}, {sepWt, sepMain, ""},
		{ambMain, ambMain, ""}, {ambWt, "", "repo_root_ambiguous"},
		{bare, bare, ""}, {projGit, projGit, ""}, {projWt, projGit, ""},
		{uGit, filepath.Dir(uGit), ""}, {uWt, filepath.Dir(uGit), ""},
		{unsetBare, "", "repo_root_ambiguous"},
		{plain, plain, ""},
	}
	for _, c := range cases {
		got, err := scope.CanonicalRepoRoot(context.Background(), c.dir, GitCommonDir)
		switch {
		case c.refusal != "" && (got != "" || !hasKind(err, cascade.KindInvalidInput, c.refusal)):
			t.Errorf("CanonicalRepoRoot(%s) = %q, %v; want KindInvalidInput %s", c.dir, got, err, c.refusal)
		case c.refusal == "" && (err != nil || got != c.want):
			t.Errorf("CanonicalRepoRoot(%s) = %q, %v; want %q", c.dir, got, err, c.want)
		}
	}
}

// TestGitCommonDirCoreBareReadErrorRefuses proves a core.bare read that
// fails for any reason but "unset" refuses (KindUnavailable), never reads
// as "not bare".
func TestGitCommonDirCoreBareReadErrorRefuses(t *testing.T) {
	installFakeGit(t, "#!/bin/sh\ncase \"$1\" in\nrev-parse) printf '/r/.git\\n/r/.git\\n' ;;\n"+
		"*) echo 'fatal: bad config line' >&2; exit 3 ;;\nesac\n")
	_, err := GitCommonDir(context.Background(), t.TempDir())
	if !hasKind(err, cascade.KindUnavailable, "git config core.bare") {
		t.Errorf("GitCommonDir with an unreadable core.bare err = %v, want KindUnavailable git config core.bare", err)
	}
}

// TestGitCommonDirFakeGitFailures proves each later git step of
// GitCommonDir refuses with its own KindUnavailable message: a rev-parse
// that prints the wrong number of lines, a failing --show-toplevel and a
// failing core.worktree read (core.bare reads as unset, exit 1, in the
// last two).
func TestGitCommonDirFakeGitFailures(t *testing.T) {
	const head = "#!/bin/sh\ncase \"$*\" in\n*--git-common-dir*) printf '/r/.git\\n/r/.git\\n' ;;\n" +
		"*core.bare*) exit 1 ;;\n"
	cases := []struct {
		name, script string
		substrs      []string
	}{
		{"short output", "#!/bin/sh\nprintf '/r/.git\\n'\n",
			[]string{"unexpected git rev-parse output", `"/r/.git\n"`}},
		{"toplevel fails", head + "*--show-toplevel*) echo 'fatal: boom' >&2; exit 4 ;;\nesac\n",
			[]string{"git rev-parse --show-toplevel", "exit status 4"}},
		{"core.worktree fails", head + "*--show-toplevel*) echo /r ;;\n" +
			"*core.worktree*) echo 'fatal: bad config line' >&2; exit 3 ;;\nesac\n",
			[]string{"git config core.worktree", "exit status 3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			installFakeGit(t, c.script)
			got, err := GitCommonDir(context.Background(), t.TempDir())
			if !hasKind(err, cascade.KindUnavailable, c.substrs...) || got != (scope.GitDirs{}) {
				t.Errorf("GitCommonDir = %+v, %v; want zero GitDirs and KindUnavailable containing %q", got, err, c.substrs)
			}
		})
	}
}
