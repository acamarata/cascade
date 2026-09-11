package capacity

// Purpose (this file): unit tests for the pure type-level invariants
// snapshot.go declares - PresenceState.Valid's excluded zero value, and
// the re-exported State/BucketKind constants matching their
// registry.* originals exactly (the CONTRACT DEVIATION this package
// documents must hold at runtime, not just in a comment).

import (
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
