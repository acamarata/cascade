// Purpose: proves ErrFleetExhausted/ErrNoAllowedLanes each wrap exactly
// one frozen pkg/cascade.Kind and are distinguishable via errors.Is.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).
package capacity

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSentinelErrorsWrapFrozenKinds(t *testing.T) {
	kind, ok := cascade.KindOf(ErrFleetExhausted)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("ErrFleetExhausted kind = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
	kind, ok = cascade.KindOf(ErrNoAllowedLanes)
	if !ok || kind != cascade.KindPolicyDenied {
		t.Fatalf("ErrNoAllowedLanes kind = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}
	if errors.Is(ErrFleetExhausted, ErrNoAllowedLanes) {
		t.Fatal("ErrFleetExhausted must not be Is(ErrNoAllowedLanes): distinct kinds")
	}
}
