package nodes

// Purpose: drain fencing on the reconcile loop. A drained node keeps its
//   tunnel while a dispatch published on it awaits its report, so the
//   heartbeat and that report still arrive; a revoked node loses its tunnel
//   within one tick whatever is in flight.

import (
	"context"
	"testing"
)

// inFlightChannel returns an up channel whose reconcile deps read the
// harness rendezvous, with dispatch d1 published on the harness node.
func inFlightChannel(t *testing.T) (*channelFixture, <-chan DispatchFrame) {
	t.Helper()
	f := newChannelFixture(t)
	f.nc.mu.Lock()
	d := f.nc.deps
	f.nc.mu.Unlock()
	d.Rendezvous = f.h.rv
	f.nc.SetReconcileDeps(d)
	setRoute(t, f.h.records, f.h.nodeID, testRoute())
	f.start(t)
	waitChannelState(t, f.m, f.h.nodeID, TunnelUp, true)
	return f, f.h.publish(t)
}

// settlePass returns once the pass after the first tick has finished: the
// ticker holds one tick, so the third send waits for the loop to take the
// second, which it does only after that pass.
func settlePass(f *channelFixture) {
	f.ticker.fire()
	f.ticker.fire()
	f.ticker.fire()
}

// TestDrainedNodeKeepsTunnelUntilReported: drained with d1 in flight, the
// tunnel stays up across passes and the heartbeat and report land; once d1
// has reported, the next pass stops the tunnel.
func TestDrainedNodeKeepsTunnelUntilReported(t *testing.T) {
	ctx := context.Background()
	f, waiter := inFlightChannel(t)
	id := f.h.nodeID
	if _, err := f.h.records.Drain(id); err != nil {
		t.Fatal(err)
	}
	settlePass(f)
	if state, ok := f.m.State(id); !ok || state != TunnelUp {
		t.Fatalf("drained node with work in flight: tunnel %v (registered %v), want up", state, ok)
	}
	if got := nodeFacingCall(ctx, t, f.h.f, id, nodeHeartbeatMethod, f.h.build(1)); got.Error != nil {
		t.Fatalf("drained heartbeat refused: %+v", got.Error)
	}
	report := DispatchFrame{NodeID: id, DispatchID: "d1", Attempt: 1}
	if got := nodeFacingCall(ctx, t, f.h.f, id, DispatchReportMethod, report); got.Error != nil {
		t.Fatalf("drained in-flight report refused: %+v", got.Error)
	}
	if frame := <-waiter; frame.DispatchID != "d1" {
		t.Fatalf("report delivered %+v", frame)
	}
	f.h.rv.withdraw("d1") // what Rendezvous.Call does once the report reaches it
	f.ticker.fire()
	waitChannelState(t, f.m, id, TunnelDown, false)
}

// TestRevokedNodeWithWorkInFlightStopped: work in flight never keeps a
// revoked node's tunnel past one tick.
func TestRevokedNodeWithWorkInFlightStopped(t *testing.T) {
	f, _ := inFlightChannel(t)
	if _, err := f.h.records.Drain(f.h.nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.records.Revoke(f.h.nodeID); err != nil {
		t.Fatal(err)
	}
	f.ticker.fire()
	waitChannelState(t, f.m, f.h.nodeID, TunnelDown, false)
}
