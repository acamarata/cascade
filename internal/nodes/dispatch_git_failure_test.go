package nodes

// Purpose (this file): the FAILING git legs — a fetch that finds nothing and
//   a worktree that cannot be created. Split from dispatch_git_test.go for
//   the 300-line cap.
// Constraints: real git (Art.2). These drive the error branches
//   P1-E17-W4-S37-T2 names in its acceptance criteria — "failed push/fetch,
//   worktree checkout failure" — which had no test at all: both
//   ErrFetchFailed and ErrWorktreeFailed were at 0% coverage, so nothing
//   proved the refusal was typed rather than a raw exec failure.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestFetchingAnAttemptThatWasNeverPushedIsATypedRefusal covers the first
// half of the fetch leg's failure: the remote has no such branch.
//
// This is the ordinary shape of a lost node — the controller waits, then
// asks for results that are not there — so a raw exec error escaping here
// would reach an operator as git's own text about a refspec.
func TestFetchingAnAttemptThatWasNeverPushedIsATypedRefusal(t *testing.T) {
	root, _ := newGitRepo(t)
	remote := t.TempDir()
	runner := testGitRunner{}
	ctx := context.Background()
	if _, err := runner.Run(ctx, remote, "init", "--bare"); err != nil {
		t.Fatal(err)
	}

	_, err := newDispatchGit(root, runner).FetchResults(ctx, remote, "d-missing", 7)
	if err == nil {
		t.Fatal("fetching an attempt nobody pushed reported success")
	}
	assertDispatchFailure(t, err, "d-missing")
}

// TestFetchingFromAnAbsentRemoteIsATypedRefusal covers the other fetch
// failure: the remote itself is gone, which is what a node that was
// de-provisioned mid-dispatch looks like.
func TestFetchingFromAnAbsentRemoteIsATypedRefusal(t *testing.T) {
	root, _ := newGitRepo(t)

	_, err := newDispatchGit(root, testGitRunner{}).FetchResults(
		context.Background(), filepath.Join(t.TempDir(), "gone"), "d2", 1)
	if err == nil {
		t.Fatal("fetching from a remote that does not exist reported success")
	}
	assertDispatchFailure(t, err, "d2")
}

// TestPreparingAWorktreeForAnUnpushedAttemptIsATypedRefusal drives the
// worktree leg's failure the way it actually happens on a node: the branch
// the attempt names is not in the repository, so `worktree add` refuses.
func TestPreparingAWorktreeForAnUnpushedAttemptIsATypedRefusal(t *testing.T) {
	root, _ := newGitRepo(t)

	dir, err := newDispatchGit(root, testGitRunner{}).PrepareWorktree(
		context.Background(), "d-nobranch", 1)
	if err == nil {
		t.Fatalf("a worktree was created for a branch that does not exist: %s", dir)
	}
	assertDispatchFailure(t, err, "d-nobranch")
	if _, statErr := os.Stat(DispatchWorktreeDir(root, "d-nobranch", 1)); !os.IsNotExist(statErr) {
		t.Errorf("a refused worktree left a directory behind (stat err = %v)", statErr)
	}
}

// TestAWorktreeThatCannotBeRemovedIsATypedRefusal is the teardown half.
//
// RemoveWorktree swallows a removal failure when the follow-up prune
// succeeds, because an already-gone directory is the ordinary case. It must
// NOT swallow one when the prune fails too: that is a repository this
// process cannot tidy, and a silent success there leaves an admin entry
// that makes the NEXT attempt's worktree add fail.
func TestAWorktreeThatCannotBeRemovedIsATypedRefusal(t *testing.T) {
	root, _ := newGitRepo(t)
	g := newDispatchGit(filepath.Join(root, "not-a-repository"), testGitRunner{})

	err := g.RemoveWorktree(context.Background(), "d3", 1)
	if err == nil {
		t.Fatal("removing a worktree from a path that is not a repository reported success")
	}
	assertDispatchFailure(t, err, "d3")
}

// assertDispatchFailure holds every one of these to the same contract: a
// typed Unavailable error that names the dispatch and still carries git's
// own words underneath, so an operator gets both the subsystem's answer and
// the tool's.
func assertDispatchFailure(t *testing.T, err error, dispatchID string) {
	t.Helper()
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindUnavailable)
	}
	if !strings.Contains(err.Error(), dispatchID) {
		t.Errorf("error = %v, want it to name dispatch %s", err, dispatchID)
	}
	if !strings.Contains(err.Error(), "git ") {
		t.Errorf("error = %v, want git's own message carried underneath", err)
	}
}
