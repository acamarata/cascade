package scope

// Real-git helpers (realGitCommonDir, runGit, newTestGitRepo, mustSymlink,
// newSeparateGitDirRepo, wantInvalidInput) live in canonical_root_test.go.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// countRows returns the number of rows in one of this package's tables.
func countRows(t *testing.T, s *GraphStore, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// seedRepo writes a repository row (skipped when rec.ID is "") and a
// repo_path row binding rootPath to id, the way a pre-ticket writer did.
func seedRepo(t *testing.T, s *GraphStore, rec RepositoryRecord, id, rootPath string) {
	t.Helper()
	ctx := context.Background()
	if rec.ID != "" {
		if err := s.PutRepository(ctx, rec); err != nil {
			t.Fatalf("PutRepository(%q): %v", rec.ID, err)
		}
	}
	if err := s.PutRepoPath(ctx, RepoPathRecord{RootPath: rootPath, RepositoryID: id}); err != nil {
		t.Fatalf("PutRepoPath(%q): %v", rootPath, err)
	}
}

// legacyRecord derives a record the pre-ticket way for rootPath.
func legacyRecord(remote, rootPath string) RepositoryRecord {
	h := hashCanonicalRoot(rootPath)
	return RepositoryRecord{ID: deriveRepositoryID(remote, h), Remote: remote, PathHash: h}
}

// TestEnsureRepositoryIdempotentAcrossWorktrees proves a worktree and its
// main checkout resolve to the SAME record across repeated calls.
func TestEnsureRepositoryIdempotentAcrossWorktrees(t *testing.T) {
	root := newTestGitRepo(t)
	s := newTestStore(t)
	ctx := context.Background()
	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", wtDir, "-b", "wtbranch")

	var ids []string
	for _, p := range []string{root, wtDir, root} {
		rec, err := s.EnsureRepository(ctx, p, "https://example.com/r.git", realGitCommonDir)
		if err != nil {
			t.Fatalf("EnsureRepository(%q): %v", p, err)
		}
		ids = append(ids, rec.ID)
	}
	if ids[1] != ids[0] || ids[2] != ids[0] || countRows(t, s, tableRepository) != 1 {
		t.Errorf("ids = %v with %d repository rows, want one id and one row", ids, countRows(t, s, tableRepository))
	}
}

// TestEnsureRepositoryKeepsHeadIDs proves a row written before this
// ticket at an already-canonical root is found, not duplicated.
func TestEnsureRepositoryKeepsHeadIDs(t *testing.T) {
	root := newTestGitRepo(t)
	s := newTestStore(t)
	legacy := legacyRecord("https://example.com/legacy.git", root)
	seedRepo(t, s, legacy, legacy.ID, root)

	got, err := s.EnsureRepository(context.Background(), root, legacy.Remote, realGitCommonDir)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if got.ID != legacy.ID || countRows(t, s, tableRepository) != 1 {
		t.Errorf("id = %q (%d rows), want %q found not re-minted", got.ID, countRows(t, s, tableRepository), legacy.ID)
	}
}

// TestEnsureRepositoryAdoptsSymlinkedLegacyRow proves a repository
// registered through a symlinked root (a stand-in for `~/Sites`) keeps its
// legacy id in both call orders, and that ambiguous or dangling legacy
// rows refuse with no write.
func TestEnsureRepositoryAdoptsSymlinkedLegacyRow(t *testing.T) {
	t.Run("canonical path first", func(t *testing.T) { testAdoptsLegacyRow(t, true) })
	t.Run("symlinked path first", func(t *testing.T) { testAdoptsLegacyRow(t, false) })
	t.Run("two distinct legacy ids conflict", testAdoptsLegacyRowConflict)
	t.Run("dangling legacy path refuses before writing", testAdoptsLegacyRowDangling)
}

// testAdoptsLegacyRow registers a legacy row at one of (symlink, root) and
// calls EnsureRepository with the other: it must adopt the legacy id and
// add a repo_path row for the canonical root, never mint.
func testAdoptsLegacyRow(t *testing.T, canonicalFirst bool) {
	root := newTestGitRepo(t)
	link := mustSymlink(t, root)
	registered, callWith := link, root
	if !canonicalFirst {
		registered, callWith = root, link
	}
	s := newTestStore(t)
	legacy := legacyRecord("https://example.com/legacy.git", registered)
	seedRepo(t, s, legacy, legacy.ID, registered)

	got, err := s.EnsureRepository(context.Background(), callWith, "https://example.com/new.git", realGitCommonDir)
	if err != nil {
		t.Fatalf("EnsureRepository(%q): %v", callWith, err)
	}
	if got.ID != legacy.ID || countRows(t, s, tableRepository) != 1 {
		t.Errorf("id = %q (%d rows), want %q adopted, not minted", got.ID, countRows(t, s, tableRepository), legacy.ID)
	}
	if rec, ok, err := s.RepositoryForRoot(context.Background(), root); err != nil || !ok || rec.ID != legacy.ID {
		t.Errorf("RepositoryForRoot(canonical) = %+v ok=%v err=%v, want the legacy id bound", rec, ok, err)
	}
}

// testAdoptsLegacyRowConflict registers two DISTINCT legacy ids through two
// symlinks of one canonical root: EnsureRepository must refuse with
// KindConflict and write nothing.
func testAdoptsLegacyRowConflict(t *testing.T) {
	root := newTestGitRepo(t)
	s := newTestStore(t)
	for _, id := range []string{"legacy-a", "legacy-b"} {
		seedRepo(t, s, RepositoryRecord{ID: id, Remote: id, PathHash: "h-" + id}, id, mustSymlink(t, root))
	}

	_, err := s.EnsureRepository(context.Background(), root, "https://example.com/new.git", realGitCommonDir)
	if kind, ok := cascade.KindOf(err); err == nil || !ok || kind != cascade.KindConflict ||
		!strings.Contains(err.Error(), "2 distinct legacy repository ids") {
		t.Fatalf("err = %v, want KindConflict naming 2 distinct legacy repository ids", err)
	}
	if r, p := countRows(t, s, tableRepository), countRows(t, s, tableRepoPath); r != 2 || p != 2 {
		t.Errorf("after conflict: %d repository rows, %d repo_path rows; want 2 and 2 (no write)", r, p)
	}
}

// testAdoptsLegacyRowDangling registers a legacy repo_path whose id has no
// repository row: adoption must refuse KindIntegrity before writing.
func testAdoptsLegacyRowDangling(t *testing.T) {
	root := newTestGitRepo(t)
	s := newTestStore(t)
	seedRepo(t, s, RepositoryRecord{}, "dangling", mustSymlink(t, root))

	_, err := s.EnsureRepository(context.Background(), root, "", realGitCommonDir)
	if kind, ok := cascade.KindOf(err); err == nil || !ok || kind != cascade.KindIntegrity ||
		!strings.Contains(err.Error(), "has no repository row") {
		t.Fatalf("err = %v, want KindIntegrity naming the missing repository row", err)
	}
	if r, p := countRows(t, s, tableRepository), countRows(t, s, tableRepoPath); r != 0 || p != 1 {
		t.Errorf("after refusal: %d repository rows, %d repo_path rows; want 0 and 1 (no write)", r, p)
	}
}

// TestEnsureRepositoryRefusesNilGit proves a nil GitCommonDirFunc is
// refused at EnsureRepository itself with no row written.
func TestEnsureRepositoryRefusesNilGit(t *testing.T) {
	s := newTestStore(t)
	_, err := s.EnsureRepository(context.Background(), "/abs/path", "", nil)
	wantInvalidInput(t, err, "EnsureRepository requires a non-nil git", "nil git")
	if r, p := countRows(t, s, tableRepository), countRows(t, s, tableRepoPath); r != 0 || p != 0 {
		t.Errorf("after nil git: %d repository rows, %d repo_path rows; want none", r, p)
	}
}

// TestEnsureRepositorySymlinkedRootOneID proves canonical, symlinked and
// worktree paths of one repository resolve to one id end to end.
func TestEnsureRepositorySymlinkedRootOneID(t *testing.T) {
	root := newTestGitRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit(t, root, "worktree", "add", "-q", wtDir, "-b", "wtbranch")
	s := newTestStore(t)

	ids := map[string]bool{}
	for _, p := range []string{root, mustSymlink(t, root), wtDir} {
		rec, err := s.EnsureRepository(context.Background(), p, "https://example.com/one.git", realGitCommonDir)
		if err != nil {
			t.Fatalf("EnsureRepository(%q): %v", p, err)
		}
		ids[rec.ID] = true
	}
	if len(ids) != 1 || countRows(t, s, tableRepository) != 1 {
		t.Errorf("ids = %v (%d rows), want one id and one row", ids, countRows(t, s, tableRepository))
	}
}

// TestCanonicalRootSeparateGitDirSharedID proves a `--separate-git-dir`
// repository whose core.worktree names its main worktree (set by the test;
// git never sets it) resolves one id from the main worktree and from a
// `git worktree add` worktree.
func TestCanonicalRootSeparateGitDirSharedID(t *testing.T) {
	mainDir, wtDir := newSeparateGitDirRepo(t, true)
	assertOneRootOneID(t, mainDir, mainDir, wtDir)
}

// TestCanonicalRootBareWithWorktreeSharedID proves step 0: `git clone
// --bare src proj/.git` plus `git worktree add proj/main` resolves the
// bare dir and its worktree to one root, proj/.git, and one id (step 1
// alone would give the worktree proj, a second identity).
func TestCanonicalRootBareWithWorktreeSharedID(t *testing.T) {
	src := newTestGitRepo(t)
	proj := mustResolve(t, t.TempDir())
	bare, wt := filepath.Join(proj, ".git"), filepath.Join(proj, "main")
	runGit(t, proj, "clone", "-q", "--bare", src, bare)
	runGit(t, bare, "worktree", "add", "-q", wt, "-b", "wtbranch")
	assertOneRootOneID(t, bare, bare, wt)
}

// assertOneRootOneID asserts every path resolves to want and that
// EnsureRepository binds all of them to one id and one repository row.
func assertOneRootOneID(t *testing.T, want string, paths ...string) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	ids := map[string]bool{}
	for _, p := range paths {
		if got, err := CanonicalRepoRoot(ctx, p, realGitCommonDir); err != nil || got != want {
			t.Errorf("CanonicalRepoRoot(%q) = %q, %v; want %q", p, got, err, want)
		}
		rec, err := s.EnsureRepository(ctx, p, "", realGitCommonDir)
		if err != nil {
			t.Fatalf("EnsureRepository(%q): %v", p, err)
		}
		ids[rec.ID] = true
	}
	if len(ids) != 1 || countRows(t, s, tableRepository) != 1 {
		t.Errorf("ids = %v (%d rows), want one shared id and one row", ids, countRows(t, s, tableRepository))
	}
}

// TestRepositoryByID proves RepositoryByID returns a stored row and
// reports ok=false (never an error) for an id nothing has minted.
func TestRepositoryByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	want := RepositoryRecord{ID: "r1", Remote: "https://example.com/r1.git", PathHash: "h1"}
	if err := s.PutRepository(ctx, want); err != nil {
		t.Fatalf("PutRepository: %v", err)
	}
	if got, ok, err := s.RepositoryByID(ctx, "r1"); err != nil || !ok || got != want {
		t.Errorf("RepositoryByID(r1) = %+v ok=%v err=%v, want %+v", got, ok, err, want)
	}
	if _, ok, err := s.RepositoryByID(ctx, "never-minted"); err != nil || ok {
		t.Errorf("RepositoryByID(never-minted): ok=%v err=%v, want false, nil", ok, err)
	}
}

// TestEnsureRepositoryIDDeterministic proves the moved remote+path-hash
// derivation stays deterministic and remote-distinct across fresh stores.
func TestEnsureRepositoryIDDeterministic(t *testing.T) {
	root := newTestGitRepo(t)
	// openTestDB keys its in-memory database on t.Name(), so each mint
	// runs in its own subtest to get a fresh store.
	n := 0
	mint := func(remote string) string {
		var id string
		n++
		t.Run(fmt.Sprintf("mint%d", n), func(t *testing.T) {
			rec, err := newTestStore(t).EnsureRepository(context.Background(), root, remote, realGitCommonDir)
			if err != nil {
				t.Fatalf("EnsureRepository(%q): %v", remote, err)
			}
			id = rec.ID
		})
		return id
	}
	if a, b := mint("r"), mint("r"); a != b || a == "" {
		t.Errorf("ids for one root+remote = %q, %q, want equal and non-empty", a, b)
	}
	if mint("r1") == mint("r2") {
		t.Error("ids collided for different remotes")
	}
	if h := hashCanonicalRoot("/a/b"); h != hashCanonicalRoot(filepath.ToSlash(filepath.Join("/a", "b"))) || len(h) != 64 {
		t.Errorf("hashCanonicalRoot(/a/b) = %q, want a stable 64-hex digest", h)
	}
}
