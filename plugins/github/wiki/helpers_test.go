// Purpose (this file): shared test doubles and fixtures for sync_test.go,
//
//	drift_test.go and runner_test.go — a local bare git repo standing in
//	for the remote wiki (no network reaches any test in this package), a
//	GitRunner that rewrites the github.com URL Sync/CheckDrift's real
//	resolveWikiURL produces into that local path (so the PRODUCTION URL-
//	building code runs, unmodified, in every test), and a fake
//	VisibilityChecker.
//
// SPORT: plugins/github/wiki:test-helpers (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// newBareRemote creates a bare git repository under t.TempDir(), standing
// in for a GitHub wiki's remote. It is entirely local: no test in this
// package ever dials github.com.
func newBareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wiki-remote.git")
	runGit(t, "", "init", "--bare", "-b", "master", dir)
	return dir
}

// seedRemote pushes files (relative path -> content) to remote as its
// initial commit, using a throwaway clone so the remote is never written
// to directly.
func seedRemote(t *testing.T, remote string, files map[string]string) {
	t.Helper()
	clone := t.TempDir()
	runGit(t, "", "clone", remote, clone)
	for rel, content := range files {
		full := filepath.Join(clone, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		return
	}
	runGit(t, clone, "add", "-A")
	runGit(t, clone, "-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "-m", "seed")
	runGit(t, clone, "push", "origin", "HEAD:master")
}

// runGit runs git directly (not through GitRunner) to set up test
// fixtures; failures fail the test immediately since a broken fixture
// makes every assertion downstream meaningless.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// writeLocalWiki populates a fresh directory (not under git) with files,
// standing in for the repository's .github/wiki/.
func writeLocalWiki(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// mkdirTempFor returns a MkdirTemp seam backed by t.TempDir(), so
// Sync/CheckDrift's isolated clone directory is one the testing package
// itself cleans up rather than a real system temp directory.
func mkdirTempFor(t *testing.T) func() (string, func(), error) {
	return func() (string, func(), error) {
		return t.TempDir(), func() {}, nil
	}
}

// redirectingRunner wraps the real execRunner and rewrites a `clone`
// call's URL argument from want to a local path, when it matches exactly.
// Every other argument, and every non-clone command, passes through
// unmodified to the real git binary. This is what lets Sync and
// CheckDrift's PRODUCTION resolveWikiURL run unmodified in a test while
// no byte reaches github.com: the URL it builds is real, and only the
// argument this double substitutes ever changes.
//
// It also RECORDS every call's argv and env (calls/envs), so a D3 test
// can assert the token never appears in argv while confirming it DOES
// reach the auth env gitAuthEnv builds.
type redirectingRunner struct {
	want  string
	local string

	mu    sync.Mutex
	calls [][]string
	envs  [][]string
}

func (r *redirectingRunner) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, []byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{}, args...))
	r.envs = append(r.envs, append([]string{}, env...))
	r.mu.Unlock()

	rewritten := make([]string, len(args))
	copy(rewritten, args)
	if len(rewritten) >= 2 && rewritten[0] == "clone" && rewritten[1] == r.want {
		rewritten[1] = r.local
	}
	return execRunner{}.Run(ctx, dir, env, rewritten...)
}

// fakeChecker is a scripted VisibilityChecker.
type fakeChecker struct {
	private bool
	err     error
}

func (f fakeChecker) IsPrivate(context.Context, string, string) (bool, error) {
	return f.private, f.err
}

// fakeLocator forces the git-absent path regardless of what this machine
// actually has on PATH.
type fakeLocator struct{}

func (fakeLocator) Lookup(string) (string, error) { return "", &gitAbsentError{GOOS: "test"} }
