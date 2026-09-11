package nodes_test

// TestPresenceFleetSnapshotIntegration is the QA-B cross-epic seam test
// named by this ticket's acceptance criteria: a real presence transition,
// produced by nodes.AdvancePresence exactly as nodes.Prober would drive
// it, must reach AE/S-63.T1's FleetSnapshot NodeSlot.Presence through the
// existing Compositor.UpdateNodes/Snapshot path end to end, with zero
// changes required to internal/fleet/capacity's own transport wiring
// (only the two files this ticket's journal records as migrated:
// snapshot.go's type alias and compositor_build.go's verbatim readback).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/nodes"
)

type fleetNodeSource struct{ recs []nodes.DeviceRecord }

func (s fleetNodeSource) List() ([]nodes.DeviceRecord, error) { return s.recs, nil }

type fleetClock struct{ now time.Time }

func (c *fleetClock) Now() time.Time { return c.now }

func TestPresenceFleetSnapshotIntegration(t *testing.T) {
	clock := &fleetClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	comp := capacity.NewCompositor(clock, time.Hour, "")

	rec := nodes.DeviceRecord{NodeID: "n1", Presence: nodes.PresenceReachable}
	if err := comp.UpdateNodes(context.Background(), fleetNodeSource{recs: []nodes.DeviceRecord{rec}}); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}
	if got := comp.Snapshot().Nodes["n1"].Presence; got != capacity.PresenceReachable {
		t.Fatalf("precondition: presence = %q, want reachable", got)
	}

	// Drive the real R-21.197 hysteresis: three consecutive direct-probe
	// misses spanning >=30s, exactly as nodes.Prober would.
	now := clock.now
	for i := 0; i < nodes.ProbeMissThreshold; i++ {
		now = now.Add(15 * time.Second)
		rec, _ = nodes.AdvancePresence(rec, nodes.PresenceObservation{OK: false, At: now, Authenticated: true}, now)
	}
	if rec.Presence != nodes.PresenceUnavailable {
		t.Fatalf("setup: rec.Presence = %q, want unavailable", rec.Presence)
	}
	clock.now = now

	if err := comp.UpdateNodes(context.Background(), fleetNodeSource{recs: []nodes.DeviceRecord{rec}}); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}
	if got := comp.Snapshot().Nodes["n1"].Presence; got != nodes.PresenceUnavailable {
		t.Fatalf("FleetSnapshot NodeSlot.Presence = %q after a real presence transition, want unavailable", got)
	}
}
