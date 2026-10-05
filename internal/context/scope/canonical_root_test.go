package scope

// Test files are exempt from the egress scan (internal/build/egress_scan.go),
// so this file may import os/exec: it runs the REAL git binary over
// t.TempDir() repositories (Art.2) behind CanonicalRepoRoot's seam.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// realGitCommonDir mirrors internal/context.GitCommonDir with the same git
// commands. It is duplicated rather than imported: internal/context
// imports this package (assembly.go), so importing it back would cycle.
func realGitCommonDir(ctx context.Context, dir string) (GitDirs, error) {
	out, err := gitOut(ctx, dir, "rev-parse", "--path-format=absolute",
		"--git-common-dir", "--absolute-git-dir", "--is-bare-repository")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return GitDirs{}, cascade.Newf(cascade.KindInvalidInput, "test: %q: %v", dir, err)
		}
		return GitDirs{}, cascade.Wrapf(cascade.KindUnavailable, err, "test: git in %q", dir)
	}
	f := strings.Split(out, "\n")
	if len(f) != 3 {
		return GitDirs{}, cascade.Newf(cascade.KindUnavailable, "test: rev-parse output %q", out)
	}
	d := GitDirs{CommonDir: filepath.Clean(f[0]), GitDir: filepath.Clean(f[1]), Bare: f[2] == "true"}
	if b, err := gitOut(ctx, dir, "config", "--file", filepath.Join(d.CommonDir, "config"), "--type=bool", "--get", "core.bare"); err == nil {
		d.Bare = b == "true"
	}
	if top, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel"); err == nil && !d.Bare {
		d.Toplevel = filepath.Clean(top)
	}
	d.CoreWorktree, _ = gitOut(ctx, dir, "config", "--file", filepath.Join(d.CommonDir, "config"), "--get", "core.worktree")
	return d, nil
}

// gitOut runs git in dir and returns trimmed stdout; stderr is in the error.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", cascade.Newf(cascade.KindUnavailable, "git %v: %s", args, exitErr.Stderr)
	}
	return strings.TrimSpace(string(out)), err
}

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// mustResolve returns p symlink-resolved and cleaned (t.TempDir() on macOS
// sits under /var -> /private/var, so baselines must be resolved).
func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return filepath.Clean(r)
}

// mustSymlink creates a symlink to target in a fresh temp dir.
func mustSymlink(t *testing.T, target string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	return link
}

// newTestGitRepo inits a real repository with one commit under
// t.TempDir() and returns its resolved path.
func newTestGitRepo(t *testing.T) string {
	t.Helper()
	skipWithoutGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return mustResolve(t, dir)
}

// newSeparateGitDirRepo makes `git init --separate-git-dir <sep> <main>`
// with a `git worktree add` worktree, setting core.worktree to main
// itself when coreWorktree is true (git never sets it for this layout).
func newSeparateGitDirRepo(t *testing.T, coreWorktree bool) (mainDir, wtDir string) {
	t.Helper()
	skipWithoutGit(t)
	base := mustResolve(t, t.TempDir())
	sep, mainDir, wtDir := filepath.Join(base, "sep"), filepath.Join(base, "main"), filepath.Join(base, "wt")
	runGit(t, base, "init", "-q", "--separate-git-dir", sep, mainDir)
	runGit(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, mainDir, "worktree", "add", "-q", wtDir, "-b", "wtbranch")
	if coreWorktree {
		runGit(t, base, "--git-dir="+sep, "config", "core.worktree", mainDir)
	}
	return mainDir, wtDir
}

// isKind reports whether err is a cascade error of kind whose message
// contains substr.
func isKind(err error, kind cascade.Kind, substr string) bool {
	k, ok := cascade.KindOf(err)
	return err != nil && ok && k == kind && strings.Contains(err.Error(), substr)
}

// wantInvalidInput asserts err is KindInvalidInput and its message
// contains substr (identity plus message, never errors.Is alone).
func wantInvalidInput(t *testing.T, err error, substr, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want KindInvalidInput error, got nil", what)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput || !strings.Contains(err.Error(), substr) {
		t.Errorf("%s: err = %v (kind %v, ok=%v), want KindInvalidInput containing %q", what, err, kind, ok, substr)
	}
}

// TestCanonicalRepoRootWorktreeSame proves a main checkout, a `git
// worktree add` worktree of it, and a subdirectory all resolve to the
// same canonical root.
func TestCanonicalRepoRootWorktreeSame(t *testing.T) {
	root := newTestGitRepo(t)
	ctx := context.Background()
	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", wtDir, "-b", "wtbranch")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", sub, err)
	}
	for _, p := range []string{root, wtDir, sub} {
		got, err := CanonicalRepoRoot(ctx, p, realGitCommonDir)
		if err != nil || got != root {
			t.Errorf("CanonicalRepoRoot(%q) = %q, %v; want %q (main checkout)", p, got, err, root)
		}
	}
}

// TestCanonicalRepoRootSymlinkSame proves a path through a symlinked
// ancestor resolves to the real root. Real git already resolves the
// symlink itself, so the second half injects an UNRESOLVED common dir to
// make CanonicalRepoRoot's own EvalSymlinks load-bearing.
func TestCanonicalRepoRootSymlinkSame(t *testing.T) {
	root := newTestGitRepo(t)
	ctx := context.Background()
	link := mustSymlink(t, root)
	if got, err := CanonicalRepoRoot(ctx, link, realGitCommonDir); err != nil || got != root {
		t.Errorf("CanonicalRepoRoot(symlinked) = %q, %v; want %q", got, err, root)
	}
	unresolved := func(context.Context, string) (GitDirs, error) {
		gd := filepath.Join(link, ".git")
		return GitDirs{CommonDir: gd, GitDir: gd, Toplevel: link}, nil
	}
	if got, err := CanonicalRepoRoot(ctx, link, unresolved); err != nil || got != root {
		t.Errorf("CanonicalRepoRoot(unresolved common dir) = %q, %v; want %q via EvalSymlinks", got, err, root)
	}
}

// TestCanonicalRepoRootBareAndErrors covers the bare repository (the
// common dir IS the root), a path inside a non-bare .git directory (the
// step-1 parent, never "." or ""), and the KindInvalidInput gates.
func TestCanonicalRepoRootBareAndErrors(t *testing.T) {
	skipWithoutGit(t)
	ctx := context.Background()
	bareDir := mustResolve(t, t.TempDir())
	runGit(t, bareDir, "init", "-q", "--bare")
	if got, err := CanonicalRepoRoot(ctx, bareDir, realGitCommonDir); err != nil || got != bareDir {
		t.Errorf("CanonicalRepoRoot(bare) = %q, %v; want %q (its own dir)", got, err, bareDir)
	}

	root := newTestGitRepo(t)
	if got, err := CanonicalRepoRoot(ctx, filepath.Join(root, ".git"), realGitCommonDir); err != nil || got != root {
		t.Errorf("CanonicalRepoRoot(inside .git) = %q, %v; want %q", got, err, root)
	}

	runGit(t, root, "config", "core.worktree", mustResolve(t, t.TempDir()))
	if got, err := CanonicalRepoRoot(ctx, root, realGitCommonDir); err != nil || got != root {
		t.Errorf("CanonicalRepoRoot(.git repo with core.worktree elsewhere) = %q, %v; want %q (step 1 first)", got, err, root)
	}
	gone := func(context.Context, string) (GitDirs, error) {
		return GitDirs{CommonDir: filepath.Join(root, "gone", ".git")}, nil
	}
	if _, err := CanonicalRepoRoot(ctx, root, gone); !isKind(err, cascade.KindUnavailable, "resolve symlinks") {
		t.Errorf("CanonicalRepoRoot(missing root) err = %v, want KindUnavailable resolve symlinks", err)
	}

	_, err := CanonicalRepoRoot(ctx, mustResolve(t, t.TempDir()), realGitCommonDir)
	wantInvalidInput(t, err, "not a git repository", "non-repository")
	_, err = CanonicalRepoRoot(ctx, "relative/path", realGitCommonDir)
	wantInvalidInput(t, err, "absolute path", "relative path")
	_, err = CanonicalRepoRoot(ctx, "/abs/path", nil)
	wantInvalidInput(t, err, "non-nil git", "nil git")
}

// TestCanonicalRootLinkedAmbiguousRefuses proves step 4: a
// `--separate-git-dir` repository without core.worktree resolves its main
// worktree through step 3, and its linked worktree (no `.git` common dir,
// no core.worktree, not the main worktree) refuses instead of guessing.
func TestCanonicalRootLinkedAmbiguousRefuses(t *testing.T) {
	mainDir, wtDir := newSeparateGitDirRepo(t, false)
	ctx := context.Background()
	if got, err := CanonicalRepoRoot(ctx, mainDir, realGitCommonDir); err != nil || got != mainDir {
		t.Errorf("CanonicalRepoRoot(main worktree) = %q, %v; want %q (step 3)", got, err, mainDir)
	}
	got, err := CanonicalRepoRoot(ctx, wtDir, realGitCommonDir)
	wantInvalidInput(t, err, errRepoRootAmbiguous, "linked worktree without a main root")
	if got != "" {
		t.Errorf("CanonicalRepoRoot(linked worktree) = %q, want \"\" with the refusal", got)
	}

	noTop := func(context.Context, string) (GitDirs, error) {
		return GitDirs{CommonDir: "/r/sep", GitDir: "/r/sep"}, nil
	}
	_, err = CanonicalRepoRoot(ctx, mainDir, noTop)
	wantInvalidInput(t, err, errRepoRootAmbiguous, "main worktree with empty Toplevel")
}

// TestScopeImportsNoExec proves internal/context/scope's production files
// stay free of os/exec: CanonicalRepoRoot goes through the injected seam.
func TestScopeImportsNoExec(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", "{{.Imports}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if strings.Contains(string(out), "os/exec") {
		t.Errorf("internal/context/scope imports os/exec: %s", out)
	}
}

// newSuperWithSubmodules makes a super-repository with two submodules,
// subA and subB, cloned from two independent real repositories.
func newSuperWithSubmodules(t *testing.T) (subA, subB string) {
	t.Helper()
	super := newTestGitRepo(t)
	for _, name := range []string{"subA", "subB"} {
		src := newTestGitRepo(t)
		runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, name)
	}
	return filepath.Join(super, "subA"), filepath.Join(super, "subB")
}

// TestCanonicalRootSubmodulesDistinct proves two submodules of one
// super-repository (common dirs <super>/.git/modules/<name>) resolve to
// their own work trees through core.worktree, so EnsureRepository mints
// two ids and two repository rows, never one shared identity.
func TestCanonicalRootSubmodulesDistinct(t *testing.T) {
	subA, subB := newSuperWithSubmodules(t)
	s := newTestStore(t)
	ctx := context.Background()
	ids := map[string]bool{}
	for _, sub := range []string{subA, subB} {
		if got, err := CanonicalRepoRoot(ctx, sub, realGitCommonDir); err != nil || got != sub {
			t.Errorf("CanonicalRepoRoot(%q) = %q, %v; want the submodule's own work tree", sub, got, err)
		}
		rec, err := s.EnsureRepository(ctx, sub, "", realGitCommonDir)
		if err != nil {
			t.Fatalf("EnsureRepository(%q): %v", sub, err)
		}
		ids[rec.ID] = true
		if got, ok, err := s.RepositoryForRoot(ctx, sub); err != nil || !ok || got.ID != rec.ID {
			t.Errorf("RepositoryForRoot(%q) = %+v ok=%v err=%v, want id %q", sub, got, ok, err, rec.ID)
		}
	}
	if len(ids) != 2 || countRows(t, s, tableRepository) != 2 || countRows(t, s, tableRepoPath) != 2 {
		t.Errorf("submodules: %d ids, %d repository rows, %d repo_path rows; want 2 of each",
			len(ids), countRows(t, s, tableRepository), countRows(t, s, tableRepoPath))
	}
}
