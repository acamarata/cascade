//go:build !windows

package main

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// factsTestEnv isolates HOME, USERPROFILE and CASCADE_HOME and returns a
// path provider whose data directory is fresh.
func factsTestEnv(t *testing.T) elevatedTestPaths {
	t.Helper()
	if !gitRunnable() {
		factsFixtureGit(t)
	}
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	root := t.TempDir()
	return elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: root}, data: filepath.Join(root, "data")}
}

// gitRunnable reports whether a git child process can start here; some
// sandboxes put git on PATH yet refuse to fork it.
var gitRunnable = sync.OnceValue(func() bool {
	return exec.Command("git", "--version").Run() == nil
})

// gitIn runs git in dir with a fixed identity and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// factsGitRepo creates a committed git repository holding files.
func factsGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if gitRunnable() {
		gitIn(t, dir, "init", "-q")
	} else {
		factsFixtureRoots[factsResolved(t, dir)] = dir
	}
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if gitRunnable() {
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	}
	return dir
}

// storedInventory reads repoID's inventory back from paths' cascade.db.
func storedInventory(t *testing.T, paths runtime.PathProvider, repoID string) (repo.Inventory, bool) {
	t.Helper()
	var inv repo.Inventory
	var ok bool
	err := withRepoDB(context.Background(), paths, runtime.NewSystemClock(), func(db *sql.DB) error {
		var err error
		inv, ok, err = repo.NewStore(db).Get(context.Background(), repoID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return inv, ok
}

func TestRegisterRepositoryUpsertsInventory(t *testing.T) {
	paths := factsTestEnv(t)
	dir := factsGitRepo(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n", "main.go": "package main\n"})
	ctx := context.Background()
	inv, err := registerRepository(ctx, paths, runtime.NewSystemClock(), dir)
	if err != nil {
		t.Fatalf("registerRepository: %v", err)
	}
	stored, ok := storedInventory(t, paths, inv.Repository.ID)
	if !ok || stored.Repository.ID == "" || stored.ScannedAt == 0 {
		t.Fatalf("stored inventory = %+v, %v", stored, ok)
	}
	foundGo := false
	for _, l := range stored.Languages {
		foundGo = foundGo || (l.Language == repo.LanguageGo && l.Detected)
	}
	if !foundGo {
		t.Fatalf("stored inventory missed the Go module: %+v", stored.Languages)
	}
	notRepo := t.TempDir()
	for _, bad := range []string{"", "relative/path", notRepo} {
		_, err := registerRepository(ctx, paths, runtime.NewSystemClock(), bad)
		if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
			t.Errorf("registerRepository(%q) err = %v, want KindInvalidInput", bad, err)
		}
	}
}

func TestRegisterRepositoryWorktreeSameID(t *testing.T) {
	paths := factsTestEnv(t)
	main := factsGitRepo(t, map[string]string{"README.md": "# x\n"})
	wt := filepath.Join(t.TempDir(), "wt")
	factsWorktree(t, main, wt)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(main, link); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range []string{main, wt, link} {
		inv, err := registerRepository(context.Background(), paths, runtime.NewSystemClock(), p)
		if err != nil {
			t.Fatalf("registerRepository(%s): %v", p, err)
		}
		ids[inv.Repository.ID] = true
	}
	if len(ids) != 1 {
		t.Fatalf("main checkout, worktree and symlink yielded %d ids, want 1: %v", len(ids), ids)
	}
	var n int
	_ = withRepoDB(context.Background(), paths, runtime.NewSystemClock(), func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM context_repository`).Scan(&n)
	})
	if n != 1 {
		t.Fatalf("context_repository holds %d rows, want 1", n)
	}
}

func TestRegisterRepositoryRecordsOriginRemote(t *testing.T) {
	paths := factsTestEnv(t)
	dir := factsGitRepo(t, map[string]string{"README.md": "# x\n"})
	const url = "https://example.com/acme/widget.git"
	if gitRunnable() {
		gitIn(t, dir, "remote", "add", "origin", url)
	} else {
		factsFixtureRemotes[factsResolved(t, dir)] = url
	}
	inv, err := registerRepository(context.Background(), paths, runtime.NewSystemClock(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Repository.Remote != url {
		t.Fatalf("inventory remote = %q, want %q", inv.Repository.Remote, url)
	}
	var rec scope.RepositoryRecord
	_ = withRepoDB(context.Background(), paths, runtime.NewSystemClock(), func(db *sql.DB) error {
		var err error
		rec, _, err = scope.NewGraphStore(db).RepositoryByID(context.Background(), inv.Repository.ID)
		return err
	})
	if rec.Remote != url {
		t.Fatalf("context_repository remote = %q, want %q", rec.Remote, url)
	}
}

// Fixture maps supply only the external Git observations when subprocesses
// are unavailable. Canonicalization, scanning, migrations and stores remain real.
var (
	factsFixtureRoots   map[string]string
	factsFixtureRemotes map[string]string
)

func factsFixtureGit(t *testing.T) {
	t.Helper()
	oldDirs, oldRemote := repoGitCommonDir, repoRemoteURL
	oldRoots, oldRemotes := factsFixtureRoots, factsFixtureRemotes
	factsFixtureRoots, factsFixtureRemotes = map[string]string{}, map[string]string{}
	repoGitCommonDir = func(_ context.Context, p string) (scope.GitDirs, error) {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return scope.GitDirs{}, err
		}
		root, ok := factsFixtureRoots[resolved]
		if !ok {
			return scope.GitDirs{}, cascade.New(cascade.KindInvalidInput, "not a fixture repository")
		}
		return scope.GitDirs{CommonDir: filepath.Join(root, ".git"), GitDir: filepath.Join(resolved, ".git"), Toplevel: resolved}, nil
	}
	repoRemoteURL = func(_ context.Context, root string) string { return factsFixtureRemotes[root] }
	t.Cleanup(func() {
		repoGitCommonDir, repoRemoteURL = oldDirs, oldRemote
		factsFixtureRoots, factsFixtureRemotes = oldRoots, oldRemotes
	})
}

func factsWorktree(t *testing.T, main, wt string) {
	t.Helper()
	if gitRunnable() {
		gitIn(t, main, "worktree", "add", "-q", wt)
		return
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	factsFixtureRoots[factsResolved(t, wt)] = main
}

// factsResolved returns p with symlinks resolved, the form repoGitCommonDir
// looks fixture roots up by (a temp directory sits behind /var or /tmp on macOS).
func factsResolved(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// factsTrackedFiles uses Git when available; fixture repositories contain only
// tracked files, so the fallback enumerates precisely the fixture's contents.
func factsTrackedFiles(ctx context.Context, root string) ([]string, error) {
	if gitRunnable() {
		return gitTrackedFiles(ctx, root)
	}
	var rels []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err == nil {
			rels = append(rels, filepath.ToSlash(rel))
		}
		return err
	})
	return rels, err
}

func TestFactsWithoutGitSubprocess(t *testing.T) {
	original := gitRunnable
	gitRunnable = func() bool { return false }
	t.Cleanup(func() { gitRunnable = original })
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"inventory", TestRegisterRepositoryUpsertsInventory},
		{"worktree", TestRegisterRepositoryWorktreeSameID},
		{"remote", TestRegisterRepositoryRecordsOriginRemote},
		{"intake", TestFactsProposeRunsExtractIntake},
		{"admission", TestFactsProposeSendsOnlyAdmittedContent},
		{"tier", TestFactsProposeTierFromTaskClass},
	} {
		t.Run(tc.name, tc.run)
	}
}
