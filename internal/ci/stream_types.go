// Purpose (this file): the streaming CI dispatcher's contract types
// (contract:ci-checkpoint-stream) -- JobRef, CandidateSnapshot, SubJob,
// SubJobResult, SubJobExecutor, CIResultEvent, DispatcherDeps,
// ResumeReport, the six stream errors, the two event kinds and
// CheckpointIDFor. Behaviour lives in stream.go, stream_dispatch.go,
// stream_fence.go, stream_attempt.go, stream_outbox.go, stream_resume.go
// and stream_local.go.
//
// Inputs: none at package scope.
// Outputs: plain data types and sentinels. The derived data tier is
// provider.SensitivityTier (contract:sensitivity-tier), never a second
// type (DEBT-ARCH-12).
// Constraints: errors use the frozen 14 cascade Kinds. errors.Is on a
// cascade sentinel compares Kind only, so callers and tests that must
// tell two conflicts apart compare identity and message, never Kind
// alone. jobs.ErrLeaseFenced is never redeclared here: Checkpoint passes
// the fence's own error through.
// SPORT: internal.ci.JobRef/ADDED, internal.ci.CandidateSnapshot/ADDED,
//
//	internal.ci.SubJob/ADDED, internal.ci.SubJobResult/ADDED,
//	internal.ci.SubJobExecutor/ADDED, internal.ci.CIResultEvent/ADDED,
//	internal.ci.DispatcherDeps/ADDED, internal.ci.ResumeReport/ADDED,
//	internal.ci.CheckpointIDFor/ADDED (P1-CI-01).

package ci

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The two event kinds the dispatcher publishes on EventNamespace.
const (
	// EventKindCheckpointDispatched is published once per requirement
	// kind when its sub-job is handed to the SubJobExecutor.
	EventKindCheckpointDispatched events.EventKind = "ci.checkpoint.dispatched"
	// EventKindCheckpointTerminal is published once per terminal sub-job
	// after its ci_run row has committed.
	EventKindCheckpointTerminal events.EventKind = "ci.checkpoint.terminal"
)

// The stream errors. ErrCheckpointStale, ErrAlreadyRunning and
// ErrLateResult are conflicts, ErrScopeViolation and ErrSensitivityLowered
// are policy refusals; ErrTreeHashMismatch is an integrity failure (the
// tree a run was asked to execute is not the tree it was bound to).
var (
	ErrTreeHashMismatch   = cascade.New(cascade.KindIntegrity, "ci: stream: tree hash does not match the checkpoint binding")
	ErrCheckpointStale    = cascade.New(cascade.KindConflict, "ci: stream: checkpoint was superseded")
	ErrAlreadyRunning     = cascade.New(cascade.KindConflict, "ci: stream: sub-job is already running")
	ErrLateResult         = cascade.New(cascade.KindConflict, "ci: stream: result for a tombstoned or superseded attempt")
	ErrScopeViolation     = cascade.New(cascade.KindPolicyDenied, "ci: stream: changed path outside the lease scope")
	ErrSensitivityLowered = cascade.New(cascade.KindPolicyDenied, "ci: stream: job sensitivity is less restrictive than the stored tier")
)

// JobRef names the leased job a checkpoint belongs to. CheckpointCommit is
// the commit the run is bound to (never the live HEAD); BaseCommit is the
// diff base; Untracked lists the explicitly declared untracked paths whose
// content may enter a stream-only snapshot (R-21.147).
type JobRef struct {
	JobID, ProjectID, RepoRoot, WorktreeRoot string
	Lease                                    jobs.ResourceLease
	LeaseEpoch                               int64
	AttemptID                                string
	AttemptGeneration                        int64
	CheckpointCommit, BaseCommit             string
	ScopePrefixes                            []string
	PlannedRisk                              jobs.RiskClass
	Sensitivity                              provider.SensitivityTier
	Untracked                                []string
}

// CandidateSnapshot is the immutable identity of one checkpoint: TreeHash
// is read from the object store (the commit's tree, or the declared-
// untracked tree for a stream-only snapshot), CheckpointID is
// CheckpointIDFor(job, commit, tree) and AttemptID names the attempt the
// checkpoint opened.
type CandidateSnapshot struct {
	TreeHash, CheckpointID, AttemptID string
	Untracked                         []string
}

// StreamOnly reports whether the snapshot carries declared untracked
// paths. Such a snapshot may feed stream runs only: an acceptance dispatch
// refuses it with ErrTreeHashMismatch.
func (s CandidateSnapshot) StreamOnly() bool { return len(s.Untracked) > 0 }

// SubJob is one requirement kind's unit of work against one snapshot.
type SubJob struct {
	Ref        JobRef
	Kind       RequirementKind
	Acceptance bool
	Snapshot   CandidateSnapshot
	Plan       CIRequirementPlan
}

// SubJobResult is what an executor reports for a finished sub-job: the
// ci_run key its rows were written under and whether the run passed.
type SubJobResult struct {
	RunID, RepoID        int64
	Passed               bool
	ExecutorKind, NodeID string
}

// SubJobExecutor runs one sub-job. The local implementation
// (NewLocalSubJobExecutor) runs Execute in a fresh checkout of the
// snapshot tree. An implementation must be idempotent per (attempt, kind):
// Resume re-dispatches an interrupted sub-job and expects one run, not two.
type SubJobExecutor interface {
	Run(ctx context.Context, sj SubJob) (SubJobResult, error)
}

// CIResultEvent is the terminal record of one sub-job: it carries the
// ci_run key (RunID, RepoID) that selects the row, the snapshot identity
// and the sensitivity tier that travelled with the job.
//
//nolint:revive // contract-mandated exported name (contract:ci-checkpoint-stream)
type CIResultEvent struct {
	JobID, CheckpointID, TreeHash string
	Kind                          RequirementKind
	Acceptance, Passed            bool
	RunID, RepoID                 int64
	ExecutorKind, NodeID          string
	Sensitivity                   provider.SensitivityTier
}

// DispatcherDeps carries every input NewDispatcher needs. Every field is
// required; NewDispatcher names the missing one (KindInvalidInput).
type DispatcherDeps struct {
	CIDB, JobsDB *sql.DB
	// Snapshot is (*jobs.WorktreeManager).Snapshot as a method value.
	Snapshot func(ctx context.Context, fence jobs.FenceFunc, l jobs.ResourceLease,
		epoch int64, untracked []string) (jobs.SnapshotResult, error)
	// Fence is (*jobs.LeaseManager).Fence as a method value.
	Fence jobs.FenceFunc
	// StoredSensitivity reads the job's recorded tier; a JobRef may never
	// be less restrictive than it.
	StoredSensitivity func(ctx context.Context, jobID string) (provider.SensitivityTier, error)
	Executor          SubJobExecutor
	Bus               *events.Bus
	Attention         AttentionPusher
	Clock             runtime.Clock
}

// ResumeReport counts what one Resume pass did to the unconfirmed
// ci_dispatch outbox rows.
type ResumeReport struct {
	// Redispatched rows had no effect yet and were dispatched again once.
	Redispatched int
	// Confirmed rows already carried their effect and were only confirmed.
	Confirmed int
	// Dropped rows belonged to a tombstoned attempt and were closed
	// without running anything.
	Dropped int
	// Unmatched rows named no recorded dispatch and were left untouched.
	Unmatched int
}

// CheckpointIDFor is hex(sha256(jobID|commit|tree)): the stable identity of
// one checkpoint. The same job, commit and tree always yield the same id.
func CheckpointIDFor(jobID, commit, tree string) string {
	sum := sha256.Sum256([]byte(jobID + "|" + commit + "|" + tree))
	return hex.EncodeToString(sum[:])
}

// gateKinds maps a gate a CI run executes onto the requirement kinds it
// switches on. Gates a CI run does not execute (review, approval, release)
// are absent.
var gateKinds = map[jobs.GateItem]func(*CIRequirement){
	jobs.GateFormat:                  func(r *CIRequirement) { r.Format = true },
	jobs.GateStatic:                  func(r *CIRequirement) { r.Lint = true },
	jobs.GateLint:                    func(r *CIRequirement) { r.Lint = true },
	jobs.GateBuild:                   func(r *CIRequirement) { r.Compile = true },
	jobs.GateTargetedTests:           func(r *CIRequirement) { r.Unit = true },
	jobs.GateIntegrationChecks:       func(r *CIRequirement) { r.Integration = true },
	jobs.GateAffectedFullIntegration: func(r *CIRequirement) { r.Architecture, r.Security = true, true },
}

// requirementForRisk derives the requirement kinds a risk class must run
// from the class's decided gate set (jobs.GateSetForRiskClass, the one
// risk model): format <- format, lint <- static and lint, compile <- build,
// unit <- targeted tests, integration <- integration checks, and the
// architecture and security kinds <- affected/full integration CI (High
// and Critical). The sets are additive, so a risk rise only adds kinds. An
// unknown class fails closed with the gate-set error.
func requirementForRisk(class jobs.RiskClass) (CIRequirement, error) {
	gates, err := jobs.GateSetForRiskClass(class)
	if err != nil {
		return CIRequirement{}, err
	}
	var r CIRequirement
	for _, g := range gates {
		if set, ok := gateKinds[g]; ok {
			set(&r)
		}
	}
	return r, nil
}
