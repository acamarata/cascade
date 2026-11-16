//go:build !windows

// Purpose: the completion gate's approval path. The reviewer and the
// completion policy decide first; a denial that needs a human (Critical
// risk, or a degraded review) files exactly one L3 request in the daemon's
// one ApprovalQueue, bound to ApprovalScopeAction(job, tree, policy
// version), and the next check redeems the stored token once through
// CompletionPolicy.checkApproval.
// Inputs: the daemon policyWiring (Queue, Entries, Registry) reached from
// w.Deps through the acceptance verifier, and the jobs database for the
// stored request rows.
// Outputs: completionApprovals and approvalCompletionGate, the gate the
// completion hook pack serves when the daemon holds a policy wiring.
// Constraints: the nonce leaves this file only into the stored row (packed
// beside the request id, which the row has no column of its own for); a
// stored request is classified by its entry state, never by reason text; a
// row marked denied never refiles; a pending reason keeps the denial text.
// SPORT: cmd/cascade/completion-approval (ADD, P1-PBD-09).
package main

import (
	"context"
	"errors"
	"strings"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// completionCapability is the capability every completion request asks for.
const completionCapability = "jobs.completion.accept"

// completionTokenSep joins request id and nonce in the stored nonce column.
const completionTokenSep = "\x1f"

// completionApprovalRows is the slice of *jobs.Store the approval path
// reads and writes: classifyStored's completionApprovalStore plus the
// insert-once Put. The _test.go file injects a failing one.
type completionApprovalRows interface {
	completionApprovalStore
	PutCompletionApproval(ctx context.Context, a jobs.CompletionApproval) error
}

// completionApprovals is the gate's view of the daemon's one queue.
type completionApprovals struct {
	queue   policy.ApprovalQueue
	entries policy.ApprovalEntrySource
	store   completionApprovalRows
	clock   runtime.Clock
}

// completionApprovalsFor resolves the daemon policy wiring the acceptance
// verifier was built over. A gate with no daemon verifier or no policy
// wiring returns nil and keeps its approval-free behaviour (every
// approval check then denies: no queue is configured).
func completionApprovalsFor(ctx context.Context, verifier jobs.AcceptanceVerifier) (*completionApprovals, error) {
	v, ok := verifier.(*ciAcceptanceVerifier)
	if !ok || v.w == nil || v.w.Deps.Policy == nil {
		return nil, nil
	}
	pol := v.w.Deps.Policy
	if pol.Queue == nil || pol.Entries == nil || pol.Registry == nil || v.jobsDB == nil {
		return nil, cascade.New(cascade.KindUnavailable, "completion approval: queue, entries, registry or jobs db not wired")
	}
	if err := ensureCompletionCapabilityRegistered(ctx, pol.Registry); err != nil {
		return nil, err
	}
	if err := jobs.ApplyCompletionApprovalSchema(ctx, v.jobsDB, migrate.SQLiteEmitter{}, v.w.Clock, "", ""); err != nil {
		return nil, err
	}
	return &completionApprovals{queue: pol.Queue, entries: pol.Entries, store: jobs.NewStore(v.jobsDB), clock: v.w.Clock}, nil
}

// ensureCompletionCapabilityRegistered adds jobs.completion.accept once.
// An identical existing entry is accepted; any other error is returned.
func ensureCompletionCapabilityRegistered(ctx context.Context, reg policy.CapabilityRegistry) error {
	want := policy.Capability{
		Name:          completionCapability,
		Desc:          "accept a completed job at one checkpoint tree after a human approval",
		DefaultPolicy: policy.ClassExternalSideEffect,
	}
	err := reg.Add(ctx, want)
	if err == nil || !cascade.HasKind(err, cascade.KindConflict) {
		return err
	}
	if have, lerr := reg.Lookup(ctx, want.Name); lerr == nil && have == want {
		return nil
	}
	return err
}

// approvalQueue is the CompletionPolicyDeps.Approvals value: the daemon's
// one queue, or a nil interface when no wiring was resolved.
func (a *completionApprovals) approvalQueue() policy.ApprovalQueue {
	if a == nil {
		return nil
	}
	return a.queue
}

// gate wraps g with the approval path; a nil receiver returns g unchanged.
func (a *completionApprovals) gate(g *completionPolicyGate) completionGateFace {
	if a == nil {
		return g
	}
	return &approvalCompletionGate{completionPolicyGate: g, approvals: a}
}

// completionGateFace is the hook-pack gate (hookpacks.PolicyGate).
type completionGateFace interface {
	CompletionCheck(ctx context.Context, jobID string) (bool, string, error)
}

// storedRequest is the request a check found stored for its action.
type storedRequest struct {
	row       jobs.CompletionApproval
	found     bool
	requestID string
}

// redeem reads the request stored for req's action and, unless a human
// denied it, hands its token to the policy's one checkApproval redemption.
func (a *completionApprovals) redeem(ctx context.Context, req *jobs.TransitionRequest) (storedRequest, error) {
	action := jobs.ApprovalScopeAction(req.Job.ID, req.TreeHash, req.PolicyVersion)
	row, found, err := a.store.CompletionApprovalFor(ctx, req.Job.ID, action)
	if err != nil || !found {
		return storedRequest{}, err
	}
	id, nonce, ok := strings.Cut(row.Nonce, completionTokenSep)
	if !ok || id == "" || nonce == "" {
		return storedRequest{}, cascade.New(cascade.KindIntegrity, "completion approval: stored request is malformed")
	}
	if !row.Denied {
		req.ApprovalRequestID, req.ApprovalNonce = id, nonce
	}
	return storedRequest{row: row, found: true, requestID: id}, nil
}

// afterDenial acts on an approval denial. It returns jobs.ErrApprovalPending
// while the job waits on a human, which is never a gate attempt, and nil for
// every other denial.
func (a *completionApprovals) afterDenial(ctx context.Context, req jobs.TransitionRequest, risk jobs.RiskClass,
	st storedRequest, reason string) (string, error) {
	if req.TreeHash == "" || req.CheckpointID == "" {
		return "completion approval needs a checkpoint tree", nil
	}
	if st.found {
		class, err := classifyStored(ctx, a.store, a.entries, st.row, st.requestID, nil)
		if err != nil {
			return "completion approval unreadable: " + err.Error(), nil
		}
		switch class {
		case storedPending:
			return approvalPendingReason(st.requestID, reason), jobs.ErrApprovalPending
		case storedDenied:
			return reason + " (completion approval " + st.requestID + " denied)", nil
		case storedApproved:
			return reason, nil
		case storedRefile:
			// The stale row is gone; file a fresh request below.
		}
	}
	id, err := a.file(ctx, req, risk)
	if err != nil {
		return "completion approval not filed: " + err.Error(), nil
	}
	return approvalPendingReason(id, reason), jobs.ErrApprovalPending
}

// file enqueues one tree-bound L3 request and stores its token. A store
// write failure cancels the request, so nothing stays filed.
func (a *completionApprovals) file(ctx context.Context, req jobs.TransitionRequest, risk jobs.RiskClass) (string, error) {
	action := jobs.ApprovalScopeAction(req.Job.ID, req.TreeHash, req.PolicyVersion)
	res, err := a.queue.Enqueue(ctx, policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectUser, ID: "cascade-operator"},
		Capability: completionCapability, Level: policy.L3, Action: action,
		Summary: completionSummary(req.Job.ID, req.TreeHash, risk),
	})
	if err != nil {
		return "", err
	}
	if res.Deduplicated {
		return res.RequestID, nil
	}
	err = a.store.PutCompletionApproval(ctx, jobs.CompletionApproval{
		JobID: req.Job.ID, Action: action, Nonce: res.RequestID + completionTokenSep + res.Token.Nonce,
		PolicyVersion: req.PolicyVersion, CreatedAt: a.clock.Now().UTC().Unix(),
	})
	if err != nil {
		if cerr := a.queue.Cancel(ctx, res.RequestID); cerr != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "completion approval: store failed; cancel failed: "+cerr.Error())
		}
		return "", err
	}
	return res.RequestID, nil
}

// approvalPendingReason is the denial text of a job waiting on a human:
// the request id first, then the denial that asked for the approval (a
// degraded review keeps its reviewer text and decision reference).
func approvalPendingReason(requestID, cause string) string {
	return "completion approval pending: " + requestID + "; " + cause
}

// completionSummary is "accept job <id> at tree <12 hex> (<risk class>)",
// the job id shortened only when the whole would pass the display limit.
func completionSummary(jobID, tree string, risk jobs.RiskClass) string {
	if len(tree) > 12 {
		tree = tree[:12]
	}
	rest := " at tree " + tree + " (" + string(risk) + ")"
	const prefix = "accept job "
	return prefix + fitID(jobID, approvalTextLimit-len(prefix)-len(rest)) + rest
}

// approvalCompletionGate is the completion gate with its approval path.
type approvalCompletionGate struct {
	*completionPolicyGate
	approvals *completionApprovals
}

// CompletionCheck implements hookpacks.PolicyGate. A denial records one
// gate attempt unless the job is waiting on a pending human approval.
func (g *approvalCompletionGate) CompletionCheck(ctx context.Context, jobID string) (bool, string, error) {
	ok, reason, err := g.approvalVerdict(ctx, jobID)
	if errors.Is(err, jobs.ErrApprovalPending) {
		return false, reason, nil
	}
	if err == nil && !ok {
		if _, rerr := g.store.RecordGateDenial(ctx, jobID, reason); rerr != nil && !cascade.HasKind(rerr, cascade.KindNotFound) {
			return false, "", rerr
		}
	}
	return ok, reason, err
}

// approvalVerdict is completionVerdict with the stored token redeemed first.
// Only an approval denial (Critical, or a degraded review) at the accepted
// edge files or classifies a request; ErrApprovalPending marks the wait.
func (g *approvalCompletionGate) approvalVerdict(ctx context.Context, jobID string) (bool, string, error) {
	req, risk, reason, err := g.completionRequest(ctx, jobID)
	if err != nil || reason != "" {
		return false, reason, err
	}
	var st storedRequest
	if req.Target == jobs.JobStateAccepted && req.TreeHash != "" {
		if st, err = g.approvals.redeem(ctx, &req); err != nil {
			return false, "completion approval unreadable: " + err.Error(), nil
		}
	}
	err = g.policy.Transition(ctx, req)
	if err == nil {
		return true, "", nil
	}
	var denied *jobs.ErrorCompletionDenied
	if !errors.As(err, &denied) {
		return false, "", err
	}
	text := g.reviewerDenial(ctx, *req.Job, completionDeniedReason(denied))
	if !denied.ExpiredApproval || req.Target != jobs.JobStateAccepted {
		return false, text, nil
	}
	text, err = g.approvals.afterDenial(ctx, req, risk, st, text)
	return false, text, err
}

// completionRequest builds the request completionVerdict builds, stamped
// with the completion-approval policy version. A non-empty reason denies.
func (g *approvalCompletionGate) completionRequest(ctx context.Context, jobID string) (jobs.TransitionRequest, jobs.RiskClass, string, error) {
	job, found, err := g.store.GetJob(ctx, jobID)
	if err != nil {
		return jobs.TransitionRequest{}, "", "", err
	}
	if !found {
		return jobs.TransitionRequest{}, "", "", cascade.Newf(cascade.KindNotFound, "completion gate: unknown job %q", jobID)
	}
	risk := jobs.RiskClass(job.RiskClass)
	if job.RiskClass == "" {
		risk = jobs.RiskClassNormal
	}
	req := jobs.TransitionRequest{Job: &job, Target: nextCompletionTarget(job.State), PlannedRiskClass: risk,
		Caller: jobs.PolicyEngineIdentity{EngineID: completionGateEngineID}, PolicyVersion: jobs.CompletionApprovalPolicyVersion}
	if req.Target != jobs.JobStateAccepted {
		return req, risk, "", nil
	}
	if job.RiskClass == "" {
		return req, risk, "completion gate: job " + jobID + " has no risk class", nil
	}
	snap, found, err := g.checkpoints.CurrentCheckpoint(ctx, jobID)
	if err != nil || !found {
		return req, risk, "completion gate: current checkpoint unavailable", nil
	}
	req.CheckpointID, req.TreeHash = snap.CheckpointID, snap.TreeHash
	return req, risk, g.fillReviewerTree(ctx, job, &req), nil
}
