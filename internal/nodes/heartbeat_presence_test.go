package nodes

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// TestProcessHeartbeatAdvancesPresenceAndEmits proves heartbeat.go's own
// presence wiring (P1-E36-W7-S72-T2): two real heartbeats spanning
// HitDwellWindow transition presence to reachable and publish
// node.presence.changed, through ProcessHeartbeat's real production
// path — not a synthetic call into AdvancePresence directly.
func TestProcessHeartbeatAdvancesPresenceAndEmits(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "hp")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := GenerateIdentity(fixedReader(t, "hp"))
	if err != nil {
		t.Fatal(err)
	}
	bus := &captureBus{}
	deps := HeartbeatDeps{Records: store, Sequences: NewSequenceStore(), Clock: clock, Bus: bus}
	build := func(seq uint64) HeartbeatFrame {
		return signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), seq)
	}

	if _, err := ProcessHeartbeat(context.Background(), deps, build(1)); err != nil {
		t.Fatalf("heartbeat 1: %v", err)
	}
	got, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Presence == PresenceReachable {
		t.Fatal("presence transitioned to reachable on a single heartbeat; want two spanning HitDwellWindow")
	}

	clock.Advance(HitDwellWindow + time.Second)
	if _, err := ProcessHeartbeat(context.Background(), deps, build(2)); err != nil {
		t.Fatalf("heartbeat 2: %v", err)
	}
	got, err = store.Get(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Presence != PresenceReachable {
		t.Fatalf("presence = %q after two heartbeats spanning HitDwellWindow, want reachable", got.Presence)
	}

	found := false
	for _, e := range bus.published {
		if e.kind == string(NodePresenceChangedKind) {
			found = true
		}
	}
	if !found {
		t.Fatal("node.presence.changed was never published on the real ProcessHeartbeat path")
	}
}
