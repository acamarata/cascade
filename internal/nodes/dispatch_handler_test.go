package nodes

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

// Purpose (this file): the RPC verb that makes the dispatch engine
//   reachable from a running program, and the shared-state invariant the
//   whole fencing guarantee rests on.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// dispatchCall parses and dispatches one node.dispatch call.
func dispatchCall(t *testing.T, reg *rpc.Registry, params string) (any, *rpc.ErrorObject) {
	t.Helper()
	raw := `{"jsonrpc":"2.0","method":"` + DispatchMethod + `","params":` + params + `,"id":1}`
	req, errObj := rpc.Parse([]byte(raw))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	return reg.Dispatch(context.Background(), req)
}

// TestTheDispatchVerbShipsThroughTheRealEngine proves the mounted verb
// reaches Ship rather than a handler that only validates and returns.
func TestTheDispatchVerbShipsThroughTheRealEngine(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
	})
	reg := rpc.NewRegistry()
	RegisterDispatchHandler(reg, func(context.Context, string) (ShipDeps, RequeueDeps, DeviceRecord, error) {
		return deps, RequeueDeps{}, rec, nil
	})

	result, errObj := dispatchCall(t, reg,
		`{"dispatch_id":"d1","node_id":"n1","action_id":"a1","head":"`+head+`","sensitivity":"normal"}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	got, ok := result.(DispatchResult)
	if !ok {
		t.Fatalf("result = %T, want DispatchResult", result)
	}
	if got.Commit != head {
		t.Errorf("commit = %q, want %q", got.Commit, head)
	}
	if got.Attempt == 0 {
		t.Error("the result reports no attempt, so a caller cannot fence a retry against it")
	}
	if caller.calls != 1 {
		t.Errorf("the node was called %d times, want 1", caller.calls)
	}
}

// TestTheDispatchVerbRefusesAnIncompleteCall covers each identifier the
// engine cannot proceed without, named at the boundary rather than left to
// surface as a confusing downstream failure.
func TestTheDispatchVerbRefusesAnIncompleteCall(t *testing.T) {
	deps, rec, caller, _ := shipHarness(t, DispatchFrame{})
	reg := rpc.NewRegistry()
	RegisterDispatchHandler(reg, func(context.Context, string) (ShipDeps, RequeueDeps, DeviceRecord, error) {
		return deps, RequeueDeps{}, rec, nil
	})

	for _, params := range []string{
		`{"node_id":"n1","action_id":"a1"}`,
		`{"dispatch_id":"d1","action_id":"a1"}`,
		`{"dispatch_id":"d1","node_id":"n1"}`,
	} {
		if _, errObj := dispatchCall(t, reg, params); errObj == nil {
			t.Errorf("an incomplete call was accepted: %s", params)
		}
	}
	if caller.calls != 0 {
		t.Errorf("the node was reached %d times for calls that never validated", caller.calls)
	}
}

// TestTheControllerFencingRegisterIsSharedAcrossDispatches is the
// invariant the entire fencing guarantee rests on, and it is asserted
// because getting it wrong is SILENT: a register rebuilt per call hands out
// attempt 1 every time, so no attempt ever supersedes another,
// ErrStaleAttempt can never fire, and every fencing test still passes in
// isolation while the running controller has no fencing at all.
func TestTheControllerFencingRegisterIsSharedAcrossDispatches(t *testing.T) {
	d := NewDispatcher()
	runner := testGitRunner{}

	first := d.ShipDepsFor("/repo", "/remote", runner, nil, nil)
	second := d.ShipDepsFor("/repo", "/remote", runner, nil, nil)

	a1 := first.Attempts.Next("d1")
	a2 := second.Attempts.Next("d1")
	if a2 <= a1 {
		t.Fatalf("attempt went %d -> %d across two dispatches; the register is not shared", a1, a2)
	}
	if first.Sequences != second.Sequences {
		t.Error("the sequence store is not shared, so replay refusal resets between dispatches")
	}
}

// TestAStreamedRecordIsFencedAgainstTheShipLegsAttempt proves the two legs
// share one fencing authority. If they did not, a record from a superseded
// attempt would still append while its results were being refused — the
// journal and the outcome would disagree about what happened.
func TestAStreamedRecordIsFencedAgainstTheShipLegsAttempt(t *testing.T) {
	d := NewDispatcher()
	sink := &recordingSink{}
	ship := d.ShipDepsFor("/repo", "/remote", testGitRunner{}, nil, nil)
	stream := d.JournalDepsFor(sink)

	attempt := ship.Attempts.Next("d1")
	if err := StreamJournalRecord(context.Background(), stream, JournalRecord{
		DispatchID: "d1", Attempt: attempt, EntityID: "job-7", OperationID: "op-1",
		Payload: json.RawMessage(`{"step":"build"}`),
	}); err != nil {
		t.Fatalf("a record from the ship leg's own attempt was refused: %v", err)
	}

	// The ship leg supersedes it; the journal leg must refuse the old one.
	ship.Attempts.Next("d1")
	if err := StreamJournalRecord(context.Background(), stream, JournalRecord{
		DispatchID: "d1", Attempt: attempt, EntityID: "job-7", OperationID: "op-2",
		Payload: json.RawMessage(`{"step":"build"}`),
	}); err == nil {
		t.Fatal("the journal leg accepted a record the ship leg had already superseded")
	}
	if len(sink.payloads) != 1 {
		t.Errorf("the store holds %d entries, want only the current attempt's", len(sink.payloads))
	}
}

// newDispatchRegistry mounts the controller entry over resolve.
func newDispatchRegistry(t *testing.T, resolve dispatchHandlerDeps) *rpc.Registry {
	t.Helper()
	reg := rpc.NewRegistry()
	RegisterDispatchHandler(reg, resolve)
	return reg
}
