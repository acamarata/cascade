package jobs

// Purpose: R-16.12's completion gate -- "an agent saying done is
// EVIDENCE, not the completion decision". CompletionPolicy.Transition
// is the ONLY path that may move a job across a policy-reserved edge.
//
// CALLER-IDENTITY design note (journal has the full contradiction):
// internal/policy cannot import internal/jobs (the conductor->egress->
// secrets->policy cycle), so Transition cannot call INTO a live
// policy.Engine to verify its caller. The trust boundary is therefore
// the EngineID this CompletionPolicy is constructed with, compared
// against the caller-presented PolicyEngineIdentity -- a documented,
// ticket-local choice, not a contract/tree contradiction; AF/S-66.T1's
// hook pack threads the real engine id through at the composition root.
//
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

// CompletionPolicyDeps bundles CompletionPolicy's real collaborators.
// Approvals and Attention may be nil (Critical-class approval checks and
// OutOfLeaseScope attention items are then skipped -- acceptable for a
// CompletionPolicy that only ever gates Normal/Low jobs, never for a
// production composition that also handles Critical/High).
type CompletionPolicyDeps struct {
	Store     *Store
	Ledger    *EvidenceLedger
	Approvals policy.ApprovalQueue
	Attention *supervision.Store
	Bus       *events.Bus
	Clock     runtime.Clock
	EngineID  string
}

// CompletionPolicy implements the R-16.12 completion gate.
type CompletionPolicy struct {
	deps CompletionPolicyDeps
}

// NewCompletionPolicy constructs the gate. Store, Ledger and Clock are
// required; EngineID must be non-empty (an empty expected id would make
// every ErrNotPolicyEngine check vacuously pass on an equally-empty
// presented identity).
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

// Transition runs the R-16.12/R-21.144-146/164/183 evaluation in the
// order the evidence actually needs (the contract numbers Reclassify
// "step 6" but requires it BEFORE step 3's completeness check; this
// follows that real order, not the numbering): caller identity, state
// ordering, chain verify, reclassify + scope containment, completeness,
// approval (Critical only), checkpoint binding, atomic commit.
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

	if mismatch := cp.checkpointMismatch(ctx, req); mismatch != nil {
		return cp.deny(ctx, req, &ErrorCompletionDenied{CheckpointMismatch: mismatch})
	}

	return cp.commit(ctx, req, cursor)
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

// checkApproval redeems the Critical-class human_approval token through
// the REAL I/S-18.T3 ApprovalQueue.ConsumeToken, binding it to
// {job_id, tree_hash, policy_version} by encoding those three fields as
// the canonical Action string ConsumeToken re-hashes and compares --
// reusing the queue's own action-binding mechanism instead of a second
// scope field the queue does not have. A token minted for a different
// job/tree/policy-version re-hashes to a different digest and is
// refused by the queue itself; an unknown, consumed or expired request
// id is refused by the queue's own state checks. Every refusal reaches
// here as a non-nil error, which Transition folds into ExpiredApproval.
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
// never retroactively satisfy an already-evaluated gate.
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
		_, err := tx.ExecContext(ctx, `UPDATE `+tableJob+` SET state = ?, updated_at = ? WHERE id = ?`,
			string(req.Target), now, req.Job.ID)
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: commit completion transition")
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
	return nil
}
