package nodes

// Purpose (this file): the three verbs a NODE calls back on — claim,
//   report and the journal stream — driven over a real rpc.Registry with
//   real JSON-RPC frames.
// WHY IT IS ITS OWN FILE: dispatch_handler_test.go covers the controller's
//   entry verb. These are the other direction, and they were mounted with
//   no test at all: RegisterDispatchJournalHandler sat at 71% and the two
//   node handlers' refusal branches were never reached, so nothing proved a
//   malformed call from a node is refused rather than decoded into a zero
//   value and acted on.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
)

// callVerb dispatches one real JSON-RPC frame for method.
func callVerb(t *testing.T, reg *rpc.Registry, method, params string) (any, *rpc.ErrorObject) {
	t.Helper()
	raw := `{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `,"id":1}`
	req, errObj := rpc.Parse([]byte(raw))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	return reg.Dispatch(context.Background(), req)
}

// TestANodeClaimsAnAttemptAndReportsItBack walks the node's whole callback
// path over the mounted verbs: claim the waiting attempt, then report its
// outcome. Both must reach the rendezvous, not a handler that validates and
// returns a shape.
func TestANodeClaimsAnAttemptAndReportsItBack(t *testing.T) {
	rv := NewRendezvous()
	reg := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(reg, rv)

	done := make(chan error, 1)
	go func() {
		_, err := rv.Call(context.Background(), "n1", Attempt{
			DispatchID: "d1", Attempt: 1, NodeID: "n1", Branch: DispatchBranch("d1", 1),
		})
		done <- err
	}()

	var claim ClaimResponse
	claimWhenOffered(t, reg, &claim)
	if claim.DispatchID != "d1" || claim.Attempt != 1 {
		t.Fatalf("claim = %+v, want the waiting attempt", claim)
	}
	if claim.Branch != DispatchBranch("d1", 1) {
		t.Errorf("branch = %q, want the fenced branch for this attempt", claim.Branch)
	}

	report := `{"dispatch_id":"d1","attempt":1,"node_id":"n1","outcome":"succeeded","action_id":"a1"}`
	if _, errObj := callVerb(t, reg, DispatchReportMethod, report); errObj != nil {
		t.Fatalf("report was refused: %+v", errObj)
	}
	if err := <-done; err != nil {
		t.Fatalf("the waiting controller never saw the report: %v", err)
	}
}

// TestANodeVerbWithNoParamsIsRefusedByName covers the boundary decode. A
// verb that accepted empty params would unmarshal into a zero value and act
// on a claim from node "" or a report for dispatch "".
func TestANodeVerbWithNoParamsIsRefusedByName(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(reg, NewRendezvous())
	RegisterDispatchJournalHandler(reg, JournalStreamDeps{
		Attempts: NewAttemptRegister(), Sink: &recordingSink{},
	})

	for _, method := range []string{DispatchClaimMethod, DispatchReportMethod, DispatchJournalMethod} {
		_, errObj := callVerb(t, reg, method, `null`)
		if errObj == nil {
			t.Errorf("%s accepted a call with no params", method)
			continue
		}
		if !strings.Contains(errObj.Message, method) {
			t.Errorf("%s: error %q does not name the verb that refused", method, errObj.Message)
		}
	}
}

// TestANodeVerbWithUndecodableParamsIsRefused is the other decode branch: a
// params object of the wrong SHAPE, which is what a version-skewed node
// sends.
func TestANodeVerbWithUndecodableParamsIsRefused(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(reg, NewRendezvous())

	if _, errObj := callVerb(t, reg, DispatchClaimMethod, `{"node_id":{"was":"a string"}}`); errObj == nil {
		t.Fatal("a claim whose node id is an object was accepted")
	}
}

// TestTheJournalVerbAppendsThroughTheRealStore proves the mounted verb
// reaches StreamJournalRecord and the store behind it, and that a record
// from a superseded attempt is refused at the verb rather than landing.
func TestTheJournalVerbAppendsThroughTheRealStore(t *testing.T) {
	deps, sink, attempt := streamHarness(t)
	reg := rpc.NewRegistry()
	RegisterDispatchJournalHandler(reg, deps)

	rec, err := json.Marshal(goodRecord(attempt))
	if err != nil {
		t.Fatal(err)
	}
	if _, errObj := callVerb(t, reg, DispatchJournalMethod, string(rec)); errObj != nil {
		t.Fatalf("a good record was refused: %+v", errObj)
	}
	if len(sink.payloads) != 1 {
		t.Fatalf("the store holds %d record(s), want 1", len(sink.payloads))
	}

	stale := goodRecord(attempt)
	deps.Attempts.Next(stale.DispatchID)
	staleRaw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, errObj := callVerb(t, reg, DispatchJournalMethod, string(staleRaw)); errObj == nil {
		t.Error("the verb appended a record from a superseded attempt")
	}
	if len(sink.payloads) != 1 {
		t.Errorf("the store holds %d record(s) after a refused append, want 1", len(sink.payloads))
	}
}

// claimWhenOffered polls the claim verb until the waiting dispatch is
// offered. Polled rather than slept on: Call publishes the attempt from
// another goroutine, and a fixed sleep is either flaky or slow.
func claimWhenOffered(t *testing.T, reg *rpc.Registry, into *ClaimResponse) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, errObj := callVerb(t, reg, DispatchClaimMethod, `{"node_id":"n1"}`)
		if errObj == nil {
			remarshal(t, res, into)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the waiting dispatch was never offered to the claiming node")
}

// remarshal round-trips a handler's returned value into out, which is what
// the JSON-RPC transport does to it anyway.
func remarshal(t *testing.T, from any, out any) {
	t.Helper()
	raw, err := json.Marshal(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}
