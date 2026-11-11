package review

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestConductorAndJobLaddersAgreeOnHold compares the real dispatch ladders.
func TestConductorAndJobLaddersAgreeOnHold(t *testing.T) {
	for _, rc := range []jobs.RiskClass{jobs.RiskClassLow, jobs.RiskClassNormal, jobs.RiskClassHigh, jobs.RiskClassCritical, "bogus"} {
		p := JobReviewPolicy{Class: "bogus"}
		if rc != "bogus" {
			var err error
			p, err = NewJobReviewPolicy(rc)
			if err != nil {
				t.Fatal(err)
			}
		}
		reg := jobFamilyRegistry()
		for _, count := range []int{2, 3} {
			fleet := fakeRegistry{infos: reg.infos[:count]}
			t.Run(string(rc)+"/"+fleet.infos[count-1].Name, func(t *testing.T) {
				checkJobFamilyParity(t, p, fleet)
			})
		}
	}
}

func checkJobFamilyParity(t *testing.T, p JobReviewPolicy, reg fakeRegistry) {
	t.Helper()
	ctx := context.Background()
	author := ProviderFamily(ctx, reg, reg.infos[0].Name)
	if !author.Resolved {
		t.Fatal("fixture author unresolved")
	}
	record, conductorErr := familyGate(ctx, reg, provider.ReviewCRLevelC, p.Class, nil)
	degraded, jobErr := p.CheckAvailable(ctx, reg, author)
	if conductorErr != nil && conductorErr != ErrNoEligibleReviewerFamily {
		t.Fatalf("unexpected conductor error: %v", conductorErr)
	}
	if jobErr != nil && jobErr != jobs.ErrNoDistinctFamily {
		t.Fatalf("unexpected jobs error: %v", jobErr)
	}
	if (conductorErr == ErrNoEligibleReviewerFamily) != (jobErr == jobs.ErrNoDistinctFamily) {
		t.Fatalf("hold mismatch: conductor=%v jobs=%v", conductorErr, jobErr)
	}
	if conductorErr == nil && jobErr == nil && record.SameFamilyFallback != degraded {
		t.Fatalf("fallback mismatch: conductor=%v jobs=%v", record.SameFamilyFallback, degraded)
	}
}
