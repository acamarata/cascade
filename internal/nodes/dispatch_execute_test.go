package nodes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

// Purpose (this file): the NODE-side leg — durable admission, the outcome a
//   duplicate produces, and the signed frame the controller verifies. The
//   duplicate case is the one that matters most: reported as a failure it
//   would invite the retry that duplicates an external side effect.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// executeHarness wires the node leg over a real durable action log and a
// real key, and returns the public half so a frame can be verified.
func executeHarness(t *testing.T) (ExecuteDeps, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	self := Identity{NodeID: DeriveNodeID(pub), PubKey: pub}
	return ExecuteDeps{
		Actions:      NewFileActionLog(t.TempDir()),
		Sign:         ed25519Signer(priv),
		Self:         self,
		EnrollmentID: "enr-1",
		Sequences:    NewSequenceStore(),
	}, pub
}

// goodExecute is a claim every rule admits.
func goodExecute() ExecuteRequest {
	return ExecuteRequest{
		DispatchID: "d1", Attempt: 1, ActionID: "a1",
		Outcome: OutcomeSucceeded, ResultCommit: "abc123",
	}
}

// TestTheNodeSignsWhatItRan is the happy path, asserted on a frame that
// actually verifies against this node's key rather than merely being
// non-empty.
func TestTheNodeSignsWhatItRan(t *testing.T) {
	deps, pub := executeHarness(t)

	frame, err := ExecuteDispatch(context.Background(), deps, goodExecute())
	if err != nil {
		t.Fatalf("ExecuteDispatch: %v", err)
	}
	if frame.Outcome != OutcomeSucceeded || frame.ResultCommit != "abc123" {
		t.Errorf("frame = %+v", frame)
	}
	if frame.Sequence == 0 {
		t.Error("the frame carries no sequence, so a replay could not be refused")
	}
	sig, err := base64.StdEncoding.DecodeString(frame.SignatureB64)
	if err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
	if !ed25519.Verify(pub, frame.signingPayload(), sig) {
		t.Fatal("the frame does not verify against the node's own key")
	}

	// STATE, not an event: the log records the terminal outcome.
	log, ok := deps.Actions.(*FileActionLog)
	if !ok {
		t.Fatal("harness did not use the durable log")
	}
	if outcome, recorded := log.Outcome("a1"); !recorded || outcome != OutcomeSucceeded {
		t.Errorf("recorded outcome = %q (recorded=%v), want succeeded", outcome, recorded)
	}
}

// TestTheNodeLegAnswersARedeliveryWithARefusal is the dedup guarantee as
// the whole node leg produces it (dispatch_test.go covers ReserveAction
// alone). The second delivery must come back as a REFUSAL rather than an
// error: the work already happened, and an error would invite the retry
// that duplicates the side effect.
func TestTheNodeLegAnswersARedeliveryWithARefusal(t *testing.T) {
	deps, _ := executeHarness(t)
	ctx := context.Background()

	if _, err := ExecuteDispatch(ctx, deps, goodExecute()); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	frame, err := ExecuteDispatch(ctx, deps, goodExecute())
	if err != nil {
		t.Fatalf("a redelivered action returned an error, which invites a retry: %v", err)
	}
	if frame.Outcome != OutcomeRefused {
		t.Errorf("redelivery outcome = %q, want refused", frame.Outcome)
	}
}

// TestTheNodesSequenceAdvancesPerFrame proves two frames never carry the
// same sequence, which is what lets the controller refuse a replay.
func TestTheNodesSequenceAdvancesPerFrame(t *testing.T) {
	deps, _ := executeHarness(t)
	ctx := context.Background()

	first, err := ExecuteDispatch(ctx, deps, goodExecute())
	if err != nil {
		t.Fatal(err)
	}
	second := goodExecute()
	second.ActionID, second.DispatchID = "a2", "d2"
	next, err := ExecuteDispatch(ctx, deps, second)
	if err != nil {
		t.Fatal(err)
	}
	if next.Sequence <= first.Sequence {
		t.Fatalf("sequence went %d -> %d; a replayed frame could not be refused", first.Sequence, next.Sequence)
	}
}

// TestAnIncompleteOrUnknownClaimIsRefused covers every field the frame
// cannot be built without, and the outcome vocabulary.
func TestAnIncompleteOrUnknownClaimIsRefused(t *testing.T) {
	deps, _ := executeHarness(t)
	for _, tc := range []struct {
		why    string
		mutate func(*ExecuteRequest)
	}{
		{"no dispatch id", func(r *ExecuteRequest) { r.DispatchID = "" }},
		{"no action id", func(r *ExecuteRequest) { r.ActionID = "" }},
		{"no attempt", func(r *ExecuteRequest) { r.Attempt = 0 }},
		{"an outcome this build does not define", func(r *ExecuteRequest) { r.Outcome = "hibernating" }},
	} {
		req := goodExecute()
		tc.mutate(&req)
		if _, err := ExecuteDispatch(context.Background(), deps, req); err == nil {
			t.Errorf("a claim with %s was executed", tc.why)
		}
	}
}

// TestASigningFailureIsSurfaced proves a node that cannot sign reports it
// rather than returning an unsigned frame the controller would refuse with
// a misleading reason.
func TestASigningFailureIsSurfaced(t *testing.T) {
	deps, _ := executeHarness(t)
	deps.Sign = func(context.Context, string, []byte) ([]byte, error) {
		return nil, errors.New("keystore locked")
	}
	if _, err := ExecuteDispatch(context.Background(), deps, goodExecute()); err == nil {
		t.Fatal("a frame that could not be signed was returned")
	}
}

// TestTheExecuteLegRefusesWhenUnwired proves a half-built node leg fails as
// a typed error rather than a nil-pointer panic mid-dispatch.
func TestTheExecuteLegRefusesWhenUnwired(t *testing.T) {
	if _, err := ExecuteDispatch(context.Background(), ExecuteDeps{}, goodExecute()); err == nil {
		t.Fatal("a node leg with no sequence store executed a dispatch")
	}
}

// TestTheNodeVerbsAreMountedAndDecode drives the three registry-mounted
// verbs, which is the only path a real caller reaches them by.
func TestTheNodeVerbsAreMountedAndDecode(t *testing.T) {
	deps, _ := executeHarness(t)
	reg := rpc.NewRegistry()
	RegisterExecuteHandler(reg, deps)

	rv := NewRendezvous()
	RegisterDispatchNodeHandlers(reg, rv)

	sink := &recordingSink{}
	dispatcher := NewDispatcher()
	RegisterDispatchJournalHandler(reg, dispatcher.JournalDepsFor(sink))

	for _, tc := range []struct {
		method, params string
		wantErr        bool
	}{
		{ExecuteMethod, `{"dispatch_id":"d1","attempt":1,"action_id":"a1","outcome":"succeeded"}`, false},
		{ExecuteMethod, `{"dispatch_id":"d1"}`, true},
		{DispatchClaimMethod, `{"node_id":"nobody"}`, true},
		{DispatchReportMethod, `{"dispatch_id":"unawaited","attempt":1,"node_id":"n1"}`, true},
		{DispatchJournalMethod, `{"dispatch_id":"d9","attempt":1,"entity_id":"e","operation_id":"o"}`, true},
	} {
		raw := `{"jsonrpc":"2.0","method":"` + tc.method + `","params":` + tc.params + `,"id":1}`
		req, parseErr := rpc.Parse([]byte(raw))
		if parseErr != nil {
			t.Fatalf("Parse(%s): %+v", tc.method, parseErr)
		}
		_, errObj := reg.Dispatch(context.Background(), req)
		if tc.wantErr && errObj == nil {
			t.Errorf("%s accepted %s", tc.method, tc.params)
		}
		if !tc.wantErr && errObj != nil {
			t.Errorf("%s refused a valid call: %+v", tc.method, errObj)
		}
	}
}

// TestEveryVerbRefusesEmptyParams covers the shared params decoder.
func TestEveryVerbRefusesEmptyParams(t *testing.T) {
	deps, _ := executeHarness(t)
	reg := rpc.NewRegistry()
	RegisterExecuteHandler(reg, deps)
	RegisterDispatchNodeHandlers(reg, NewRendezvous())

	for _, method := range []string{ExecuteMethod, DispatchClaimMethod, DispatchReportMethod} {
		req, parseErr := rpc.Parse([]byte(`{"jsonrpc":"2.0","method":"` + method + `","id":1}`))
		if parseErr != nil {
			t.Fatalf("Parse: %+v", parseErr)
		}
		if _, errObj := reg.Dispatch(context.Background(), req); errObj == nil {
			t.Errorf("%s accepted a call with no params", method)
		}
	}
}
