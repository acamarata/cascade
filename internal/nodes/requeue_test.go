package nodes

// Purpose (this file): the re-queue decision — the loss signals that
//   trigger it, the filters recovery must not bypass, the fencing that
//   stops a partitioned node racing its replacement, and the hold that
//   keeps non-idempotent work from being re-run on a guess.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T3.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingFiler captures held outcomes instead of filing them anywhere.
type recordingFiler struct {
	held []HeldOutcome
	err  error
}

func (f *recordingFiler) FileUnknownOutcome(_ context.Context, h HeldOutcome) error {
	if f.err != nil {
		return f.err
	}
	f.held = append(f.held, h)
	return nil
}

// fixedContinuity answers with a canned record set.
type fixedContinuity struct {
	records []StreamedRecord
	err     error
}

func (c fixedContinuity) RecordsSinceCheckpoint(context.Context, string) ([]StreamedRecord, error) {
	return c.records, c.err
}

// requeueHarness wires a controller with one healthy replacement node.
func requeueHarness(t *testing.T) (RequeueDeps, *recordingFiler) {
	t.Helper()
	filer := &recordingFiler{}
	return RequeueDeps{
		Placement:  Engine{Tunnels: func(string) TunnelState { return TunnelUp }},
		Candidates: []Candidate{healthyNode("lost"), healthyNode("spare")},
		Attempts:   NewAttemptRegister(),
		Continuity: fixedContinuity{records: []StreamedRecord{{Seq: 4, Attempt: 1, OperationID: "op-1"}}},
		Attention:  filer,
	}, filer
}

// healthyNode is a candidate that clears every placement filter.
func healthyNode(id string) Candidate {
	return Candidate{
		Record: DeviceRecord{NodeID: id, Tier: TierWorkerTrusted, Presence: PresenceReachable},
		Report: CapabilityReport{Capabilities: []string{"docker"}},
	}
}

// idempotentWork is a request whose action may safely be re-run.
func idempotentWork() RequeueRequest {
	return RequeueRequest{
		DispatchID:  "d1",
		LostNodeID:  "lost",
		Action:      Action{ID: "a1", Idempotent: true},
		Requirement: Requirement{Capabilities: []string{"docker"}, Sensitivity: SensitivityNormal},
		EntityID:    "job-7",
	}
}

// TestRequeueOnUnknownLiveness is the fail-closed rule (R-21.225). The most
// dangerous liveness value is `unknown`, because it is what a controller
// with no news looks at — and reading it as health parks work on a machine
// that is gone.
func TestRequeueOnUnknownLiveness(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  LossObservation
		want LossSignal
	}{
		{"unknown liveness", LossObservation{Liveness: LivenessUnknown, Tunnel: TunnelUp}, LossHeartbeat},
		{"explicit unavailable", LossObservation{Liveness: LivenessUnavailable, Tunnel: TunnelUp}, LossHeartbeat},
		{"tunnel reconnecting", LossObservation{Liveness: LivenessReachable, Tunnel: TunnelReconnecting}, LossTunnel},
		{"tunnel down", LossObservation{Liveness: LivenessReachable, Tunnel: TunnelDown}, LossTunnel},
		{"channel error", LossObservation{
			Liveness: LivenessReachable, Tunnel: TunnelUp,
			ChannelErr: ErrTunnelDropped("lost", "d1", errors.New("eof")),
		}, LossChannel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, lost := DetectLoss(tc.obs)
			if !lost {
				t.Fatalf("%+v was read as a healthy node", tc.obs)
			}
			if got != tc.want {
				t.Errorf("signal = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAHealthyNodeIsNotALoss is the other half, and it is what keeps the
// rule above from being "always re-queue".
func TestAHealthyNodeIsNotALoss(t *testing.T) {
	signal, lost := DetectLoss(LossObservation{Liveness: LivenessReachable, Tunnel: TunnelUp})
	if lost {
		t.Fatalf("a reachable node on an up tunnel was read as lost (%q)", signal)
	}
}

// TestRequeueIncrementsAttempt is the fencing rule (R-21.221). A
// replacement that reused the attempt number would land on the superseded
// attempt's own branch and worktree, where the node that is still alive may
// be writing.
func TestRequeueIncrementsAttempt(t *testing.T) {
	deps, _ := requeueHarness(t)
	first := deps.Attempts.Next("d1") // the attempt that was lost

	plan, err := PlanRequeue(context.Background(), deps, idempotentWork(), LossHeartbeat)
	if err != nil {
		t.Fatalf("PlanRequeue: %v", err)
	}
	if plan.Disposition != DispositionRequeue {
		t.Fatalf("disposition = %q, want %q", plan.Disposition, DispositionRequeue)
	}
	if plan.Attempt != first+1 {
		t.Errorf("replacement attempt = %d, want %d", plan.Attempt, first+1)
	}
	if plan.Branch == DispatchBranch("d1", first) {
		t.Errorf("the replacement reuses the superseded attempt's branch %q", plan.Branch)
	}
	if plan.Node.NodeID != "spare" {
		t.Errorf("replacement node = %q, want the node that was not lost", plan.Node.NodeID)
	}
}

// TestRequeueNeverReturnsToTheLostNode pins the exclusion. The device
// record's liveness is maintained by a different loop on a different
// schedule, so a recovery that trusted placement to reject the lost node
// would loop back onto it for as long as that record says reachable.
func TestRequeueNeverReturnsToTheLostNode(t *testing.T) {
	deps, _ := requeueHarness(t)
	deps.Candidates = []Candidate{healthyNode("lost")} // the lost node still looks healthy

	if _, err := PlanRequeue(context.Background(), deps, idempotentWork(), LossHeartbeat); err == nil {
		t.Fatal("the work was placed back on the node it was just lost from")
	}
}

// TestRequeueNeverBypassesAFilter is the rule recovery is most tempted to
// break: the work is late, and the filter in the way is the one saying this
// machine must not see it.
func TestRequeueNeverBypassesAFilter(t *testing.T) {
	for _, tc := range []struct {
		name      string
		candidate Candidate
		req       Requirement
	}{
		{"trust tier", Candidate{
			Record: DeviceRecord{NodeID: "spare", Tier: TierPairedDevice, Presence: PresenceReachable},
			Report: CapabilityReport{Capabilities: []string{"docker"}},
		}, Requirement{Capabilities: []string{"docker"}, Sensitivity: SensitivityRestricted}},
		{"capability", healthyNode("spare"),
			Requirement{Capabilities: []string{"browser"}, Sensitivity: SensitivityNormal}},
		{"liveness", Candidate{
			Record: DeviceRecord{NodeID: "spare", Tier: TierWorkerTrusted, Presence: PresenceUnknown},
			Report: CapabilityReport{Capabilities: []string{"docker"}},
		}, Requirement{Capabilities: []string{"docker"}, Sensitivity: SensitivityNormal}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := requeueHarness(t)
			deps.Candidates = []Candidate{healthyNode("lost"), tc.candidate}
			req := idempotentWork()
			req.Requirement = tc.req

			if _, err := PlanRequeue(context.Background(), deps, req, LossHeartbeat); err == nil {
				t.Fatalf("recovery placed work on a node the %s filter excludes", tc.name)
			}
		})
	}
}

// TestLocalOnlyWorkIsNeverRequeuedToANode is 06 §5.22's rule, restated on
// the recovery path because that is where it would be lost: the work has
// already failed once, and "just run it here" is the tempting answer.
func TestLocalOnlyWorkIsNeverRequeuedToANode(t *testing.T) {
	deps, _ := requeueHarness(t)
	req := idempotentWork()
	req.Requirement = Requirement{Sensitivity: SensitivityLocalOnly}

	plan, err := PlanRequeue(context.Background(), deps, req, LossTunnel)
	if err == nil {
		t.Fatalf("local-only work was re-queued to %q", plan.Node.NodeID)
	}
	if plan.Node.NodeID != "" {
		t.Errorf("a refused re-queue still named node %q", plan.Node.NodeID)
	}
}

// TestAmbiguousNonIdempotentHeld is R-21.221's held state. Both automatic
// choices are destructive and nothing in the system can tell which, so a
// person decides.
func TestAmbiguousNonIdempotentHeld(t *testing.T) {
	deps, filer := requeueHarness(t)
	req := idempotentWork()
	req.Action.Idempotent = false
	deps.Attempts.Next("d1")

	plan, err := PlanRequeue(context.Background(), deps, req, LossChannel)
	if err == nil {
		t.Fatal("a non-idempotent action with an unknown outcome was handled silently")
	}
	if plan.Disposition != DispositionHold {
		t.Errorf("disposition = %q, want %q", plan.Disposition, DispositionHold)
	}
	if plan.Node.NodeID != "" || plan.Attempt != 0 {
		t.Errorf("a held dispatch carries a replacement target: %+v", plan)
	}
	if len(filer.held) != 1 {
		t.Fatalf("%d attention entr(y/ies) filed, want 1; a held dispatch nobody is told about is lost",
			len(filer.held))
	}
	held := filer.held[0]
	if held.ActionID != "a1" || held.NodeID != "lost" || held.Attempt != 1 {
		t.Errorf("held entry = %+v, want it to name the action, the node and the attempt", held)
	}
	if !strings.Contains(held.Reason, "idempotent") {
		t.Errorf("held reason does not say why it cannot be automatic: %q", held.Reason)
	}
}

// TestAHoldThatCannotBeFiledIsReported is the rule that keeps the hold from
// becoming a quiet drop. A filer that fails must surface as the filing
// failure, not as the ordinary held error, or the operator is told the work
// is waiting for them somewhere it never arrived.
func TestAHoldThatCannotBeFiledIsReported(t *testing.T) {
	deps, filer := requeueHarness(t)
	filer.err = errors.New("the attention store is unavailable")
	req := idempotentWork()
	req.Action.Idempotent = false

	_, err := PlanRequeue(context.Background(), deps, req, LossChannel)
	if err == nil {
		t.Fatal("a hold nobody could be told about was reported as handled")
	}
	if !strings.Contains(err.Error(), "could not be held") {
		t.Errorf("error = %v, want it to say the hold itself failed", err)
	}
}

// TestHoldingWithNoFilerIsRefused proves the seam cannot be left unwired.
func TestHoldingWithNoFilerIsRefused(t *testing.T) {
	deps, _ := requeueHarness(t)
	deps.Attention = nil
	req := idempotentWork()
	req.Action.Idempotent = false

	if _, err := PlanRequeue(context.Background(), deps, req, LossHeartbeat); err == nil {
		t.Fatal("a controller with no attention filer held a dispatch silently")
	}
}

// TestRequeueWithoutAFencingRegisterIsRefused proves an unwired register
// cannot produce an unfenced replacement.
func TestRequeueWithoutAFencingRegisterIsRefused(t *testing.T) {
	deps, _ := requeueHarness(t)
	deps.Attempts = nil

	if _, err := PlanRequeue(context.Background(), deps, idempotentWork(), LossHeartbeat); err == nil {
		t.Fatal("a re-queue was planned with no way to fence it")
	}
}

// TestRequeueRefusesAnUnidentifiedDispatch covers the boundary.
func TestRequeueRefusesAnUnidentifiedDispatch(t *testing.T) {
	deps, _ := requeueHarness(t)
	for _, req := range []RequeueRequest{
		{LostNodeID: "lost", Action: Action{ID: "a1", Idempotent: true}},
		{DispatchID: "d1", LostNodeID: "lost", Action: Action{Idempotent: true}},
	} {
		if _, err := PlanRequeue(context.Background(), deps, req, LossHeartbeat); err == nil {
			t.Errorf("an unidentified re-queue was accepted: %+v", req)
		}
	}
}
