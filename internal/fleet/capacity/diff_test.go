package capacity

// Purpose (this file): TestFleetSnapshotDiff - table-driven coverage of
// Diff/Clone: no-change returns nil, a changed bucket/presence value is
// detected, a removed slot is reported, and Clone produces an
// independent deep copy (mutating the clone never touches the source).
// Each case's body is its own top-level helper (funlen: the dispatching
// TestFleetSnapshotDiff itself must stay under 50 lines).

import (
	"testing"
	"time"
)

func baseSnapshot() FleetSnapshot {
	return FleetSnapshot{
		Providers: map[string]ProviderSlot{
			"acme": {
				ProfileRef: "acme",
				State:      StateAvailable,
				Buckets: map[BucketKind]Bucket{
					BucketInteractiveUsage: {State: StateAvailable},
				},
			},
		},
		Nodes: map[string]NodeSlot{
			"node1": {ID: "node1", Presence: PresenceReachable, Toolchains: []string{"go"}},
		},
	}
}

func TestFleetSnapshotDiff(t *testing.T) {
	t.Run("no change returns nil", diffCaseNoChange)
	t.Run("changed bucket state is detected", diffCaseChangedBucket)
	t.Run("changed presence is detected", diffCaseChangedPresence)
	t.Run("removed slot is reported", diffCaseRemovedSlot)
	t.Run("GeneratedAt and Seq alone never produce a diff", diffCaseMetadataOnly)
}

func diffCaseNoChange(t *testing.T) {
	prev, next := baseSnapshot(), baseSnapshot()
	if d := Diff(prev, next); d != nil {
		t.Errorf("Diff on identical content = %+v, want nil", d)
	}
}

func diffCaseChangedBucket(t *testing.T) {
	prev, next := baseSnapshot(), baseSnapshot()
	slot := next.Providers["acme"]
	slot.State = StateExhausted
	b := slot.Buckets[BucketInteractiveUsage]
	b.State = StateExhausted
	slot.Buckets[BucketInteractiveUsage] = b
	next.Providers["acme"] = slot

	d := Diff(prev, next)
	if d == nil {
		t.Fatal("expected a non-nil delta for a changed bucket state")
	}
	if got := d.ChangedProviders["acme"].State; got != StateExhausted {
		t.Errorf("changed provider state = %q, want exhausted", got)
	}
}

func diffCaseChangedPresence(t *testing.T) {
	prev, next := baseSnapshot(), baseSnapshot()
	n := next.Nodes["node1"]
	n.Presence = presenceUnavailable
	next.Nodes["node1"] = n

	d := Diff(prev, next)
	if d == nil {
		t.Fatal("expected a non-nil delta for a changed presence")
	}
	if got := d.ChangedNodes["node1"].Presence; got != presenceUnavailable {
		t.Errorf("changed node presence = %q, want unavailable", got)
	}
}

func diffCaseRemovedSlot(t *testing.T) {
	prev := baseSnapshot()
	next := FleetSnapshot{Providers: map[string]ProviderSlot{}, Nodes: map[string]NodeSlot{}}

	d := Diff(prev, next)
	if d == nil {
		t.Fatal("expected a non-nil delta for a removed slot")
	}
	if len(d.RemovedProviders) != 1 || d.RemovedProviders[0] != "acme" {
		t.Errorf("removed providers = %v, want [acme]", d.RemovedProviders)
	}
	if len(d.RemovedNodes) != 1 || d.RemovedNodes[0] != "node1" {
		t.Errorf("removed nodes = %v, want [node1]", d.RemovedNodes)
	}
}

func diffCaseMetadataOnly(t *testing.T) {
	prev, next := baseSnapshot(), baseSnapshot()
	next.GeneratedAt = time.Now()
	next.Seq = 42
	if d := Diff(prev, next); d != nil {
		t.Errorf("Diff over only GeneratedAt/Seq = %+v, want nil", d)
	}
}

func TestClone(t *testing.T) {
	src := baseSnapshot()
	clone := Clone(src)

	clone.Providers["acme"].Buckets[BucketInteractiveUsage] = Bucket{State: StateExhausted}
	if got := src.Providers["acme"].Buckets[BucketInteractiveUsage].State; got != StateAvailable {
		t.Errorf("mutating the clone's bucket map affected the source: got %q", got)
	}

	cloneNode := clone.Nodes["node1"]
	cloneNode.Toolchains[0] = "rust"
	if got := src.Nodes["node1"].Toolchains[0]; got != "go" {
		t.Errorf("mutating the clone's Toolchains slice affected the source: got %q", got)
	}
}
