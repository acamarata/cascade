package nodes

// Purpose: the calling node is the channel identity and nothing else. Over
// the node-facing registry a heartbeat, claim or report naming another node
// is refused with no state change, and the claim handler on its own claims
// for the channel node, never for the node its params name.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mismatchMessage is the exact wire message of the channel refusal.
var mismatchMessage = cascade.New(cascade.KindPermissionDenied, "channel node mismatch").Error()

// dispatchOn sends method with params straight into reg under ctx.
func dispatchOn(ctx context.Context, t *testing.T, reg *rpc.Registry, method string, params any) (any, *rpc.ErrorObject) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw, ID: json.RawMessage("1")})
}

// assertMismatch checks errObj is the channel refusal, by kind and message.
func assertMismatch(t *testing.T, name string, errObj *rpc.ErrorObject) {
	t.Helper()
	if errObj == nil || errObj.Code != cascade.KindPermissionDenied.JSONRPCCode() || errObj.Message != mismatchMessage {
		t.Errorf("%s: got %+v, want %d %q", name, errObj, cascade.KindPermissionDenied.JSONRPCCode(), mismatchMessage)
	}
}

// otherNode enrolls node B beside the harness node A, publishes an attempt
// for B and returns B's id, a valid B heartbeat and B's report waiter.
func otherNode(t *testing.T, h *nodeFacingHarness) (string, HeartbeatFrame, <-chan DispatchFrame) {
	t.Helper()
	id, priv, err := GenerateIdentity(strings.NewReader(strings.Repeat("b", 64)))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := h.records.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := h.rv.publish(id.NodeID, Attempt{DispatchID: "d-b", Attempt: 1, NodeID: id.NodeID, Branch: "dispatch/d-b/1"})
	if err != nil {
		t.Fatal(err)
	}
	return id.NodeID, signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 1), waiter
}

// TestNodeFacingRefusesChannelMismatch: over NodeFacing.Registry with A's
// channel identity, a heartbeat, claim and report naming B are refused and
// change nothing; the same calls with no channel are refused; A's claim on
// A's channel gets A's attempt. The claim handler alone claims for the
// channel node only.
func TestNodeFacingRefusesChannelMismatch(t *testing.T) {
	ctx := context.Background()
	h := newNodeFacingHarness(t)
	h.publish(t)
	b, beat, waiter := otherNode(t, h)
	calls := []struct {
		name, method string
		params       any
	}{
		{"heartbeat", nodeHeartbeatMethod, beat},
		{"claim", DispatchClaimMethod, ClaimRequest{NodeID: b}},
		{"report", DispatchReportMethod, DispatchFrame{NodeID: b, DispatchID: "d-b", Attempt: 1}},
	}
	onA := WithChannelIdentity(ctx, ChannelIdentity{NodeID: h.nodeID})
	for _, c := range calls {
		_, errObj := dispatchOn(onA, t, h.f.Registry(), c.method, c.params)
		assertMismatch(t, c.name+" naming B on A's channel", errObj)
		_, errObj = dispatchOn(ctx, t, h.f.Registry(), c.method, c.params)
		assertMismatch(t, c.name+" with no channel", errObj)
	}
	assertNothingMoved(t, h, b, waiter)
	res, errObj := dispatchOn(onA, t, h.f.Registry(), DispatchClaimMethod, ClaimRequest{NodeID: h.nodeID})
	var claim ClaimResponse
	if errObj != nil {
		t.Fatalf("A's claim on A's channel refused: %+v", errObj)
	}
	remarshal(t, res, &claim)
	if claim.DispatchID != "d1" || !claimStillOpen(h.rv, "d-b") {
		t.Fatalf("A's claim got %+v, want A's attempt d1 with B's still open", claim)
	}
	t.Run("claim handler alone", assertClaimHandlerUsesChannel)
}

// assertNothingMoved: no handler ran, neither node's LastSeen moved, both
// attempts are unclaimed and no report reached B's waiter.
func assertNothingMoved(t *testing.T, h *nodeFacingHarness, b string, waiter <-chan DispatchFrame) {
	t.Helper()
	if n := h.sawCount(); n != 0 {
		t.Errorf("%d refused call(s) reached a handler", n)
	}
	for _, id := range []string{h.nodeID, b} {
		if rec, err := h.records.Get(id); err != nil || !rec.LastSeen.IsZero() {
			t.Errorf("node %s LastSeen = %v (%v), want untouched", id, rec.LastSeen, err)
		}
	}
	if !claimStillOpen(h.rv, "d1") || !claimStillOpen(h.rv, "d-b") {
		t.Error("a refused claim consumed a published attempt")
	}
	select {
	case f := <-waiter:
		t.Errorf("a refused report was delivered: %+v", f)
	default:
	}
}

// assertClaimHandlerUsesChannel drives RegisterDispatchNodeHandlers with no
// guard in front: the handler itself refuses a claim naming another node or
// arriving with no channel, and claims for the channel node.
func assertClaimHandlerUsesChannel(t *testing.T) {
	ctx := context.Background()
	rv := NewRendezvous()
	reg := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(reg, rv)
	if _, err := rv.publish("node-b", Attempt{DispatchID: "d-b", Attempt: 1, NodeID: "node-b"}); err != nil {
		t.Fatal(err)
	}
	onA := WithChannelIdentity(ctx, ChannelIdentity{NodeID: "node-a"})
	_, errObj := dispatchOn(onA, t, reg, DispatchClaimMethod, ClaimRequest{NodeID: "node-b"})
	assertMismatch(t, "bare handler, B named on A's channel", errObj)
	_, errObj = dispatchOn(ctx, t, reg, DispatchClaimMethod, ClaimRequest{NodeID: "node-b"})
	assertMismatch(t, "bare handler, no channel", errObj)
	if !claimStillOpen(rv, "d-b") {
		t.Fatal("a refused claim consumed B's attempt")
	}
	onB := WithChannelIdentity(ctx, ChannelIdentity{NodeID: "node-b"})
	res, errObj := dispatchOn(onB, t, reg, DispatchClaimMethod, ClaimRequest{NodeID: "node-b"})
	var claim ClaimResponse
	if errObj != nil {
		t.Fatalf("B's claim on B's channel refused: %+v", errObj)
	}
	remarshal(t, res, &claim)
	if claim.DispatchID != "d-b" || claimStillOpen(rv, "d-b") {
		t.Fatalf("B's claim got %+v, want d-b claimed", claim)
	}
}
