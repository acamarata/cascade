package nodes

// Purpose (this file): the held state itself, and the single-side-effect
//   guarantee the recovery path rests on.
// WHY THE TWO BELONG TOGETHER: they are the two halves of one promise.
//   Durable dedup makes a redelivery to the SAME node harmless. It cannot
//   help when the replacement is a DIFFERENT machine, whose log is empty —
//   which is exactly why a non-idempotent action is held instead of
//   re-queued, and why the hold has to be a first-class state rather than a
//   dropped dispatch (R-21.221).
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T3.

import (
	"context"
	"strings"
	"testing"
)

// TestAHeldOutcomeAlwaysReturnsTheTypedRefusal proves the caller cannot
// proceed past a hold. Success here is still an error, deliberately: a
// holding function that returned nil on the happy path would let a caller
// carry on as though the dispatch had finished.
func TestAHeldOutcomeAlwaysReturnsTheTypedRefusal(t *testing.T) {
	filer := &recordingFiler{}
	err := HoldUnknownOutcome(context.Background(), filer, HeldOutcome{
		DispatchID: "d1", ActionID: "a1", NodeID: "n1", Attempt: 3,
		Signal: LossHeartbeat, Reason: heldReason(LossHeartbeat, "n1"),
	})
	if err == nil {
		t.Fatal("a successful hold reported success; the caller would carry on")
	}
	if !strings.Contains(err.Error(), "d1") || !strings.Contains(err.Error(), "a1") {
		t.Errorf("error = %v, want it to name the dispatch and the action", err)
	}
	if len(filer.held) != 1 {
		t.Fatalf("%d entr(y/ies) filed, want 1", len(filer.held))
	}
	if filer.held[0].Attempt != 3 {
		t.Errorf("held attempt = %d, want the attempt that was lost", filer.held[0].Attempt)
	}
}

// TestAnUnidentifiedHoldIsRefused covers the boundary. Without the action
// id nobody reconciling this item can ask the node's dedup log whether the
// work ran, which is the only question the item exists to answer.
func TestAnUnidentifiedHoldIsRefused(t *testing.T) {
	filer := &recordingFiler{}
	for _, held := range []HeldOutcome{
		{ActionID: "a1"},
		{DispatchID: "d1"},
	} {
		if err := HoldUnknownOutcome(context.Background(), filer, held); err == nil {
			t.Errorf("an unidentified hold was accepted: %+v", held)
		}
	}
	if len(filer.held) != 0 {
		t.Errorf("%d unidentified entr(y/ies) reached the attention queue", len(filer.held))
	}
}

// TestTheHeldReasonNamesTheSignalAndTheNode pins what a person reads. A
// held item whose reason does not say what happened is a queue entry
// somebody has to reverse-engineer.
func TestTheHeldReasonNamesTheSignalAndTheNode(t *testing.T) {
	reason := heldReason(LossTunnel, "worker-3")
	for _, want := range []string{"worker-3", string(LossTunnel), "idempotent"} {
		if !strings.Contains(reason, want) {
			t.Errorf("held reason %q does not mention %q", reason, want)
		}
	}
}

// TestReplacementDedupSingleSideEffect is the guarantee that makes an
// automatic re-queue safe when the replacement is the SAME node coming
// back — the ordinary case for a restart or a reconnect.
//
// The side effect is counted only where the node would actually incur it:
// after the reservation is granted. A durable log that survives the
// restart means the second delivery never gets that far.
func TestReplacementDedupSingleSideEffect(t *testing.T) {
	dataDir := t.TempDir()
	var sideEffects int

	run := func() error {
		// A fresh log over the same directory is the restart: in-process
		// state is gone, the durable record is not.
		if err := ReserveAction(context.Background(), NewFileActionLog(dataDir),
			Action{ID: "a1", Idempotent: true}); err != nil {
			return err
		}
		sideEffects++
		return nil
	}

	if err := run(); err != nil {
		t.Fatalf("the first delivery was refused: %v", err)
	}
	err := run()
	if err == nil {
		t.Fatal("the redelivered action ran a second time")
	}
	if sideEffects != 1 {
		t.Fatalf("%d side effect(s) across two deliveries, want exactly 1", sideEffects)
	}
	if !strings.Contains(err.Error(), "already been executed") {
		t.Errorf("error = %v, want the duplicate refusal", err)
	}
}

// TestADifferentNodeCannotDedupIsWhyHoldsExist states the limit of the
// guarantee above, in code, because it is the reason the held state exists
// at all — and a reader who assumed dedup covered the cross-node case
// would conclude that holds are over-cautious.
func TestADifferentNodeCannotDedupIsWhyHoldsExist(t *testing.T) {
	original, replacement := t.TempDir(), t.TempDir()
	action := Action{ID: "a1"}

	if err := ReserveAction(context.Background(), NewFileActionLog(original), action); err != nil {
		t.Fatalf("the original node refused its own first delivery: %v", err)
	}
	if err := ReserveAction(context.Background(), NewFileActionLog(replacement), action); err != nil {
		t.Fatalf("a replacement node's empty log refused a new action: %v", err)
	}
	// Both reservations succeeded, which is the point: dedup is per-node.
	// So the recovery path must NOT auto-re-queue this action, and does
	// not — it is not declared idempotent.
	deps, filer := requeueHarness(t)
	req := idempotentWork()
	req.Action = action

	if _, err := PlanRequeue(context.Background(), deps, req, LossHeartbeat); err == nil {
		t.Fatal("a non-idempotent action was re-queued across nodes, where dedup cannot protect it")
	}
	if len(filer.held) != 1 {
		t.Fatalf("%d held entr(y/ies), want 1: the cross-node case must be held, not re-queued",
			len(filer.held))
	}
}
