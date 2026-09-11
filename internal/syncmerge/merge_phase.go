//go:build spike

package syncmerge

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PhaseMergeResult reports the outcome of a fetch+fast-forward-only phase
// state carriage attempt (R-21.223 domain 3).
type PhaseMergeResult struct {
	FastForwarded bool
	ResultRef     string // the ref HEAD points to after a successful fast-forward
	LocalRef      string // journaled on divergence
	RemoteRef     string // journaled on divergence
}

// runGit runs a real git subprocess in dir and returns combined output,
// trimmed, and any error. This exercises the Git external contract
// (Art.2): no hand-rolled merge logic, the real git binary decides
// fast-forward eligibility.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// phaseMergeGit fetches remoteRef from remoteRepo into localRepo (as
// refs/remotes/spike/<remoteRef>) and attempts a fast-forward-only merge
// into localRepo's current branch. Phase state is git-tracked YAML and
// carriage is fetch + fast-forward only (R-21.223 domain 3): a
// non-fast-forward or conflicting merge is NEVER resolved by this
// function -- it returns ErrPhaseStateDiverged, and the caller journals
// both refs and surfaces an operator attention item.
func phaseMergeGit(ctx context.Context, localRepo, remoteRepo, remoteRef string) (PhaseMergeResult, error) {
	remoteName := "spike-remote"
	if _, err := runGit(ctx, localRepo, "remote", "add", remoteName, remoteRepo); err != nil {
		// remote may already exist across repeated calls in one test; that
		// is not a divergence, just re-point it.
		if _, err2 := runGit(ctx, localRepo, "remote", "set-url", remoteName, remoteRepo); err2 != nil {
			return PhaseMergeResult{}, fmt.Errorf("git remote add/set-url: %w (%v)", err, err2)
		}
	}
	if out, err := runGit(ctx, localRepo, "fetch", remoteName, remoteRef); err != nil {
		return PhaseMergeResult{}, fmt.Errorf("git fetch: %w: %s", err, out)
	}

	localHead, err := runGit(ctx, localRepo, "rev-parse", "HEAD")
	if err != nil {
		return PhaseMergeResult{}, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	remoteHead, err := runGit(ctx, localRepo, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return PhaseMergeResult{}, fmt.Errorf("git rev-parse FETCH_HEAD: %w", err)
	}

	out, mergeErr := runGit(ctx, localRepo, "merge", "--ff-only", "FETCH_HEAD")
	if mergeErr != nil {
		return PhaseMergeResult{LocalRef: localHead, RemoteRef: remoteHead}, fmt.Errorf("%w: local=%s remote=%s: %s", ErrPhaseStateDiverged, localHead, remoteHead, out)
	}

	newHead, err := runGit(ctx, localRepo, "rev-parse", "HEAD")
	if err != nil {
		return PhaseMergeResult{}, fmt.Errorf("git rev-parse HEAD after merge: %w", err)
	}
	return PhaseMergeResult{FastForwarded: true, ResultRef: newHead, LocalRef: localHead, RemoteRef: remoteHead}, nil
}
