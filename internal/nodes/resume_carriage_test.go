package nodes

// Purpose (this file): the resume point's journey — decided by the
//   re-queue, carried on the replacement attempt, handed to the node by
//   the claim verb, and acted on by the execute leg.
// WHAT IT PROTECTS: a replacement on a DIFFERENT machine. That node's own
//   durable action log is empty, so nothing local can tell it the work was
//   already done; the carried list is the only thing that can, and before
//   P1-E17-W4-S37-T7 the controller computed it and threw it away.
// SPORT: internal/nodes status:resume-carriage (ADD tests) —
//   P1-E17-W4-S37-T7.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
)

// resumeFixture is one lost attempt's recorded history.
func resumeFixture() ResumePoint {
	return ResumePoint{
		EntityID: "job-1", Seq: 11,
		CompletedOperations: []string{"already-ran"},
	}
}

// TestTheClaimHandsTheNodeItsResumePoint is the carriage: what the
// controller put on the attempt is what the node is told.
func TestTheClaimHandsTheNodeItsResumePoint(t *testing.T) {
	rv := NewRendezvous()
	registry := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(registry, rv)

	// The replacement attempt, as ShipWithRecovery mints it.
	done := make(chan error, 1)
	go func() {
		_, err := rv.Call(context.Background(), "spare", Attempt{
			DispatchID: "d-1", Attempt: 2, NodeID: "spare",
			Branch: "dispatch/d-1/2", Resume: resumeFixture(),
		})
		done <- err
	}()

	var claim ClaimResponse
	claimWhenOfferedTo(t, registry, "spare", &claim)
	if claim.Attempt != 2 {
		t.Fatalf("claimed attempt = %d, want the replacement's fenced number", claim.Attempt)
	}
	if claim.Resume.Seq != 11 {
		t.Errorf("resume seq = %d, want 11; the node would restart the entity", claim.Resume.Seq)
	}
	if !claim.Resume.Completed("already-ran") {
		t.Errorf("the completed operations did not travel: %+v", claim.Resume)
	}
	if claim.Resume.FromScratch {
		t.Error("a replacement with a recorded history was told it was a cold start")
	}

	// Release the waiting caller so the goroutine does not outlive the test.
	_ = rv.Report(DispatchFrame{DispatchID: "d-1", Attempt: 2, NodeID: "spare"})
	<-done
}

// TestAFirstAttemptCarriesNoResumePoint keeps the field from reading as a
// resume on work that has never run. A cold start that looked like a
// resume would have the node skip nothing but report a position it never
// reached.
func TestAFirstAttemptCarriesNoResumePoint(t *testing.T) {
	rv := NewRendezvous()
	registry := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(registry, rv)

	done := make(chan error, 1)
	go func() {
		_, err := rv.Call(context.Background(), "n1",
			NewAttempt(NewAttemptRegister(), "d-first", "n1", nowForTest()))
		done <- err
	}()

	var claim ClaimResponse
	claimWhenOfferedTo(t, registry, "n1", &claim)
	if claim.Resume.Seq != 0 || len(claim.Resume.CompletedOperations) != 0 {
		t.Errorf("a first attempt carried a resume point: %+v", claim.Resume)
	}

	_ = rv.Report(DispatchFrame{DispatchID: "d-first", Attempt: claim.Attempt, NodeID: "n1"})
	<-done
}

// TestAMachineThatNeverSawTheWorkStillRefusesACompletedAction is the
// acceptance, and the case durable dedup cannot reach: the action log is
// EMPTY, so the only thing that can stop a second run is the carried list.
func TestAMachineThatNeverSawTheWorkStillRefusesACompletedAction(t *testing.T) {
	deps := emptyLogExecuteDeps(t)

	frame, err := ExecuteDispatch(context.Background(), deps, ExecuteRequest{
		DispatchID: "d-1", Attempt: 2, ActionID: "already-ran",
		Idempotent: true, Outcome: OutcomeSucceeded, Resume: resumeFixture(),
	})
	if err != nil {
		t.Fatalf("executing a carried-completed action: %v", err)
	}
	if frame.Outcome != OutcomeRefused {
		t.Fatalf("outcome = %q, want %q; the action ran a second time on a machine that could not "+
			"know it had already run", frame.Outcome, OutcomeRefused)
	}
}

// TestTheCarriedRefusalDoesNotRecordTheActionLocally pins the rule behind
// that refusal: the action ran SOMEWHERE ELSE, and recording it here would
// make a later, genuine re-delivery indistinguishable from this one.
func TestTheCarriedRefusalDoesNotRecordTheActionLocally(t *testing.T) {
	deps := emptyLogExecuteDeps(t)
	if _, err := ExecuteDispatch(context.Background(), deps, ExecuteRequest{
		DispatchID: "d-1", Attempt: 2, ActionID: "already-ran",
		Idempotent: true, Outcome: OutcomeSucceeded, Resume: resumeFixture(),
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// The same action with NO carried resume point must now be admitted:
	// if the refusal above had reserved it, this would refuse too.
	frame, err := ExecuteDispatch(context.Background(), deps, ExecuteRequest{
		DispatchID: "d-1", Attempt: 3, ActionID: "already-ran",
		Idempotent: true, Outcome: OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("execute without a resume point: %v", err)
	}
	if frame.Outcome != OutcomeSucceeded {
		t.Errorf("outcome = %q, want %q; the carried refusal wrote the action into this node's "+
			"own log", frame.Outcome, OutcomeSucceeded)
	}
}

// TestAnOperationTheResumePointDoesNotNameStillRuns is the other half:
// the carried list must not become a blanket refusal.
func TestAnOperationTheResumePointDoesNotNameStillRuns(t *testing.T) {
	deps := emptyLogExecuteDeps(t)
	frame, err := ExecuteDispatch(context.Background(), deps, ExecuteRequest{
		DispatchID: "d-1", Attempt: 2, ActionID: "never-ran",
		Idempotent: true, Outcome: OutcomeSucceeded, Resume: resumeFixture(),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if frame.Outcome != OutcomeSucceeded {
		t.Errorf("outcome = %q, want %q; work the lost attempt never did was skipped",
			frame.Outcome, OutcomeSucceeded)
	}
}

// TestTheResumePointSurvivesTheWire proves the fields the node reads are
// the fields the controller set, over the JSON the claim verb actually
// emits — not over an in-process struct copy.
func TestTheResumePointSurvivesTheWire(t *testing.T) {
	encoded, err := json.Marshal(ClaimResponse{
		DispatchID: "d-1", Attempt: 2, Branch: "dispatch/d-1/2",
		Resume: ResumePoint{EntityID: "job-1", Seq: 11,
			CompletedOperations: []string{"a", "b"}, FromScratch: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	var back ClaimResponse
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if back.Resume.Seq != 11 || len(back.Resume.CompletedOperations) != 2 {
		t.Fatalf("resume point did not survive the wire: %s", encoded)
	}

	// A genuine cold start says so, and is not inferred from a zero
	// sequence — the two mean different things and the field exists to
	// keep them apart.
	cold, err := json.Marshal(ResumePoint{EntityID: "job-1", FromScratch: true})
	if err != nil {
		t.Fatal(err)
	}
	var coldBack ResumePoint
	if err := json.Unmarshal(cold, &coldBack); err != nil {
		t.Fatal(err)
	}
	if !coldBack.FromScratch {
		t.Errorf("a cold start arrived as a resume: %s", cold)
	}
}

// claimWhenOfferedTo is claimWhenOffered for an arbitrary node id.
func claimWhenOfferedTo(t *testing.T, reg *rpc.Registry, nodeID string, into *ClaimResponse) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, errObj := callVerb(t, reg, DispatchClaimMethod, `{"node_id":"`+nodeID+`"}`)
		if errObj == nil {
			remarshal(t, res, into)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the waiting dispatch was never offered to node %q", nodeID)
}

// nowForTest is the instant a minted attempt is stamped with.
func nowForTest() time.Time { return time.Now() }

// emptyLogExecuteDeps builds a node whose durable action log has never
// seen anything — the machine a re-queue actually lands on.
func emptyLogExecuteDeps(t *testing.T) ExecuteDeps {
	t.Helper()
	deps, _ := executeHarness(t)
	return deps
}

// TestTheReplacementAttemptCarriesTheResumePoint is the mutation proof for
// ShipWithRecovery's own half of the carriage. The claim tests above prove
// an attempt's resume point reaches the node; this proves the re-queue
// puts one there. Without it the whole chain is a field nobody sets.
func TestTheReplacementAttemptCarriesTheResumePoint(t *testing.T) {
	ship, recovery, rec, head := recoveryHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
	})
	caller, ok := ship.Caller.(*recordingCaller)
	if !ok {
		t.Fatalf("harness caller is %T, want the recording caller", ship.Caller)
	}
	caller.failFirst = errors.New("connection reset")

	if _, err := ShipWithRecovery(context.Background(), ship, recovery, rec,
		recoveryRequest(head)); err != nil {
		t.Fatalf("a lost node was reported as a failed dispatch: %v", err)
	}
	if len(caller.seen) < 2 {
		t.Fatalf("%d attempts were shipped, want the lost one and its replacement", len(caller.seen))
	}

	first, replacement := caller.seen[0], caller.seen[len(caller.seen)-1]
	if first.Resume.Seq != 0 || len(first.Resume.CompletedOperations) != 0 {
		t.Errorf("the FIRST attempt carried a resume point: %+v", first.Resume)
	}
	// The harness's continuity reader holds one record at sequence 3,
	// operation op-1.
	if replacement.Resume.Seq != 3 {
		t.Errorf("replacement resume seq = %d, want 3; the plan's resume point was discarded",
			replacement.Resume.Seq)
	}
	if !replacement.Resume.Completed("op-1") {
		t.Errorf("the replacement carried no completed operations: %+v", replacement.Resume)
	}
	if replacement.Resume.FromScratch {
		t.Error("a replacement with a recorded history was shipped as a cold start")
	}
}
