//go:build integration

// Purpose: Art.2's real-counterpart proof for the git half of remote
//   dispatch — the whole ship cycle driven against REAL git repositories:
//   a real bare remote, real refs, a real worktree lifecycle. Git is an
//   external contract this repo does not control, so it is never exercised
//   against a dialect this package invented for itself.
//
//   The default lane's dispatch_git_test.go covers each git LEG in
//   isolation; what is here is the end-to-end flow those legs compose
//   into, including a retry after a dropped attempt, asserted on real refs
//   and real directories.
//
//   Tagged `integration` so it rides the same lane as its sshd sibling,
//   though it needs no service container: ci.yml runs it in a BLOCKING job
//   (node-dispatch-real-git) because git is on every runner.
//
//   Reuses dispatch_git_test.go's newGitRepo/testGitRunner (same package).
// SPORT: internal/nodes TestDispatchRealGit (P1-E17-W4-S37-T2).

package nodes

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// bareRemote initialises a real bare repository to push to and fetch from.
func bareRemote(t *testing.T) string {
	t.Helper()
	remote := t.TempDir()
	if _, err := (testGitRunner{}).Run(context.Background(), remote, "init", "--bare"); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	return remote
}

// remoteHasRef reports whether the bare remote carries ref.
func remoteHasRef(t *testing.T, remote, ref string) bool {
	t.Helper()
	out, err := (testGitRunner{}).Run(context.Background(), remote, "for-each-ref", "--format=%(refname)")
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	return strings.Contains(out, ref)
}

// realGitDispatch runs one whole dispatch against real repositories and
// returns the outcome plus the attempt the node answered.
func realGitDispatch(t *testing.T, deps ShipDeps, rec DeviceRecord, head string) (ShipOutcome, error) {
	t.Helper()
	return Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID:  "d-real",
		Head:        head,
		Work:        Action{ID: "a-real"},
		Sensitivity: SensitivityNormal,
	})
}

// TestDispatchRealGit drives the complete ship cycle against real git: the
// work branch is pushed to a real bare remote under the attempt's own ref,
// the node's results are fetched back, and the worktree the dispatch
// created is gone on the terminal outcome.
func TestDispatchRealGit(t *testing.T) {
	root, head := newGitRepo(t)
	remote := bareRemote(t)
	rec, priv := enrolledDispatchNode(t)
	caller := &recordingCaller{
		frame:  DispatchFrame{Sequence: 1, ActionID: "a-real", Outcome: OutcomeSucceeded},
		signer: priv,
		rec:    rec,
	}
	deps := ShipDeps{
		Git:       newDispatchGit(root, testGitRunner{}),
		Caller:    caller,
		Attempts:  NewAttemptRegister(),
		Sequences: NewSequenceStore(),
		Remote:    remote,
	}

	outcome, err := realGitDispatch(t, deps, rec, head)
	if err != nil {
		t.Fatalf("Ship over real git: %v", err)
	}
	if outcome.Commit != head {
		t.Errorf("result commit = %q, want the commit the node pushed (%q)", outcome.Commit, head)
	}
	if outcome.Attempt == 0 {
		t.Fatal("the outcome reports no attempt")
	}

	// The branch exists on the REAL remote, under the attempt's own ref.
	wantRef := "refs/heads/" + DispatchBranch("d-real", outcome.Attempt)
	if !remoteHasRef(t, remote, wantRef) {
		t.Errorf("%s is not on the remote; the work was never really pushed", wantRef)
	}
	// And the worktree is gone: a dispatch that leaves directories behind
	// fills the operator's checkout one attempt at a time.
	worktree := DispatchWorktreeDir(root, "d-real", outcome.Attempt)
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree %s survived the terminal outcome (stat err = %v)", worktree, err)
	}
}

// TestDispatchRealGit_ARetryAfterFailureGetsItsOwnRef proves fencing is
// real on disk, not just in the register. The scenario is the one fencing
// exists for: the first attempt does NOT reach a terminal outcome (so the
// controller cannot know whether the node is dead or merely partitioned),
// the controller retries, and the retry must land on a DIFFERENT ref — or
// a partitioned node still pushing to the old branch would be writing into
// the live attempt's work.
//
// Note on the success path: Ship forgets a dispatch's attempt on a TERMINAL
// outcome, so a fresh dispatch reusing a completed dispatch id restarts at
// attempt 1 and reuses its ref. That is bounded-map hygiene, and it is safe
// only because a dispatch id names one dispatch; reusing a completed id is
// a caller error, not a fencing hole. Recorded in the S-37 journal.
func TestDispatchRealGit_ARetryAfterFailureGetsItsOwnRef(t *testing.T) {
	root, head := newGitRepo(t)
	remote := bareRemote(t)
	rec, priv := enrolledDispatchNode(t)
	register := NewAttemptRegister()
	// The first attempt drops mid-flight: the branch is pushed, the node
	// never answers. The attempt is NOT forgotten, which is what makes the
	// retry a second attempt rather than a repeat of the first.
	dropped := &recordingCaller{err: errTunnelDroppedForTest, signer: priv, rec: rec}
	deps := ShipDeps{
		Git:       newDispatchGit(root, testGitRunner{}),
		Caller:    dropped,
		Attempts:  register,
		Sequences: NewSequenceStore(),
		Remote:    remote,
	}
	if _, err := realGitDispatch(t, deps, rec, head); err == nil {
		t.Fatal("a dropped node call reported success")
	}
	firstAttempt := register.Current("d-real")
	if firstAttempt == 0 {
		t.Fatal("the in-flight attempt was forgotten, so a retry cannot fence against it")
	}

	deps.Caller = &recordingCaller{frame: DispatchFrame{Sequence: 2, ActionID: "a-real", Outcome: OutcomeSucceeded}, signer: priv, rec: rec}
	second, err := realGitDispatch(t, deps, rec, head)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	firstRef := "refs/heads/" + DispatchBranch("d-real", firstAttempt)
	secondRef := "refs/heads/" + DispatchBranch("d-real", second.Attempt)
	if firstRef == secondRef {
		t.Fatalf("the retry reused %s; a partitioned node could push into the live attempt", firstRef)
	}
	for _, ref := range []string{firstRef, secondRef} {
		if !remoteHasRef(t, remote, ref) {
			t.Errorf("%s is not on the remote", ref)
		}
	}
}

// errTunnelDroppedForTest stands in for a node call that never answered.
var errTunnelDroppedForTest = errors.New("connection reset by peer")
