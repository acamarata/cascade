package consult

import (
	"errors"
	"testing"
)

// TestRouteT0PlanRejectsNativeOpus verifies that native Opus is rejected
// for every combination of quota/burn flags and invalid tiers.
func TestRouteT0PlanRejectsNativeOpus(t *testing.T) {
	tiers := []string{"T0", "T1", "", "t0", " T0"}
	bools := []bool{true, false}

	for _, tier := range tiers {
		for _, nqs := range bools {
			for _, ba := range bools {
				req := T0Request{
					Tier:             tier,
					Model:            "native Opus",
					NativeQuotaShort: nqs,
					BurnAccount:      ba,
				}
				route, err := RouteT0Plan(req)
				if route != (T0Route{}) {
					t.Errorf("tier=%q nqs=%v ba=%v: expected zero route, got %+v", tier, nqs, ba, route)
				}
				if !errors.Is(err, ErrNativeOpusForbidden) {
					t.Errorf("tier=%q nqs=%v ba=%v: expected ErrNativeOpusForbidden, got %v", tier, nqs, ba, err)
				}
				if err == nil || err.Error() != "D6166: native Opus forbidden" {
					t.Errorf("tier=%q nqs=%v ba=%v: expected exact error text, got %v", tier, nqs, ba, err)
				}
			}
		}
	}
}
