// Purpose (this file): the checkpoint's refusal gates and its binding to
// the commit tree: the lease-epoch fence with its attention item, the
// sensitivity floor, the tree the run is bound to, and the hardened git
// invocation every controller-side git read goes through.
//
// Inputs: a JobRef, the injected Fence, StoredSensitivity, Snapshot and
// AttentionPusher, and a git binary on PATH.
// Outputs: nil or a typed refusal (jobs.ErrLeaseFenced passed through
// unchanged, ErrSensitivityLowered, ErrTreeHashMismatch, a scope item).
// Constraints (R18 B1): the tree a run is bound to is
// `git rev-parse <CheckpointCommit>^{tree}` read from the object store,
// never the worktree index or live HEAD. jobs.WorktreeManager.Snapshot
// hashes the index, which may hold edits staged after the commit, so its
// tree is used ONLY for a declared-untracked (stream-only) snapshot, and
// only after proving it differs from the commit tree by declared
// untracked paths alone. Controller git runs without the user's global
// config and hooks (core.hooksPath and fsmonitor are disabled).
// SPORT: internal.ci.fence/ADDED (P1-CI-01).

package ci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// objectIDPattern is the safe-argv shape of a commit or tree id.
var objectIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// targetPattern is the safe-argv shape of one affected target (a Go import
// path or a relative path) before it is placed on a command line.
var targetPattern = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)

// errChainHas reports whether target (by identity, not by Kind) is in err's
// chain, through single and joined unwrapping.
func errChainHas(err, target error) bool {
	if err == nil {
		return false
	}
	if err == target {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return errChainHas(u.Unwrap(), target)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if errChainHas(e, target) {
				return true
			}
		}
	}
	return false
}

// raiseAttention pushes one deduplicated attention item for a refused
// checkpoint and returns the original error, joined with the push failure
// if the item could not be queued.
func (d *Dispatcher) raiseAttention(ctx context.Context, tag string, ref JobRef, detail string, cause error) error {
	item := supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: fmt.Sprintf("ci-stream:%s:%s:%s", tag, ref.JobID, detail),
		ScopeRef:  supervision.ScopeRef{Kind: scope.ScopeKindGlobal},
	}
	if _, err := d.deps.Attention.Push(ctx, item); err != nil {
		return errors.Join(cause, cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: raising the attention item"))
	}
	return cause
}

// fence presents the lease epoch to the injected Fence. A stale epoch
// returns the fence's own jobs.ErrLeaseFenced error and raises one
// attention item; nothing is written before this passes.
func (d *Dispatcher) fence(ctx context.Context, ref JobRef) error {
	err := d.deps.Fence(ctx, ref.Lease.RepoID, ref.Lease.ScopeGlob, ref.LeaseEpoch)
	if err == nil {
		return nil
	}
	if errChainHas(err, jobs.ErrLeaseFenced) {
		return d.raiseAttention(ctx, "fence", ref, fmt.Sprintf("%s:%d", ref.Lease.ScopeGlob, ref.LeaseEpoch), err)
	}
	return err
}

// checkSensitivity refuses a ref whose tier is less restrictive than the
// tier stored for the job. An unreadable stored tier refuses too.
func (d *Dispatcher) checkSensitivity(ctx context.Context, ref JobRef) error {
	stored, err := d.deps.StoredSensitivity(ctx, ref.JobID)
	if err != nil {
		return err
	}
	if !ref.Sensitivity.Valid() || stored.MoreRestrictiveThan(ref.Sensitivity) {
		return cascade.Wrapf(cascade.KindPolicyDenied, ErrSensitivityLowered,
			"ci: stream: job %s carries tier %s, stored tier is %s", ref.JobID, ref.Sensitivity, stored)
	}
	return nil
}

// validateRef checks the fields every checkpoint needs.
func validateRef(ref JobRef) error {
	switch {
	case ref.JobID == "":
		return cascade.New(cascade.KindInvalidInput, "ci: stream: JobRef requires a JobID")
	case ref.RepoRoot == "":
		return cascade.New(cascade.KindInvalidInput, "ci: stream: JobRef requires a RepoRoot")
	case ref.Lease.RepoID == "" || ref.Lease.ScopeGlob == "":
		return cascade.New(cascade.KindInvalidInput, "ci: stream: JobRef requires a Lease")
	case riskRankOf(ref.PlannedRisk) > riskRankOf(jobs.RiskClassCritical):
		return cascade.Newf(cascade.KindInvalidInput, "ci: stream: planned risk %q is not a risk class", ref.PlannedRisk)
	case !objectIDPattern.MatchString(ref.CheckpointCommit):
		return cascade.Newf(cascade.KindInvalidInput, "ci: stream: checkpoint commit %q is not a commit id", ref.CheckpointCommit)
	}
	return nil
}

// bindTree returns the tree hash the checkpoint is bound to (see the file
// header). snapTree is the index tree jobs.WorktreeManager.Snapshot hashed.
func (d *Dispatcher) bindTree(ctx context.Context, ref JobRef, snapTree string) (string, error) {
	commitTree, err := streamGit(ctx, ref.RepoRoot, "rev-parse", "--verify", ref.CheckpointCommit+"^{tree}")
	if err != nil {
		return "", cascade.Wrapf(cascade.KindIntegrity, err, "ci: stream: checkpoint commit %s has no readable tree", ref.CheckpointCommit)
	}
	commitTree = strings.TrimSpace(commitTree)
	if len(ref.Untracked) == 0 {
		return commitTree, nil
	}
	if !objectIDPattern.MatchString(snapTree) {
		return "", cascade.Wrap(cascade.KindIntegrity, ErrTreeHashMismatch, "ci: stream: snapshot returned no tree hash")
	}
	paths, err := treeDiffPaths(ctx, ref.RepoRoot, commitTree, snapTree)
	if err != nil {
		return "", err
	}
	for _, p := range paths {
		if !declaredUntracked(ref.Untracked, p) {
			return "", cascade.Wrapf(cascade.KindIntegrity, ErrTreeHashMismatch,
				"ci: stream: snapshot differs from commit %s at %q, which is not a declared untracked path", ref.CheckpointCommit, p)
		}
	}
	return snapTree, nil
}

// declaredUntracked reports whether path p is one of the declared
// untracked paths or lies under a declared directory.
func declaredUntracked(declared []string, p string) bool {
	for _, u := range declared {
		u = strings.TrimSuffix(u, "/")
		if p == u || strings.HasPrefix(p, u+"/") {
			return true
		}
	}
	return false
}

// treeDiffPaths lists the paths that differ between two tree-ish values.
func treeDiffPaths(ctx context.Context, repoRoot, a, b string) ([]string, error) {
	out, err := streamGit(ctx, repoRoot, "diff-tree", "-r", "--name-only", "--no-renames", "-z", a, b)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "ci: stream: diffing the snapshot against the commit tree")
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// streamGit runs one controller-side git command in dir and returns its
// stdout. The user's global config and hooks never apply.
func streamGit(ctx context.Context, dir string, args ...string) (string, error) {
	return streamGitEnv(ctx, dir, nil, args...)
}

// streamGitEnv is streamGit with extra environment entries (for example a
// private GIT_INDEX_FILE).
func streamGitEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s in %q: %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
