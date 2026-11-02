package jobs

// Purpose: gate reserved completion edges through evidence and checkpoint-bound CI.
// Caller identity is the composition root's EngineID, compared with the request.
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EventGateDenied is the R-16.73 event every completion-gate denial
// publishes exactly once; a successful Transition publishes none.
const (
	completionGateNamespace                  = "jobs.gate"
	EventGateDenied         events.EventKind = "jobs.gate.denied"
)

// PolicyEngineIdentity is the caller-identity token step 1 checks (see
// this file's header comment).
type PolicyEngineIdentity struct {
	EngineID string
}

// AcceptanceVerdict binds the acceptance test result to an immutable checkpoint.
type AcceptanceVerdict struct {
	Passed                         bool
	CheckpointID, TreeHash, Reason string
}

// AcceptanceVerifier synchronously verifies the job's current checkpoint.
type AcceptanceVerifier interface {
	VerifyAcceptance(ctx context.Context, jobID string) (AcceptanceVerdict, error)
}

// CompletionPolicyDeps bundles the gate's collaborators. Production requires acceptance.
type CompletionPolicyDeps struct {
	Store             *Store
	Ledger            *EvidenceLedger
	Approvals         policy.ApprovalQueue
	Attention         *supervision.Store
	Bus               *events.Bus
	Clock             runtime.Clock
	EngineID          string
	Acceptance        AcceptanceVerifier
	RequireAcceptance bool
}

// CompletionPolicy implements the R-16.12 completion gate.
type CompletionPolicy struct {
	deps CompletionPolicyDeps
}

// NewCompletionPolicy validates required collaborators and a non-empty EngineID.
func NewCompletionPolicy(deps CompletionPolicyDeps) (*CompletionPolicy, error) {
	if deps.Store == nil || deps.Ledger == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: CompletionPolicy requires a Store and Ledger")
	}
	if deps.Clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: CompletionPolicy requires a Clock")
	}
	if deps.EngineID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: CompletionPolicy requires a non-empty EngineID")
	}
	if deps.RequireAcceptance && deps.Acceptance == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: CompletionPolicy requires an Acceptance verifier")
	}
	return &CompletionPolicy{deps: deps}, nil
}

// TransitionRequest is one Transition call's full input.
type TransitionRequest struct {
	Job                *Job
	Target             JobState
	Caller             PolicyEngineIdentity
	CheckpointID       string
	TreeHash           string
	PlannedRiskClass   RiskClass
	ActualFootprint    ChangeFootprint
	LeaseScopePrefixes []string
	ProbeRoot          string
	ApprovalRequestID  string
	ApprovalNonce      string
	PolicyVersion      string
	SessionID          string
	TicketID           string
	Attempt            int
}

// Transition checks identity, ordering, evidence, scope, approval and acceptance
// before an atomic checkpoint-bound state commit.
func (cp *CompletionPolicy) Transition(ctx context.Context, req TransitionRequest) error {
	if req.Job == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: Transition requires a job")
	}
	if req.Caller.EngineID == "" || req.Caller.EngineID != cp.deps.EngineID {
		return ErrNotPolicyEngine
	}
	if err := PolicyTransitionAllowed(req.Job.State, req.Target); err != nil {
		wrong := req.Target
		return cp.deny(ctx, req, &ErrorCompletionDenied{WrongSuccessor: &wrong})
	}
	cursor, err := cp.deps.Ledger.Cursor(ctx, req.Job.ID)
	if err != nil {
		return err
	}
	if err := cp.deps.Ledger.Verify(ctx, req.Job.ID); err != nil {
		return err
	}
	derived, denyErr := cp.reclassifyAndCheck(ctx, req)
	if denyErr != nil {
		return denyErr
	}
	gateSet, err := GateSetForRiskClass(derived)
	if err != nil {
		return err
	}
	missing, err := cp.missingEvidence(ctx, req.Job.ID, gateSet)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return cp.deny(ctx, req, &ErrorCompletionDenied{MissingEvidence: missing})
	}
	if requiresHumanApproval(derived, req.Target) {
		if err := cp.checkApproval(ctx, req); err != nil {
			return cp.deny(ctx, req, &ErrorCompletionDenied{ExpiredApproval: true, Reason: err.Error()})
		}
	}
	if err := cp.verifyAcceptance(ctx, req); err != nil {
		return err
	}
	if mismatch := cp.checkpointMismatch(ctx, req); mismatch != nil {
		return cp.deny(ctx, req, &ErrorCompletionDenied{CheckpointMismatch: mismatch})
	}
	return cp.commit(ctx, req, cursor)
}

// verifyAcceptance fails closed before commit when the requested tree did not pass.
func (cp *CompletionPolicy) verifyAcceptance(ctx context.Context, req TransitionRequest) error {
	if req.Target != JobStateAccepted || (cp.deps.Acceptance == nil && !cp.deps.RequireAcceptance) {
		return nil
	}
	if cp.deps.Acceptance != nil {
		verdict, err := cp.deps.Acceptance.VerifyAcceptance(ctx, req.Job.ID)
		if err == nil && verdict.Passed && req.CheckpointID != "" && req.TreeHash != "" &&
			verdict.CheckpointID == req.CheckpointID && verdict.TreeHash == req.TreeHash {
			return nil
		}
	}
	return cp.deny(ctx, req, &ErrorCompletionDenied{Reason: "ci-acceptance-not-passed"})
}

// reclassifyAndCheck runs the R-21.146 reclassification + scope
// containment step, returning the derived class and, on a denial, the
// (already-published) error to return from Transition.
func (cp *CompletionPolicy) reclassifyAndCheck(ctx context.Context, req TransitionRequest) (RiskClass, error) {
	derived, esc, outOfScope, err := Reclassify(ctx, req.PlannedRiskClass, req.ActualFootprint, req.LeaseScopePrefixes, req.ProbeRoot)
	if err != nil {
		return "", err
	}
	if len(outOfScope) > 0 {
		cp.pushOutOfScopeAttention(ctx, req)
		return "", cp.deny(ctx, req, &ErrorCompletionDenied{OutOfLeaseScope: outOfScope})
	}
	if esc != nil {
		return "", cp.deny(ctx, req, &ErrorCompletionDenied{RiskEscalated: esc})
	}
	return derived, nil
}

// deny publishes exactly one jobs.gate.denied event and returns denial.
func (cp *CompletionPolicy) deny(ctx context.Context, req TransitionRequest, denial *ErrorCompletionDenied) error {
	if cp.deps.Bus != nil {
		payload, mErr := json.Marshal(gateDeniedWirePayload{
			JobID: req.Job.ID, SessionID: req.SessionID, TicketID: req.TicketID,
			Reason: denial.Error(), Attempt: req.Attempt,
		})
		if mErr == nil {
			_, _ = cp.deps.Bus.Publish(ctx, completionGateNamespace, EventGateDenied, "jobs.completion", payload)
		}
	}
	return denial
}

// pushOutOfScopeAttention raises one R/S-39.T1 attention item for an
// OutOfLeaseScope denial, regardless of risk class.
func (cp *CompletionPolicy) pushOutOfScopeAttention(ctx context.Context, req TransitionRequest) {
	if cp.deps.Attention == nil {
		return
	}
	_, _ = cp.deps.Attention.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: req.Job.ID,
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindTask, ID: req.Job.ID},
		Priority:  0,
	})
}

// missingEvidence returns the required EvidenceKinds (evidenceKindsForGateSet,
// completion_errors.go) that have no passing, non-agent-claim record.
func (cp *CompletionPolicy) missingEvidence(ctx context.Context, jobID string, gateSet GateSet) ([]EvidenceKind, error) {
	var missing []EvidenceKind
	for _, kind := range evidenceKindsForGateSet(gateSet) {
		rec, err := cp.deps.Ledger.Query(ctx, jobID, kind)
		if err != nil {
			missing = append(missing, kind)
			continue
		}
		if rec.ProducerCapability == ProducerAgentClaim || rec.Outcome != OutcomePass {
			missing = append(missing, kind)
		}
	}
	return missing, nil
}

// checkpointMismatch compares req.CheckpointID against the checkpoint
// id on the job's evidence rows (R-21.144). Until AP/S-81.T1's real
// checkpoint record lands, the contract's own text treats it as an
// opaque caller-supplied string compared verbatim; an empty
// CheckpointID never mismatches (no binding requested).
func (cp *CompletionPolicy) checkpointMismatch(ctx context.Context, req TransitionRequest) *string {
	if req.CheckpointID == "" {
		return nil
	}
	for _, kind := range []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial} {
		rec, err := cp.deps.Ledger.Query(ctx, req.Job.ID, kind)
		if err != nil {
			continue
		}
		if rec.CheckpointID != "" && rec.CheckpointID != req.CheckpointID {
			got := rec.CheckpointID
			return &got
		}
	}
	return nil
}

// checkApproval consumes the Critical-class token bound to job, tree and policy.
func (cp *CompletionPolicy) checkApproval(ctx context.Context, req TransitionRequest) error {
	if cp.deps.Approvals == nil {
		return cascade.New(cascade.KindPermissionDenied, "jobs: no approval queue configured")
	}
	_, err := cp.deps.Approvals.ConsumeToken(ctx, policy.ConsumeRequest{
		RequestID: req.ApprovalRequestID, Nonce: req.ApprovalNonce, Action: ApprovalScopeAction(req.Job.ID, req.TreeHash, req.PolicyVersion),
	})
	return err
}

// commit re-reads the ledger cursor in the SAME store transaction as
// the state write: a row appended after Transition's initial read can
// never retroactively satisfy an already-evaluated gate. The UPDATE
// carries the evaluated state (`AND state = ?`) and must change exactly
// one row, so two transitions evaluated from one state cannot both win
// and a job moved out of band after evaluation is left untouched.
func (cp *CompletionPolicy) commit(ctx context.Context, req TransitionRequest, evaluatedCursor int64) error {
	var denial *ErrorCompletionDenied
	txErr := cp.deps.Store.withTx(ctx, func(tx *sql.Tx) error {
		var seq sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM `+tableEvidence+` WHERE job_id = ?`, req.Job.ID).Scan(&seq); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: re-read evidence cursor")
		}
		if seq.Int64 != evaluatedCursor {
			denial = &ErrorCompletionDenied{Reason: "evidence cursor advanced during evaluation"}
			return nil
		}
		now := cp.deps.Clock.Now().UTC().Unix()
		res, err := tx.ExecContext(ctx, `UPDATE `+tableJob+` SET state = ?, updated_at = ? WHERE id = ? AND state = ?`,
			string(req.Target), now, req.Job.ID, string(req.Job.State))
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: commit completion transition")
		}
		if n, err := res.RowsAffected(); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: read completion transition result")
		} else if n != 1 {
			return cascade.Newf(cascade.KindConflict,
				"jobs: job %q is no longer in state %s; the completion transition was not applied", req.Job.ID, req.Job.State)
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}
	if denial != nil {
		return cp.deny(ctx, req, denial)
	}
	req.Job.State = req.Target
	return cp.deps.Store.afterTransition(ctx, req.Job.ID, req.Target)
}
