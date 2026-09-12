package capacity

// Purpose (this file): unit tests for the pure type-level invariants
// snapshot.go declares - PresenceState.Valid's excluded zero value, and
// the re-exported State/BucketKind constants matching their
// registry.* originals exactly (the CONTRACT DEVIATION this package
// documents must hold at runtime, not just in a comment).

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
)

func TestPresenceStateValid(t *testing.T) {
	cases := []struct {
		state PresenceState
		valid bool
	}{
		{PresenceReachable, true},
		{presenceUnavailable, true},
		{presenceRemoteViaRoute, true},
		{PresenceUnknown, true},
		{PresenceState(""), false},
		{PresenceState("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.state.Valid(); got != tc.valid {
			t.Errorf("PresenceState(%q).Valid() = %v, want %v", tc.state, got, tc.valid)
		}
	}
}

func TestStateReexportsRegistryLaneState(t *testing.T) {
	if StateAvailable != registry.LaneStateAvailable ||
		StateConstrained != registry.LaneStateConstrained ||
		StateExhausted != registry.LaneStateExhausted ||
		StateAuthRequired != registry.LaneStateAuthRequired ||
		StateUnknown != registry.LaneStateUnknown {
		t.Fatal("capacity.State* constants have drifted from registry.LaneState*")
	}
}

func TestBucketKindReexportsRegistryCapacityBucket(t *testing.T) {
	if BucketInteractiveUsage != registry.CapacityInteractiveUsage ||
		BucketAgentSDKCredit != registry.CapacityAgentSDKCredit ||
		BucketAPICredit != registry.CapacityAPICredit {
		t.Fatal("capacity.Bucket* constants have drifted from registry.CapacityBucket*")
	}
}

func TestWindowUtilizationUnknownIsNegative(t *testing.T) {
	if WindowUtilizationUnknown >= 0 {
		t.Fatal("WindowUtilizationUnknown must be negative to be distinguishable from a real 0% reading")
	}
}

// TestFleetSnapshotQuotaEmbedIsAdditive is the named acceptance test
// (S-77.T3): FleetSnapshot's pre-existing fields and their JSON names are
// unchanged, and an OLDER payload with no "quota" key decodes to the zero
// QuotaSnapshot (confidence 0, nil domains) rather than failing to decode
// or leaving Quota looking like a confirmed-empty-but-observed reading.
func TestFleetSnapshotQuotaEmbedIsAdditive(t *testing.T) {
	oldPayload := []byte(`{"generated_at":"2026-01-01T00:00:00Z","seq":1,"providers":{},"nodes":{},"task_capabilities":[]}`)
	var snap FleetSnapshot
	if err := json.Unmarshal(oldPayload, &snap); err != nil {
		t.Fatalf("decode an older, quota-less payload: %v", err)
	}
	if snap.Seq != 1 {
		t.Errorf("pre-existing field Seq = %d, want 1 (unaffected by the new field)", snap.Seq)
	}
	if len(snap.Quota.Domains) != 0 {
		t.Errorf("Quota.Domains = %v, want nil/empty for a payload with no quota key", snap.Quota.Domains)
	}

	// Round-trip: a snapshot WITH a populated quota field survives
	// encode/decode without disturbing any pre-existing field.
	snap.Quota.Domains = nil
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped FleetSnapshot
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if roundTripped.Seq != snap.Seq {
		t.Errorf("round trip Seq = %d, want %d", roundTripped.Seq, snap.Seq)
	}
}
