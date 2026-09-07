package governor

// Purpose: asserts the three admission sentinels are real taxonomy
//
//	errors and that resource exhaustion (ErrQueueFull, ErrThrottled) is
//	distinguishable from refusal (ErrDraining) via cascade.KindOf, per
//	the ticket's non-negotiable that these two outcomes must never be
//	conflated by a caller.
import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAdmissionErrorSentinelsAreTaxonomyKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want cascade.Kind
	}{
		{"ErrQueueFull", ErrQueueFull, cascade.KindQuotaExhausted},
		{"ErrThrottled", ErrThrottled, cascade.KindQuotaExhausted},
		{"ErrDraining", ErrDraining, cascade.KindUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := cascade.KindOf(tc.err)
			if !ok {
				t.Fatalf("%s is not a taxonomy error", tc.name)
			}
			if kind != tc.want {
				t.Fatalf("%s kind = %s, want %s", tc.name, kind, tc.want)
			}
		})
	}
}

// TestAdmissionExhaustionVsRefusalDistinguishable proves a caller can
// tell "try again, resource exhausted" (ErrQueueFull/ErrThrottled) apart
// from "this controller is going away, do not retry here"
// (ErrDraining): they carry different taxonomy Kinds, so errors.Is never
// conflates the two groups.
func TestAdmissionExhaustionVsRefusalDistinguishable(t *testing.T) {
	if errors.Is(ErrDraining, ErrQueueFull) {
		t.Fatal("ErrDraining must not be Is-equivalent to ErrQueueFull (refusal vs exhaustion)")
	}
	if errors.Is(ErrDraining, ErrThrottled) {
		t.Fatal("ErrDraining must not be Is-equivalent to ErrThrottled (refusal vs exhaustion)")
	}
	if !errors.Is(ErrQueueFull, ErrQueueFull) {
		t.Fatal("ErrQueueFull must be Is-equivalent to itself")
	}
}
