package nodes

// Purpose (this file): ShipWithRecovery — the consumer that joins the ship
//   leg to the recovery decision, and the line it draws between a node that
//   was LOST and a node that answered.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T3.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recoveryHarness pairs a ship harness with a recovery one, sharing the
// fencing register so the replacement really supersedes the lost attempt.
func recoveryHarness(t *testing.T, frame DispatchFrame) (ShipDeps, RequeueDeps, DeviceRecord, string) {
	t.Helper()
	ship, rec, _, head := shipHarness(t, frame)
	recovery := RequeueDeps{
		Placement:  Engine{Tunnels: func(string) TunnelState { return TunnelUp }},
		Candidates: []Candidate{healthyNode(rec.NodeID), spareLike(rec)},
		Attempts:   ship.Attempts,
		Continuity: fixedContinuity{records: []StreamedRecord{{Seq: 3, Attempt: 1, OperationID: "op-1"}}},
		Attention:  &recordingFiler{},
		Liveness:   func(string) Liveness { return LivenessUnknown },
	}
	return ship, recovery, rec, head
}

// spareLike is a second enrolled node that clears every filter, carrying
// the same signing identity as rec so the replacement's frame verifies.
func spareLike(rec DeviceRecord) Candidate {
	spare := rec
	spare.NodeID = "spare"
	spare.Tier = TierWorkerTrusted
	spare.Presence = PresenceReachable
	return Candidate{Record: spare, Report: CapabilityReport{Capabilities: []string{"docker"}}}
}

// recoveryRequest is work that may be re-queued.
func recoveryRequest(head string) ShipRequest {
	return ShipRequest{
		DispatchID:   "d1",
		Head:         head,
		Work:         Action{ID: "a1", Idempotent: true},
		Sensitivity:  SensitivityNormal,
		Capabilities: []string{"docker"},
		EntityID:     "job-7",
	}
}

// TestALostNodeCostsAReQueueNotAnError is the wiring this ticket exists
// for: without it the whole recovery path is a decision nothing consults.
func TestALostNodeCostsAReQueueNotAnError(t *testing.T) {
	ship, recovery, rec, head := recoveryHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
	})
	// The first node's call fails the way a lost node's does.
	caller, ok := ship.Caller.(*recordingCaller)
	if !ok {
		t.Fatalf("harness caller is %T, want the recording caller", ship.Caller)
	}
	caller.failFirst = errors.New("connection reset")

	outcome, err := ShipWithRecovery(context.Background(), ship, recovery, rec, recoveryRequest(head))
	if err != nil {
		t.Fatalf("a lost node was reported as a failed dispatch: %v", err)
	}
	if outcome.Attempt < 2 {
		t.Errorf("replacement attempt = %d, want it to supersede the lost one", outcome.Attempt)
	}
	if outcome.Commit != head {
		t.Errorf("result commit = %q, want the replacement's results (%q)", outcome.Commit, head)
	}
}

// TestANodeThatAnsweredIsNotRecoveredFrom is the line this consumer draws.
// A refusal is the node telling the controller something true; shipping the
// same work to a second machine would get the same answer from a different
// place, after doing whatever the first answer was warning about.
func TestANodeThatAnsweredIsNotRecoveredFrom(t *testing.T) {
	ship, recovery, rec, head := recoveryHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeRefused,
	})

	_, err := ShipWithRecovery(context.Background(), ship, recovery, rec, recoveryRequest(head))
	if err == nil {
		t.Fatal("a refusal was recovered from as though the node had vanished")
	}
	if strings.Contains(err.Error(), "spare") {
		t.Errorf("the work reached the replacement node anyway: %v", err)
	}
}

// TestAControllerSideGitFailureIsNotALoss is the same rule for the other
// direction. A failed push is the CONTROLLER's git failing; a second node
// would fail identically, having had a branch pushed at it first.
func TestAControllerSideGitFailureIsNotALoss(t *testing.T) {
	ship, recovery, rec, head := recoveryHarness(t, DispatchFrame{})
	ship.Remote = ship.Remote + "-gone"

	if _, err := ShipWithRecovery(
		context.Background(), ship, recovery, rec, recoveryRequest(head),
	); err == nil {
		t.Fatal("a failed push was recovered from as a lost node")
	}
}

// TestAnUnwiredLivenessSourceReadsAsUnknown proves the fail-closed
// default. A composition root that forgot the lookup must treat a ship
// failure as a possible loss, never as proof the node is fine.
func TestAnUnwiredLivenessSourceReadsAsUnknown(t *testing.T) {
	var deps RequeueDeps
	if got := deps.livenessOf("n1"); got != LivenessUnknown {
		t.Errorf("liveness = %q, want %q", got, LivenessUnknown)
	}
	if got := deps.tunnelOf("n1"); got != TunnelDown {
		t.Errorf("tunnel = %v, want %v", got, TunnelDown)
	}
}

// TestLossShapedErrorsAreExactlyTheTwoSentinels pins which failures mean
// "the node is gone". Widening this set re-queues work that failed for a
// reason a second machine will hit too.
func TestLossShapedErrorsAreExactlyTheTwoSentinels(t *testing.T) {
	cause := errors.New("eof")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"unreachable", ErrNodeUnreachable("n1", cause), true},
		{"tunnel dropped", ErrTunnelDropped("n1", "d1", cause), true},
		{"push failed", ErrPushFailed("d1", cause), false},
		{"fetch failed", ErrFetchFailed("d1", cause), false},
		{"worktree failed", ErrWorktreeFailed("d1", cause), false},
		{"static key", ErrStaticKeyOnDispatch("d1"), false},
		{"duplicate action", ErrDuplicateAction("a1"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLossShaped(tc.err); got != tc.want {
				t.Errorf("isLossShaped(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
