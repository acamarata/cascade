// Purpose: bind acceptance CI to the current immutable checkpoint and cache
// its terminal verdict. Inputs: a recorded attempt and requirement model.
// Outputs: AcceptanceResult. Constraints: no acceptance of stream-only trees.
// SPORT: internal.ci.Dispatcher/AcceptanceRerun (P1-CI-02).

package ci

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

const acceptanceCompleted events.EventKind = "ci.acceptance.completed"

// AcceptanceResult records the exact checkpoint and all acceptance outcomes.
type AcceptanceResult struct {
	CheckpointID, TreeHash string
	Passed                 bool
	Results                []CIResultEvent
}

// AcceptanceRerun synchronously runs acceptance against the recorded commit.
// Concurrent calls are refused and completed results are returned unchanged.
func (d *Dispatcher) AcceptanceRerun(ctx context.Context, ref JobRef, model RequirementModel) (AcceptanceResult, error) {
	snap, err := d.acceptanceSnapshot(ctx, ref)
	if err != nil {
		return AcceptanceResult{}, err
	}
	key := "acceptance|" + snap.CheckpointID
	if !d.begin(key) {
		return AcceptanceResult{}, ErrAlreadyRunning
	}
	defer d.end(key)
	if err := d.fence(ctx, ref); err != nil {
		return AcceptanceResult{}, err
	}
	if err := d.checkSensitivity(ctx, ref); err != nil {
		return AcceptanceResult{}, err
	}
	ref.PlannedRisk, err = d.plannedRisk(ctx, ref, leaseKey(ref.Lease))
	if err != nil {
		return AcceptanceResult{}, err
	}
	if err := d.requireCurrent(ctx, snap.AttemptID); err != nil {
		return AcceptanceResult{}, err
	}
	if cached, found, err := d.cachedAcceptance(ctx, snap); err != nil || found {
		return cached, err
	}
	plan := acceptancePlan(ctx, ref, model, snap)
	ref.Untracked = nil
	results, err := d.Dispatch(ctx, ref, snap, plan, true)
	if err != nil {
		return AcceptanceResult{}, err
	}
	result := acceptanceResult(snap, results)
	return result, d.cacheAcceptance(ctx, result)
}

// acceptanceSnapshot checks the stored commit binding before any dispatch.
func (d *Dispatcher) acceptanceSnapshot(ctx context.Context, ref JobRef) (CandidateSnapshot, error) {
	snap, found, err := d.CurrentCheckpoint(ctx, ref.JobID)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	if !found || ref.CheckpointCommit == "" {
		return CandidateSnapshot{}, ErrCheckpointStale
	}
	attempt, found, err := d.getAttempt(ctx, snap.AttemptID)
	if err != nil {
		return CandidateSnapshot{}, err
	}
	if !found || len(attempt.Entries) == 0 || attempt.Entries[0].Ref.CheckpointCommit != ref.CheckpointCommit {
		return CandidateSnapshot{}, ErrCheckpointStale
	}
	tree, err := acceptanceCommitTree(ctx, ref.RepoRoot, ref.CheckpointCommit)
	if err != nil {
		return CandidateSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: acceptance: resolve checkpoint tree")
	}
	if snap.StreamOnly() || snap.TreeHash != tree || snap.CheckpointID != CheckpointIDFor(ref.JobID, ref.CheckpointCommit, tree) {
		return CandidateSnapshot{}, ErrTreeHashMismatch
	}
	return snap, nil
}

// acceptancePlan reads selection inputs only from the checkpoint checkout.
// Any unavailable selection information forces every requirement kind.
func acceptancePlan(ctx context.Context, ref JobRef, model RequirementModel, snap CandidateSnapshot) CIRequirementPlan {
	full := CIRequirementPlan{Requirement: AllRequired, Targets: []Target{TargetAll},
		Selection: TargetSelectionFull, CandidateTreeHash: snap.TreeHash, RiskClass: string(ref.PlannedRisk)}
	if riskRankOf(ref.PlannedRisk) >= riskRankOf(jobs.RiskClassHigh) {
		return full
	}
	changed, err := ChangedPathsTree(ctx, ref.RepoRoot, ref.BaseCommit, snap.TreeHash)
	if err != nil {
		return full
	}
	checkout, cleanup, err := checkoutCommit(ctx, ref.RepoRoot, ref.CheckpointCommit)
	if err != nil {
		return full
	}
	defer cleanup()
	model.WorktreeRoot = checkout
	plan, err := SelectTargets(ctx, model, changed, snap.TreeHash, string(ref.PlannedRisk))
	if err != nil || plan.Selection.Resolved() == TargetSelectionFull {
		return full
	}
	plan.Requirement, err = requirementForRisk(ref.PlannedRisk)
	if err != nil {
		return full
	}
	return plan
}

// cachedAcceptance reads the verdict published only after every kind completed.
func (d *Dispatcher) cachedAcceptance(ctx context.Context, snap CandidateSnapshot) (AcceptanceResult, bool, error) {
	events, err := d.deps.Bus.Replay(ctx, EventNamespace, 0)
	if err != nil {
		return AcceptanceResult{}, false, err
	}
	for _, event := range events {
		if event.Kind != acceptanceCompleted {
			continue
		}
		var result AcceptanceResult
		if err := json.Unmarshal(event.Payload, &result); err != nil {
			return AcceptanceResult{}, false, cascade.Wrap(cascade.KindIntegrity, err, "ci: decode cached acceptance")
		}
		if result.CheckpointID == snap.CheckpointID && result.TreeHash == snap.TreeHash {
			return result, true, nil
		}
	}
	return AcceptanceResult{}, false, nil
}

// cacheAcceptance persists the complete verdict, including executor provenance.
func (d *Dispatcher) cacheAcceptance(ctx context.Context, result AcceptanceResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "ci: encode acceptance verdict")
	}
	_, err = d.deps.Bus.Publish(ctx, EventNamespace, acceptanceCompleted, streamEventSource, payload)
	return err
}

func acceptanceResult(snap CandidateSnapshot, results []CIResultEvent) AcceptanceResult {
	result := AcceptanceResult{CheckpointID: snap.CheckpointID, TreeHash: snap.TreeHash, Passed: len(results) > 0, Results: results}
	for _, event := range results {
		result.Passed = result.Passed && event.Passed
	}
	return result
}

// acceptanceCommitTree resolves a commit's tree. It is a package variable
// only so tests in a sandbox that forbids running git can stand in for it.
var acceptanceCommitTree = gitTreeOfCommit

func gitTreeOfCommit(ctx context.Context, repo, commit string) (string, error) {
	out, err := streamGit(ctx, repo, "rev-parse", "--verify", commit+"^{tree}")
	return strings.TrimSpace(out), err
}
