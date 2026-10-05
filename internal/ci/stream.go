// Purpose (this file): the streaming CI Dispatcher (contract:ci-checkpoint-
// stream): NewDispatcher, Checkpoint -- the fenced, snapshot-bound entry
// point a leased job calls at each commit -- OnTerminal and
// CurrentCheckpoint. Dispatch, Resume and the local executor are siblings
// (stream_dispatch.go, stream_resume.go, stream_local.go).
//
// Inputs: a JobRef (the lease, its epoch, the checkpoint commit and the
// diff base) and the RequirementModel the repository's stack defines.
// Outputs: a CandidateSnapshot, or the typed refusal that stopped the
// checkpoint before any effect: jobs.ErrLeaseFenced, ErrSensitivityLowered,
// ErrScopeViolation, ErrTreeHashMismatch, ErrCheckpointStale.
// Constraints: the order of Checkpoint is the contract -- Fence, then the
// sensitivity floor, then Snapshot, then CheckpointIDFor, then the
// previous attempt is tombstoned, then ChangedPathsTree over the captured
// tree, then jobs.Reclassify (risk only rises), then the scope check, then
// SelectTargets over a detached checkout of the checkpoint commit (so
// Affected, affected_cmd and the freshness check read only the commit
// tree), then one outbox intent per required kind and the sub-jobs. A
// stale fence writes nothing at all. No second snapshot, outbox, fence or
// risk classifier exists here: every one is the internal/jobs original.
// SPORT: internal.ci.Dispatcher/ADDED, internal.ci.NewDispatcher/ADDED,
//
//	internal.ci.Dispatcher.Checkpoint/ADDED,
//	internal.ci.Dispatcher.OnTerminal/ADDED,
//	internal.ci.Dispatcher.CurrentCheckpoint/ADDED (P1-CI-01).

package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Dispatcher dispatches a checkpoint's requirement kinds as sub-jobs. The
// zero value is not usable; construct with NewDispatcher.
type Dispatcher struct {
	deps DispatcherDeps

	mu       sync.Mutex
	inflight map[string]struct{}

	cbMu      sync.Mutex
	terminals []func(CIResultEvent)
}

// NewDispatcher validates d (every field is required, the error names the
// missing one) and returns a Dispatcher.
func NewDispatcher(d DispatcherDeps) (*Dispatcher, error) {
	missing := ""
	switch {
	case d.CIDB == nil:
		missing = "CIDB"
	case d.JobsDB == nil:
		missing = "JobsDB"
	case d.Snapshot == nil:
		missing = "Snapshot"
	case d.Fence == nil:
		missing = "Fence"
	case d.StoredSensitivity == nil:
		missing = "StoredSensitivity"
	case d.Executor == nil:
		missing = "Executor"
	case d.Bus == nil:
		missing = "Bus"
	case d.Attention == nil:
		missing = "Attention"
	case d.Clock == nil:
		missing = "Clock"
	}
	if missing != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "ci: NewDispatcher requires a non-nil %s", missing)
	}
	return &Dispatcher{deps: d, inflight: map[string]struct{}{}}, nil
}

// OnTerminal registers fn, called once per terminal sub-job after its
// ci_run row has committed. Calls are serialised; fn must not call back
// into the Dispatcher synchronously.
func (d *Dispatcher) OnTerminal(fn func(CIResultEvent)) {
	d.cbMu.Lock()
	defer d.cbMu.Unlock()
	d.terminals = append(d.terminals, fn)
}

// CurrentCheckpoint returns the job's current (not tombstoned) checkpoint.
func (d *Dispatcher) CurrentCheckpoint(ctx context.Context, jobID string) (CandidateSnapshot, bool, error) {
	rows, err := queryAttempts(ctx, d.deps.CIDB,
		`WHERE job_id = ? AND state != ? ORDER BY created_at DESC, attempt_id DESC LIMIT 1`, jobID, attemptTombstoned)
	if err != nil || len(rows) == 0 {
		return CandidateSnapshot{}, false, err
	}
	snap := CandidateSnapshot{TreeHash: rows[0].TreeHash, CheckpointID: rows[0].CheckpointID, AttemptID: rows[0].AttemptID}
	if n := len(rows[0].Entries); n > 0 {
		snap.Untracked = rows[0].Entries[n-1].Snapshot.Untracked
	}
	return snap, true, nil
}

// Checkpoint is the streaming entry point: see the file header for the
// order of its gates. It returns once every dispatched sub-job is terminal;
// a failing CI run is a terminal result, not an error.
func (d *Dispatcher) Checkpoint(ctx context.Context, ref JobRef, model RequirementModel) (CandidateSnapshot, error) {
	if err := validateRef(ref); err != nil {
		return CandidateSnapshot{}, err
	}
	if err := d.fence(ctx, ref); err != nil {
		return CandidateSnapshot{}, err
	}
	if err := d.checkSensitivity(ctx, ref); err != nil {
		return CandidateSnapshot{}, err
	}
	snapRes, err := d.deps.Snapshot(ctx, d.deps.Fence, ref.Lease, ref.LeaseEpoch, ref.Untracked)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	tree, err := d.bindTree(ctx, ref, snapRes.TreeHash)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	ckID := CheckpointIDFor(ref.JobID, ref.CheckpointCommit, tree)
	lease := leaseKey(ref.Lease)
	if err := d.refuseSuperseded(ctx, ckID); err != nil {
		return CandidateSnapshot{}, err
	}
	planned, err := d.plannedRisk(ctx, ref, lease)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	if err := d.tombstoneOthers(ctx, lease, ckID); err != nil {
		return CandidateSnapshot{}, err
	}
	plan, derived, err := d.plan(ctx, ref, model, tree, ckID, planned)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	snap := CandidateSnapshot{TreeHash: tree, CheckpointID: ckID, AttemptID: attemptIDFor(ref.AttemptID, ckID, derived)}
	if len(ref.Untracked) > 0 {
		snap.Untracked = append([]string(nil), ref.Untracked...)
	}
	if done, err := d.openOrRepeat(ctx, ref, snap, lease); err != nil || done {
		return snap, err
	}
	if _, err := d.Dispatch(ctx, ref, snap, plan, false); err != nil {
		return snap, err
	}
	return snap, nil
}

// openOrRepeat opens snap's attempt. A repeat of a checkpoint whose attempt
// already exists, is current and has recorded a dispatch reports done
// (nothing is dispatched twice); an attempt with no dispatch entry is a
// crash between openAttempt and appendEntry, so it is not done and the
// caller dispatches it (appendEntry and the outbox dedup). A repeat of a
// superseded one is ErrCheckpointStale.
func (d *Dispatcher) openOrRepeat(ctx context.Context, ref JobRef, snap CandidateSnapshot, lease string) (bool, error) {
	existing, ok, err := d.getAttempt(ctx, snap.AttemptID)
	if err != nil {
		return false, err
	}
	if ok {
		if existing.State == attemptTombstoned {
			return false, cascade.Wrapf(cascade.KindConflict, ErrCheckpointStale,
				"ci: stream: checkpoint %s of job %s was superseded", snap.CheckpointID, ref.JobID)
		}
		return len(existing.Entries) > 0, nil
	}
	return false, d.openAttempt(ctx, attemptRow{
		AttemptID: snap.AttemptID, JobID: ref.JobID, LeaseID: lease, CheckpointID: snap.CheckpointID, TreeHash: snap.TreeHash,
	})
}

// attemptIDFor is hex(sha256(refAttempt|checkpoint|risk)): a new checkpoint
// or a risk rise opens a new attempt, a repeat of either maps to the same.
func attemptIDFor(refAttempt, checkpointID string, risk jobs.RiskClass) string {
	sum := sha256.Sum256([]byte(refAttempt + "|" + checkpointID + "|" + string(risk)))
	return hex.EncodeToString(sum[:])
}

// riskRankOf orders the four risk classes; an unknown class ranks above
// every known one so it can never lower the planned class.
func riskRankOf(c jobs.RiskClass) int {
	switch c {
	case jobs.RiskClassLow:
		return 0
	case jobs.RiskClassNormal:
		return 1
	case jobs.RiskClassHigh:
		return 2
	case jobs.RiskClassCritical:
		return 3
	}
	return 4
}

// plannedRisk is the class the reclassification starts from: the larger of
// the ref's planned class and the class the lease's current attempt was
// dispatched under, so a caller passing a stale lower class can never lower
// the monotonic risk (R-21.146).
func (d *Dispatcher) plannedRisk(ctx context.Context, ref JobRef, lease string) (jobs.RiskClass, error) {
	planned := ref.PlannedRisk
	prior, ok, err := d.currentForLease(ctx, lease)
	if err != nil || !ok || len(prior.Entries) == 0 {
		return planned, err
	}
	if last := jobs.RiskClass(prior.Entries[len(prior.Entries)-1].Risk); riskRankOf(last) > riskRankOf(planned) {
		planned = last
	}
	return planned, nil
}

// plan computes the checkpoint's changed paths from the captured tree,
// reclassifies risk over them, enforces the lease scope and selects the
// targets, all against a detached checkout of the checkpoint commit that is
// removed afterwards. It returns the plan and the (never lower) risk class.
func (d *Dispatcher) plan(ctx context.Context, ref JobRef, model RequirementModel, tree, ckID string,
	planned jobs.RiskClass) (CIRequirementPlan, jobs.RiskClass, error) {
	changed, err := ChangedPathsTree(ctx, ref.RepoRoot, ref.BaseCommit, tree)
	if err != nil {
		return CIRequirementPlan{}, "", err
	}
	checkout, cleanup, err := checkoutCommit(ctx, ref.RepoRoot, ref.CheckpointCommit)
	if err != nil {
		return CIRequirementPlan{}, "", err
	}
	defer cleanup()
	derived, _, outOfScope, err := jobs.Reclassify(ctx, planned, jobs.ChangeFootprint{ChangedPaths: changed}, ref.ScopePrefixes, checkout)
	if err != nil {
		return CIRequirementPlan{}, "", err
	}
	if len(outOfScope) > 0 {
		cause := cascade.Wrapf(cascade.KindPolicyDenied, ErrScopeViolation,
			"ci: stream: %s is outside the lease scope %v", strings.Join(outOfScope, ", "), ref.ScopePrefixes)
		return CIRequirementPlan{}, "", d.raiseAttention(ctx, "scope", ref, ckID, cause)
	}
	model.WorktreeRoot = checkout
	plan, err := SelectTargets(ctx, model, changed, tree, string(derived))
	if err != nil {
		return CIRequirementPlan{}, "", err
	}
	if plan.Requirement, err = requirementForRisk(derived); err != nil {
		return CIRequirementPlan{}, "", err
	}
	return plan, derived, nil
}

// checkoutCommit makes a detached checkout of commit under a fresh temp
// directory (`git worktree add --detach`, hooks disabled) and returns its
// path with a cleanup that removes the worktree registration and the
// directory.
func checkoutCommit(ctx context.Context, repoRoot, commit string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "cascade-ci-sel-")
	if err != nil {
		return "", func() {}, cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: creating the selection checkout directory")
	}
	dir := filepath.Join(tmp, "sel")
	cleanup := func() {
		_, _ = streamGit(context.WithoutCancel(ctx), repoRoot, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(tmp)
	}
	if _, err := streamGit(ctx, repoRoot, "worktree", "add", "--detach", dir, commit); err != nil {
		cleanup()
		return "", func() {}, cascade.Wrapf(cascade.KindUnavailable, err, "ci: stream: checking out commit %s", commit)
	}
	return dir, cleanup, nil
}
