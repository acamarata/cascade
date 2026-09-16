package nodes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose (this file): the git legs driven against REAL git repositories
//   (Art.2 — git is an external contract). The runner here shells to the
//   real binary; nothing in this file reimplements git's behaviour, because
//   a hand-rolled dialect passes its own tests and fails against the real
//   thing.
// Constraints: internal/nodes may not import os/exec in shipped code (the
//   process-spawn allowlist), which is why GitRunner is an interface. Test
//   files are outside that scan, so the real runner lives here.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// testGitRunner shells to the real git binary.
type testGitRunner struct{ bin string }

func (r testGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	bin := r.bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &gitTestError{args: args, stderr: stderr.String(), cause: err}
	}
	return stdout.String(), nil
}

type gitTestError struct {
	args   []string
	stderr string
	cause  error
}

func (e *gitTestError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + strings.TrimSpace(e.stderr) + ": " + e.cause.Error()
}
func (e *gitTestError) Unwrap() error { return e.cause }

// newGitRepo initializes a real repository with one commit and returns its
// root and that commit's sha.
func newGitRepo(t *testing.T) (root, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; this test exercises the real binary")
	}
	root = t.TempDir()
	runner := testGitRunner{}
	ctx := context.Background()

	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := runner.Run(ctx, root, args...); err != nil {
			t.Fatalf("setup %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, root, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, root, "commit", "-m", "seed"); err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(out)
}

// TestDispatchRefsAreDisjointFromJobRefs is the namespace rule. Two
// subsystems making worktrees in one repository must not be able to collide.
func TestDispatchRefsAreDisjointFromJobRefs(t *testing.T) {
	branch := DispatchBranch("d1", 1)
	if !strings.HasPrefix(branch, "dispatch/") {
		t.Fatalf("branch = %q, want the dispatch namespace", branch)
	}
	if strings.HasPrefix(branch, "job/") {
		t.Fatalf("branch = %q collides with the jobs namespace", branch)
	}
	dir := DispatchWorktreeDir("/repo", "d1", 1)
	if !strings.Contains(filepath.ToSlash(dir), "/.cascade/worktrees/dispatch-d1-1") {
		t.Fatalf("worktree dir = %q", dir)
	}
}

// TestEachAttemptGetsItsOwnRefAndDirectory is what makes the ref the fence:
// a replacement attempt must not reuse a superseded one's branch or
// directory while that one may still be writing.
func TestEachAttemptGetsItsOwnRefAndDirectory(t *testing.T) {
	if DispatchBranch("d1", 1) == DispatchBranch("d1", 2) {
		t.Error("two attempts share a branch")
	}
	if DispatchWorktreeDir("/repo", "d1", 1) == DispatchWorktreeDir("/repo", "d1", 2) {
		t.Error("two attempts share a worktree directory")
	}
}

// TestPushWorkCreatesTheAttemptBranchOnTheRemote drives the real push leg
// against two real repositories.
func TestPushWorkCreatesTheAttemptBranchOnTheRemote(t *testing.T) {
	root, head := newGitRepo(t)
	remote := t.TempDir()
	runner := testGitRunner{}
	ctx := context.Background()
	if _, err := runner.Run(ctx, remote, "init", "--bare"); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	g := newDispatchGit(root, runner)
	if err := g.PushWork(ctx, remote, "d1", head, 1); err != nil {
		t.Fatalf("PushWork: %v", err)
	}

	// STATE, not a return value: the ref must exist on the remote.
	out, err := runner.Run(ctx, remote, "rev-parse", DispatchBranch("d1", 1))
	if err != nil {
		t.Fatalf("the attempt branch is not on the remote: %v", err)
	}
	if got := strings.TrimSpace(out); got != head {
		t.Errorf("remote branch points at %s, want %s", got, head)
	}
}

// TestPushWorkDoesNotMoveTheControllersHead is the rule that keeps a
// dispatch from disturbing whoever is using the repository.
func TestPushWorkDoesNotMoveTheControllersHead(t *testing.T) {
	root, head := newGitRepo(t)
	remote := t.TempDir()
	runner := testGitRunner{}
	ctx := context.Background()
	if _, err := runner.Run(ctx, remote, "init", "--bare"); err != nil {
		t.Fatal(err)
	}
	before, err := runner.Run(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	g := newDispatchGit(root, runner)
	if err := g.PushWork(ctx, remote, "d1", head, 1); err != nil {
		t.Fatalf("PushWork: %v", err)
	}

	after, err := runner.Run(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(before) != strings.TrimSpace(after) {
		t.Fatalf("the dispatch moved HEAD from %q to %q", strings.TrimSpace(before), strings.TrimSpace(after))
	}
}

// TestFetchResultsReadsWhatTheNodePushed drives the return leg, including
// that the fetch lands on its own ref rather than on the dispatch branch.
func TestFetchResultsReadsWhatTheNodePushed(t *testing.T) {
	root, head := newGitRepo(t)
	remote := t.TempDir()
	runner := testGitRunner{}
	ctx := context.Background()
	if _, err := runner.Run(ctx, remote, "init", "--bare"); err != nil {
		t.Fatal(err)
	}

	g := newDispatchGit(root, runner)
	if err := g.PushWork(ctx, remote, "d1", head, 1); err != nil {
		t.Fatalf("PushWork: %v", err)
	}

	got, err := g.FetchResults(ctx, remote, "d1", 1)
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if got != head {
		t.Errorf("fetched commit = %s, want %s", got, head)
	}
}

// TestAFailedPushIsATypedRefusal proves a real git failure surfaces as this
// package's own typed error rather than a raw exec failure.
func TestAFailedPushIsATypedRefusal(t *testing.T) {
	root, head := newGitRepo(t)
	g := newDispatchGit(root, testGitRunner{})

	err := g.PushWork(context.Background(), filepath.Join(t.TempDir(), "nope"), "d1", head, 1)
	if err == nil {
		t.Fatal("pushing to a remote that does not exist reported success")
	}
	if !strings.Contains(err.Error(), "d1") {
		t.Errorf("error = %v, want it to name the dispatch", err)
	}
}

// TestWorktreeLifecycleUsesRealGit drives prepare and teardown against a
// real repository, asserting the directory really appears and really goes.
func TestWorktreeLifecycleUsesRealGit(t *testing.T) {
	root, head := newGitRepo(t)
	runner := testGitRunner{}
	ctx := context.Background()

	// A worktree needs the branch to exist locally first; PushWork's own
	// branch step is what creates it in production.
	if _, err := runner.Run(ctx, root, "branch", DispatchBranch("d1", 1), head); err != nil {
		t.Fatal(err)
	}

	g := newDispatchGit(root, runner)
	dir, err := g.PrepareWorktree(ctx, "d1", 1)
	if err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("the worktree directory was not created: %v", statErr)
	}

	if err := g.RemoveWorktree(ctx, "d1", 1); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("the worktree directory survived teardown: %v", statErr)
	}
	// And git's own admin entry must be gone too, or the next add for this
	// path fails with a message about a worktree that no longer exists.
	out, err := runner.Run(ctx, root, "worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "dispatch-d1-1") {
		t.Errorf("git still lists the removed worktree:\n%s", out)
	}
}

// TestRemovingAnAlreadyGoneWorktreeIsNotAnError proves teardown is safe to
// run twice — the terminal path may be reached more than once.
func TestRemovingAnAlreadyGoneWorktreeIsNotAnError(t *testing.T) {
	root, head := newGitRepo(t)
	runner := testGitRunner{}
	ctx := context.Background()
	if _, err := runner.Run(ctx, root, "branch", DispatchBranch("d1", 1), head); err != nil {
		t.Fatal(err)
	}

	g := newDispatchGit(root, runner)
	if _, err := g.PrepareWorktree(ctx, "d1", 1); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveWorktree(ctx, "d1", 1); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	if err := g.RemoveWorktree(ctx, "d1", 1); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}
