package topology

import (
	"errors"
	"testing"
)

// TestValidateReserveRange is the named acceptance test: ValidateReserve
// clamps into [0.0, 0.60] and returns ErrConfigRange only outside it,
// while ALWAYS returning a value safe to use.
func TestValidateReserveRange(t *testing.T) {
	cases := []struct {
		in        float64
		wantValue float64
		wantErr   bool
	}{
		{0.0, 0.0, false},
		{0.30, 0.30, false},
		{0.60, 0.60, false},
		{-0.5, 0.0, true},
		{1.0, 0.60, true},
	}
	for _, c := range cases {
		got, err := ValidateReserve(c.in)
		if got != c.wantValue {
			t.Errorf("ValidateReserve(%v) value = %v, want %v", c.in, got, c.wantValue)
		}
		if c.wantErr && !errors.Is(err, ErrConfigRange) {
			t.Errorf("ValidateReserve(%v): err = %v, want ErrConfigRange", c.in, err)
		}
		if !c.wantErr && err != nil {
			t.Errorf("ValidateReserve(%v): unexpected err %v", c.in, err)
		}
		if got < 0 || got > 0.60 {
			t.Errorf("ValidateReserve(%v) returned an unsafe value %v", c.in, got)
		}
	}
}

// TestBarrierBucketPerDomainKind is the named acceptance test.
func TestBarrierBucketPerDomainKind(t *testing.T) {
	cases := []struct {
		kind QuotaDomainKind
		want string
	}{
		{QuotaDomainSubscriptionWindow, DimensionWeeklyShared},
		{QuotaDomainSharedPool, DimensionWeekly},
		{QuotaDomainAPIProject, DimensionRPD},
	}
	for _, c := range cases {
		got, err := BarrierBucket(c.kind)
		if err != nil {
			t.Errorf("BarrierBucket(%v): unexpected err %v", c.kind, err)
		}
		if got != c.want {
			t.Errorf("BarrierBucket(%v) = %q, want %q", c.kind, got, c.want)
		}
	}
	if _, err := BarrierBucket(QuotaDomainKind("bogus")); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("BarrierBucket(bogus): err = %v, want ErrTopologyInvariant", err)
	}
}

func TestExecutiveModelFractionNamesAreNeverBarrierBuckets(t *testing.T) {
	for _, kind := range []QuotaDomainKind{QuotaDomainAPIProject, QuotaDomainSubscriptionWindow, QuotaDomainSharedPool} {
		got, err := BarrierBucket(kind)
		if err != nil {
			continue
		}
		if got == ExecutiveModelFractionSoft || got == ExecutiveModelFractionHard {
			t.Errorf("BarrierBucket(%v) = %q must never be an executive-role eligibility cap", kind, got)
		}
	}
}
