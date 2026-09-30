package governor

// Purpose: asserts the three admission sentinels are real taxonomy
//
//	errors and that resource exhaustion (ErrQueueFull, ErrThrottled) is
//	distinguishable from refusal (ErrDraining) via cascade.KindOf, per
//	the ticket's non-negotiable that these two outcomes must never be
//	conflated by a caller.
import (
	"errors"
	"fmt"
	"strings"
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

// helperCases are the errors both identity helpers are checked against:
// every KindUnavailable sentinel bare and wrapped (by cascade and by the
// standard library), a fresh error of the same Kind, and nil.
func helperCases() map[string]error {
	cases := map[string]error{
		"fresh KindUnavailable": cascade.New(cascade.KindUnavailable, "governor: something else"),
		"nil":                   nil,
	}
	sentinels := map[string]*cascade.Error{
		"ErrDraining":            ErrDraining,
		"ErrNoResourceSignal":    ErrNoResourceSignal,
		"ErrStaleResourceSignal": ErrStaleResourceSignal,
	}
	for name, s := range sentinels {
		cases[name] = s
		cases[name+" wrapped"] = cascade.Wrapf(cascade.KindUnavailable, s, "posture=x")
		cases[name+" fmt-wrapped"] = fmt.Errorf("outer: %w", cascade.Wrap(cascade.KindUnavailable, s, "inner"))
		cases[name+" joined"] = errors.Join(errors.New("other"), s)
	}
	return cases
}

func assertHelper(t *testing.T, helper func(error) bool, name string, own ...string) {
	t.Helper()
	cases := helperCases()
	for label, err := range cases {
		want := false
		for _, o := range own {
			if strings.HasPrefix(label, o) {
				want = true
			}
		}
		if got := helper(err); got != want {
			t.Errorf("%s(%s) = %v, want %v", name, label, got, want)
		}
	}
	if !errors.Is(ErrDraining, ErrStaleResourceSignal) {
		t.Fatal("precondition: errors.Is compares Kind only, so it cannot tell these sentinels apart")
	}
}

func TestIsDrainingComparesSentinelIdentity(t *testing.T) {
	assertHelper(t, IsDraining, "IsDraining", "ErrDraining")
}

func TestIsSignalRefusalComparesSentinelIdentity(t *testing.T) {
	assertHelper(t, IsSignalRefusal, "IsSignalRefusal", "ErrNoResourceSignal", "ErrStaleResourceSignal")
}
