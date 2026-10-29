package daemon

// Purpose: the refresh loop shows enrolled nodes beside the controller and, over unchanged
// sources, publishes nothing (UpdateNodes would otherwise drop and re-add the self node
// every tick). The fake ticker drives each tick.
// SPORT: internal/daemon (ADD, P1-CAP-05).

import (
	"crypto/rand"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/nodes"
)

func TestFleetCapacityRefreshShowsNodesAndIdleIsQuiet(t *testing.T) {
	g, r, capT := newCapRig(t)
	p := r.CapacityParts()
	id, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Nodes.Enroll(id, nodes.TierWorkerTrusted); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	capT.tick(t)
	snap := p.Compositor.Snapshot()
	if _, ok := snap.Nodes[id.NodeID]; !ok {
		t.Fatalf("enrolled node %s missing from the snapshot after a refresh: %v", id.NodeID, snap.Nodes)
	}
	if _, ok := snap.Nodes[localControllerNodeID]; !ok {
		t.Fatalf("the controller slot is missing beside the enrolled node: %v", snap.Nodes)
	}
	// UpdateNodes stamps the slot; past the TTL an unstamped slot would read unknown.
	g.clock.Advance(fleetCapacityTTL)
	capT.tick(t)
	if got := p.Compositor.Snapshot().Nodes[id.NodeID].UpdatedAt; !got.Equal(g.clock.Now()) {
		t.Fatalf("enrolled node stamped at %v after the TTL elapsed, want %v (a stale slot expires to unknown)", got, g.clock.Now())
	}
	var changes atomic.Int32
	p.Compositor.SetOnChange(func(capacity.FleetSnapshot, *capacity.Delta, uint64) { changes.Add(1) })
	capT.tick(t)
	g.clock.Advance(fleetCapacityTTL / 3) // the periodic re-stamp runs: the controller slot must not flap
	capT.tick(t)
	if got := p.Compositor.Snapshot().Nodes[id.NodeID].UpdatedAt; !got.Equal(g.clock.Now()) {
		t.Fatalf("enrolled node not re-stamped at the TTL/3 boundary: %v, want %v", got, g.clock.Now())
	}
	if n := changes.Load(); n != 0 {
		t.Fatalf("%d capacity changes over unchanged sources (one tick and one periodic re-stamp), want 0", n)
	}
}
