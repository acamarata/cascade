package nodes

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// upChannel returns a fixture whose harness node has a complete route and
// an up tunnel under a running Reconcile.
func upChannel(t *testing.T) *channelFixture {
	t.Helper()
	f := newChannelFixture(t)
	setRoute(t, f.h.records, f.h.nodeID, testRoute())
	f.start(t)
	waitChannelState(t, f.m, f.h.nodeID, TunnelUp, true)
	return f
}

// assertStoppedOnChange applies change to an up node, fires exactly one
// reconcile tick and requires the tunnel gone, Call refused, and claim and
// heartbeat from that node refused for reason.
func assertStoppedOnChange(ctx context.Context, t *testing.T, reason string, change func(*nodeFacingHarness) error) {
	t.Helper()
	f := upChannel(t)
	id := f.h.nodeID
	f.h.publish(t)
	if err := change(f.h); err != nil {
		t.Fatal(err)
	}
	f.ticker.fire()
	waitChannelState(t, f.m, id, TunnelDown, false)
	assertCascadeErr(t, reason+" call", f.nc.Call(ctx, id, "node.ping", nil, nil), cascade.KindUnavailable, "node channel: no tunnel up for "+id)
	want := `nodes: node "` + id + `" is ` + reason
	got := nodeFacingCall(ctx, t, f.h.f, id, DispatchClaimMethod, ClaimRequest{NodeID: id})
	assertRefused(t, reason+" claim", got, cascade.KindPermissionDenied, want)
	got = nodeFacingCall(ctx, t, f.h.f, id, nodeHeartbeatMethod, f.h.build(1))
	assertRefused(t, reason+" heartbeat", got, cascade.KindPermissionDenied, want)
	if !claimStillOpen(f.h.rv, "d1") {
		t.Fatalf("%s: a refused claim consumed the published dispatch", reason)
	}
}

// TestRevokedNodeChannelStopped: a revoked, removed or drained node's
// tunnel is down within one reconcile tick and the revoked or removed
// node's claim and heartbeat refuse; a newly enrolled routed node gets a
// tunnel on the next record-store change notification.
func TestRevokedNodeChannelStopped(t *testing.T) {
	ctx := context.Background()
	assertStoppedOnChange(ctx, t, "revoked", func(h *nodeFacingHarness) error { _, err := h.records.Revoke(h.nodeID); return err })
	assertStoppedOnChange(ctx, t, "not enrolled", func(h *nodeFacingHarness) error { return h.records.Remove(h.nodeID) })

	f := upChannel(t)
	if _, err := f.h.records.Drain(f.h.nodeID); err != nil {
		t.Fatal(err)
	}
	f.ticker.fire()
	waitChannelState(t, f.m, f.h.nodeID, TunnelDown, false)

	ident, _, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.records.Enroll(ident, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	setRoute(t, f.h.records, ident.NodeID, testRoute())
	f.changes <- struct{}{} // a record-store change runs a pass without waiting for the tick
	waitChannelState(t, f.m, ident.NodeID, TunnelUp, true)
	if _, ok := f.m.State(f.h.nodeID); ok {
		t.Fatal("the drained node's tunnel came back on the enroll tick")
	}
}
