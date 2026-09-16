package nodes

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Purpose (this file): the rendezvous's matching rules — which report
//   satisfies which waiting attempt, and which are refused. Every case is
//   driven through the exported surface a node actually reaches.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// waitingAttempt publishes one attempt and returns the rendezvous, the
// attempt, and the Call result channel.
func waitingAttempt(t *testing.T) (*Rendezvous, Attempt, <-chan error, <-chan DispatchFrame) {
	t.Helper()
	r := NewRendezvous()
	attempt := Attempt{DispatchID: "d1", Attempt: 3, NodeID: "n1", Branch: "dispatch/d1/3"}

	errs := make(chan error, 1)
	frames := make(chan DispatchFrame, 1)
	go func() {
		frame, err := r.Call(context.Background(), "n1", attempt)
		frames <- frame
		errs <- err
	}()

	// The claim is the node's first move, and it only succeeds once the
	// attempt is published — so it doubles as the handshake that makes
	// this test deterministic rather than a race the assertions hope wins.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := r.Claim("n1"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the attempt was never published for the node to claim")
		}
	}
	return r, attempt, errs, frames
}

// TestTheNodesReportSatisfiesTheWaitingController is the happy path across
// the two directions: the controller waits, the node claims and reports,
// and the frame the controller gets back is the one the node sent.
func TestTheNodesReportSatisfiesTheWaitingController(t *testing.T) {
	r, attempt, errs, frames := waitingAttempt(t)

	want := DispatchFrame{
		DispatchID: "d1", Attempt: attempt.Attempt, NodeID: "n1",
		ActionID: "a1", Outcome: OutcomeSucceeded, ResultCommit: "abc123",
	}
	if err := r.Report(want); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := <-frames; got.ResultCommit != "abc123" || got.ActionID != "a1" {
		t.Errorf("frame = %+v, want the one the node reported", got)
	}
}

// TestAReportFromASupersededAttemptIsRefused is fencing at the transport
// boundary: a partitioned node that comes back and reports its old attempt
// must not satisfy the controller's wait on the new one.
func TestAReportFromASupersededAttemptIsRefused(t *testing.T) {
	r, attempt, errs, _ := waitingAttempt(t)

	err := r.Report(DispatchFrame{
		DispatchID: "d1", Attempt: attempt.Attempt - 1, NodeID: "n1", Outcome: OutcomeSucceeded,
	})
	if err == nil {
		t.Fatal("a report from a superseded attempt satisfied the controller")
	}
	if !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("error = %v, want it to wrap ErrStaleAttempt", err)
	}

	select {
	case got := <-errs:
		t.Fatalf("the controller stopped waiting on a stale report: %v", got)
	default:
	}
}

// TestAReportFromTheWrongNodeIsRefused proves the dispatch cannot be
// answered by a machine it was never placed on, which would let any
// enrolled node close out another's work.
func TestAReportFromTheWrongNodeIsRefused(t *testing.T) {
	r, attempt, _, _ := waitingAttempt(t)

	err := r.Report(DispatchFrame{
		DispatchID: "d1", Attempt: attempt.Attempt, NodeID: "n2", Outcome: OutcomeSucceeded,
	})
	if err == nil {
		t.Fatal("a node reported a dispatch placed on a different node")
	}
}

// TestAnUnawaitedReportIsRefusedNotDropped is the rule that keeps S-37.T3's
// recovery from having to guess. A node that reports into silence would
// believe it had handed over a result the controller never recorded.
func TestAnUnawaitedReportIsRefusedNotDropped(t *testing.T) {
	r := NewRendezvous()
	err := r.Report(DispatchFrame{DispatchID: "nobody-waiting", Attempt: 1, NodeID: "n1"})
	if err == nil {
		t.Fatal("a report for a dispatch nobody awaits was accepted")
	}
}

// TestASecondReportIsRefused proves a redelivered report does not satisfy
// a second, later wait on the same dispatch id.
func TestASecondReportIsRefused(t *testing.T) {
	r, attempt, errs, _ := waitingAttempt(t)
	frame := DispatchFrame{
		DispatchID: "d1", Attempt: attempt.Attempt, NodeID: "n1", Outcome: OutcomeSucceeded,
	}
	if err := r.Report(frame); err != nil {
		t.Fatalf("first Report: %v", err)
	}
	<-errs

	if err := r.Report(frame); err == nil {
		t.Fatal("a redelivered report was accepted after the dispatch completed")
	}
}

// TestAClaimIsHandedOutOnlyOnce proves a redelivered claim never reaches
// execution — the boundary before the node's own durable action dedup.
func TestAClaimIsHandedOutOnlyOnce(t *testing.T) {
	r := NewRendezvous()
	go func() {
		_, _ = r.Call(context.Background(), "n1",
			Attempt{DispatchID: "d1", Attempt: 1, NodeID: "n1"})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := r.Claim("n1"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the attempt was never published")
		}
	}
	if _, err := r.Claim("n1"); err == nil {
		t.Fatal("the same attempt was claimed twice")
	}
}

// TestAClaimForAnotherNodeIsRefused proves work placed on one machine is
// not handed to whichever node asks first.
func TestAClaimForAnotherNodeIsRefused(t *testing.T) {
	r := NewRendezvous()
	go func() {
		_, _ = r.Call(context.Background(), "n1",
			Attempt{DispatchID: "d1", Attempt: 1, NodeID: "n1"})
	}()
	if _, err := r.Claim("n2"); err == nil {
		t.Fatal("a node claimed a dispatch placed on another node")
	}
}

// TestAnAbandonedWaitReleasesTheDispatch proves a controller that gives up
// does not leave the dispatch id permanently unusable.
func TestAnAbandonedWaitReleasesTheDispatch(t *testing.T) {
	r := NewRendezvous()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Call(ctx, "n1", Attempt{DispatchID: "d1", Attempt: 1, NodeID: "n1"}); err == nil {
		t.Fatal("an already-cancelled wait reported a node result")
	}
	// The next attempt on the same dispatch must be publishable.
	if _, err := r.publish("n1", Attempt{DispatchID: "d1", Attempt: 2, NodeID: "n1"}); err != nil {
		t.Fatalf("the abandoned dispatch was never released: %v", err)
	}
}

// TestTwoWaitersOnOneDispatchAreRefused proves the second is refused rather
// than replacing the first, which would leave a caller waiting forever on a
// channel nothing can answer.
func TestTwoWaitersOnOneDispatchAreRefused(t *testing.T) {
	r := NewRendezvous()
	attempt := Attempt{DispatchID: "d1", Attempt: 1, NodeID: "n1"}
	if _, err := r.publish("n1", attempt); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := r.publish("n1", attempt); err == nil {
		t.Fatal("a second controller began waiting on the same dispatch")
	}
}
