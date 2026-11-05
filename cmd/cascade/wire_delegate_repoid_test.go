package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// repoIDTestDB opens a fresh sqlite database carrying the scope tables.
func repoIDTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "cascade.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, fixedRepoIDClock{}, "", ""); err != nil {
		t.Fatalf("ApplyScopeSchema: %v", err)
	}
	return db
}

// fixedRepoIDClock is a deterministic migrate.Clock for the schema apply.
type fixedRepoIDClock struct{}

func (fixedRepoIDClock) Now() time.Time { return time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC) }

// repoIDGit runs git in dir with a fixed identity, failing the test on error.
func repoIDGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repoIDGitRepo creates a committed real-git repository, skipping the test
// when no git child process can start.
func repoIDGitRepo(t *testing.T) string {
	t.Helper()
	if err := exec.Command("git", "--version").Run(); err != nil {
		t.Skipf("git not runnable: %v", err)
	}
	dir := t.TempDir()
	repoIDGit(t, dir, "init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoIDGit(t, dir, "add", "-A")
	repoIDGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// repoIDRowCount counts rows in one scope table.
func repoIDRowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// wantRepoIDKind asserts err carries kind and is never a KindNotFound miss
// unless kind is KindNotFound.
func wantRepoIDKind(t *testing.T, label string, id string, err error, kind cascade.Kind) {
	t.Helper()
	if err == nil || id != "" {
		t.Fatalf("%s: got (%q, %v), want (\"\", %s error)", label, id, err, kind)
	}
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("%s: kind = %v (ok=%v), want %s: %v", label, got, ok, kind, err)
	}
}

func TestDelegateRepoIDOfResolvesRegisteredRepo(t *testing.T) {
	ctx := context.Background()
	root := repoIDGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	repoIDGit(t, root, "worktree", "add", "-q", wt, "-b", "wtbranch")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}

	db := repoIDTestDB(t)
	graph := scope.NewGraphStore(db)
	rec, err := graph.EnsureRepository(ctx, root, "https://example.invalid/r.git", cascadecontext.GitCommonDir)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("EnsureRepository returned an empty id")
	}
	repos, paths := repoIDRowCount(t, db, "context_repository"), repoIDRowCount(t, db, "context_repo_path")

	repoIDOf := delegateRepoIDOf(graph, cascadecontext.GitCommonDir)
	for _, p := range []string{root, filepath.Join(root, "sub"), wt, link} {
		got, err := repoIDOf(ctx, p)
		if err != nil {
			t.Fatalf("repoIDOf(%q): %v", p, err)
		}
		if got != rec.ID {
			t.Errorf("repoIDOf(%q) = %q, want %q", p, got, rec.ID)
		}
	}
	if r, p := repoIDRowCount(t, db, "context_repository"), repoIDRowCount(t, db, "context_repo_path"); r != repos || p != paths {
		t.Errorf("rows after lookup = (%d, %d), want unchanged (%d, %d): the binder must not write", r, p, repos, paths)
	}
}

func TestDelegateRepoIDOfUnknownRepoErrors(t *testing.T) {
	ctx := context.Background()
	root := repoIDGitRepo(t)
	db := repoIDTestDB(t)
	graph := scope.NewGraphStore(db)
	repoIDOf := delegateRepoIDOf(graph, cascadecontext.GitCommonDir)

	// (a) an unregistered real-git repository: the sentinel, no write.
	id, err := repoIDOf(ctx, root)
	wantRepoIDKind(t, "unregistered", id, err, cascade.KindNotFound)
	assertRepoIDNotRegisteredChain(t, err)
	if n := repoIDRowCount(t, db, "context_repository") + repoIDRowCount(t, db, "context_repo_path"); n != 0 {
		t.Errorf("unregistered lookup wrote %d rows, want 0", n)
	}

	// (b) a relative path and a non-repository directory are invalid input.
	checkInvalidRepoIDPaths(ctx, t, repoIDOf)

	// (c) nil dependencies yield a non-nil binder that always refuses.
	checkNilRepoIDBinders(ctx, t, graph, root)

	// (d) a repository row with an empty id is an integrity failure.
	canonical, err := scope.CanonicalRepoRoot(ctx, root, cascadecontext.GitCommonDir)
	if err != nil {
		t.Fatalf("CanonicalRepoRoot: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO context_repository (id, remote, path_hash) VALUES ('', '', '')`); err != nil {
		t.Fatalf("seed empty-id repository: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO context_repo_path (root_path, repository_id) VALUES (?, '')`, canonical); err != nil {
		t.Fatalf("seed empty-id repo_path: %v", err)
	}
	id, err = repoIDOf(ctx, root)
	wantRepoIDKind(t, "empty id", id, err, cascade.KindIntegrity)

	// (e) a database failure is unavailable, never read as a miss.
	_ = db.Close()
	id, err = repoIDOf(ctx, root)
	wantRepoIDKind(t, "closed db", id, err, cascade.KindUnavailable)
	if errors.Is(err, errRepoIDNotRegistered) {
		t.Errorf("closed db: error reads as a miss: %v", err)
	}
}

// assertRepoIDNotRegisteredChain: the unregistered error carries the exact
// sentinel in its chain and its text.
func assertRepoIDNotRegisteredChain(t *testing.T, err error) {
	t.Helper()
	found := false
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(errRepoIDNotRegistered) {
			found = true
		}
	}
	if !found {
		t.Errorf("unregistered: error chain lacks errRepoIDNotRegistered: %v", err)
	}
	if !strings.Contains(err.Error(), "delegate: repository not registered") {
		t.Errorf("unregistered: message %q lacks the sentinel text", err.Error())
	}
}

// checkInvalidRepoIDPaths: a relative path and a non-repository directory
// are refused, never read as an unregistered repository.
func checkInvalidRepoIDPaths(ctx context.Context, t *testing.T, repoIDOf func(context.Context, string) (string, error)) {
	t.Helper()
	id, err := repoIDOf(ctx, "relative/repo")
	wantRepoIDKind(t, "relative", id, err, cascade.KindInvalidInput)
	id, err = repoIDOf(ctx, t.TempDir())
	if err == nil || id != "" || errors.Is(err, errRepoIDNotRegistered) {
		t.Errorf("non-repo dir: got (%q, %v), want a non-NotFound error", id, err)
	}
}

// checkNilRepoIDBinders: nil dependencies yield a non-nil binder that always
// refuses.
func checkNilRepoIDBinders(ctx context.Context, t *testing.T, graph *scope.GraphStore, root string) {
	t.Helper()
	for label, f := range map[string]func(context.Context, string) (string, error){
		"nil graph": delegateRepoIDOf(nil, cascadecontext.GitCommonDir),
		"nil git":   delegateRepoIDOf(graph, nil),
	} {
		if f == nil {
			t.Fatalf("%s: binder is nil", label)
		}
		id, err := f(ctx, root)
		wantRepoIDKind(t, label, id, err, cascade.KindInvalidInput)
	}
}
