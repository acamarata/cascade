package nodes

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Purpose (this file): the git legs of a dispatch — the branch and worktree
//
//	naming, the push of the work branch, the fetch of results, and the
//	worktree teardown on a terminal outcome.
//
// Inputs: a repository root, a dispatch id and attempt, a remote.
// Outputs: real refs in a real repository.
// Constraints: REAL git only (Art.2 — git is an external contract), but
//
//	this package SPAWNS NOTHING. os/exec is restricted to a named set of
//	packages and internal/nodes is not one of them, so the runner arrives
//	as an injected interface and the process is started by a package
//	already permitted to start one. That boundary is why this file holds
//	the git SEMANTICS — ref naming, leg order, teardown — and none of the
//	process handling.
//
//	Only the runner is injectable, never git's own behaviour: error-path
//	tests drive real git against a broken repository rather than replacing
//	its semantics with a self-authored double, because a hand-rolled git
//	dialect passes its own tests and fails against the real thing.
//
//	The dispatch namespace is deliberately DISJOINT from the R-16.37 jobs
//	namespace: `dispatch/<id>/<attempt>` against `job/<id>`, and
//	`dispatch-<id>-<attempt>` against `job-<id>`. Two subsystems creating
//	worktrees in one repository must not be able to collide on a name, and
//	the attempt suffix is what keeps a replacement attempt from reusing a
//	superseded one's directory while that one may still be writing to it.
//
// SPORT: internal/nodes:dispatch-git (ADD) — P1-E17-W4-S37-T2.

// dispatchWorktreesDir is the shared worktree root segment, matching
// R-16.37's verbatim path.
const dispatchWorktreesDir = ".cascade/worktrees"

// DispatchBranch is the ref a dispatch attempt ships on.
//
// The attempt is part of the ref, not metadata beside it: that is what
// makes fencing a property of git itself rather than of bookkeeping a
// partitioned node could be holding a stale copy of.
func DispatchBranch(dispatchID string, attempt uint64) string {
	return fmt.Sprintf("dispatch/%s/%d", dispatchID, attempt)
}

// DispatchWorktreeDir is the worktree root for one dispatch attempt.
func DispatchWorktreeDir(repoRoot, dispatchID string, attempt uint64) string {
	return filepath.Join(repoRoot, filepath.FromSlash(dispatchWorktreesDir),
		fmt.Sprintf("dispatch-%s-%d", dispatchID, attempt))
}

// GitRunner runs one real git command. It is satisfied by
// internal/jobs.NewGitRunner, the tree's single git-spawning
// implementation; this package declares the need and never spawns.
type GitRunner interface {
	// Run executes git with args in dir and returns its stdout.
	Run(ctx context.Context, dir string, args ...string) (stdout string, err error)
}

// dispatchGit performs the git legs of a dispatch against one repository.
type dispatchGit struct {
	repoRoot string
	git      GitRunner
}

// newDispatchGit builds the git legs over repoRoot.
func newDispatchGit(repoRoot string, git GitRunner) dispatchGit {
	return dispatchGit{repoRoot: repoRoot, git: git}
}

// PushWork creates the attempt's branch at head and pushes it to remote.
//
// The branch is created with `git branch` at an explicit commit rather than
// by checking anything out: the controller's own working tree belongs to
// whoever is using it, and a dispatch must never move their HEAD.
func (g dispatchGit) PushWork(ctx context.Context, remote, dispatchID, head string, attempt uint64) error {
	branch := DispatchBranch(dispatchID, attempt)
	if _, err := g.git.Run(ctx, g.repoRoot, "branch", "--force", branch, head); err != nil {
		return ErrPushFailed(dispatchID, err)
	}
	if _, err := g.git.Run(ctx, g.repoRoot, "push", "--force", remote, branch+":"+branch); err != nil {
		return ErrPushFailed(dispatchID, err)
	}
	return nil
}

// FetchResults fetches the attempt's result branch back from remote and
// reports the commit the node left there.
//
// The fetch is into a ref under refs/dispatch-results/ rather than onto the
// dispatch branch itself: the controller must be able to inspect what came
// back without the act of fetching it changing what was shipped.
func (g dispatchGit) FetchResults(ctx context.Context, remote, dispatchID string, attempt uint64) (string, error) {
	branch := DispatchBranch(dispatchID, attempt)
	local := "refs/dispatch-results/" + dispatchID + "/" + fmt.Sprint(attempt)
	if _, err := g.git.Run(ctx, g.repoRoot, "fetch", "--force", remote, branch+":"+local); err != nil {
		return "", ErrFetchFailed(dispatchID, err)
	}
	out, err := g.git.Run(ctx, g.repoRoot, "rev-parse", local)
	if err != nil {
		return "", ErrFetchFailed(dispatchID, err)
	}
	return strings.TrimSpace(out), nil
}

// PrepareWorktree creates the node-side worktree for an attempt. It is the
// node's half of the model and runs on the node, not the controller.
func (g dispatchGit) PrepareWorktree(ctx context.Context, dispatchID string, attempt uint64) (string, error) {
	dir := DispatchWorktreeDir(g.repoRoot, dispatchID, attempt)
	branch := DispatchBranch(dispatchID, attempt)
	if _, err := g.git.Run(ctx, g.repoRoot, "worktree", "add", "--force", dir, branch); err != nil {
		return "", ErrWorktreeFailed(dispatchID, err)
	}
	return dir, nil
}

// RemoveWorktree tears the attempt's worktree down on a terminal outcome.
//
// The prune afterwards is not optional bookkeeping: `worktree remove`
// leaves git's admin entry behind if the directory is already gone, and a
// stale entry makes the next `worktree add` for the same path fail with a
// message about a worktree that no longer exists.
func (g dispatchGit) RemoveWorktree(ctx context.Context, dispatchID string, attempt uint64) error {
	dir := DispatchWorktreeDir(g.repoRoot, dispatchID, attempt)
	if _, err := g.git.Run(ctx, g.repoRoot, "worktree", "remove", "--force", dir); err != nil {
		if _, pruneErr := g.git.Run(ctx, g.repoRoot, "worktree", "prune"); pruneErr != nil {
			return ErrWorktreeFailed(dispatchID, err)
		}
		return nil
	}
	_, _ = g.git.Run(ctx, g.repoRoot, "worktree", "prune")
	return nil
}
