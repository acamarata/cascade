package topology

import "testing"

func TestValidDimensionNamePerKind(t *testing.T) {
	cases := []struct {
		kind QuotaDomainKind
		name string
		want bool
	}{
		{QuotaDomainAPIProject, DimensionRPM, true},
		{QuotaDomainAPIProject, DimensionTPM, true},
		{QuotaDomainAPIProject, DimensionRPD, true},
		{QuotaDomainAPIProject, DimensionWeekly, false},
		{QuotaDomainSubscriptionWindow, DimensionSession5h, true},
		{QuotaDomainSubscriptionWindow, DimensionWeeklyShared, true},
		{QuotaDomainSubscriptionWindow, DimensionWeeklyModelFraction, true},
		{QuotaDomainSubscriptionWindow, DimensionMonthly, true},
		{QuotaDomainSubscriptionWindow, DimensionRPM, false},
		{QuotaDomainSharedPool, DimensionWindow5h, true},
		{QuotaDomainSharedPool, DimensionWeekly, true},
		{QuotaDomainSharedPool, DimensionMonthly, true},
		{QuotaDomainSharedPool, DimensionSession5h, false},
		{QuotaDomainKind("bogus"), DimensionRPM, false},
	}
	for _, c := range cases {
		if got := ValidDimensionName(c.kind, c.name); got != c.want {
			t.Errorf("ValidDimensionName(%v, %q) = %v, want %v", c.kind, c.name, got, c.want)
		}
	}
}

// TestDimensionNameOutsideKindIsInvariant is the named acceptance-criteria
// test: a dimension name outside its kind's closed set is
// ErrTopologyInvariant.
func TestDimensionNameOutsideKindIsInvariant(t *testing.T) {
	dims := map[string]Bucket{DimensionWeekly: {}}
	err := ValidateDimensions(QuotaDomainAPIProject, dims)
	if err == nil {
		t.Fatal("expected ErrTopologyInvariant for weekly under api_project")
	}
}

func TestValidateDimensionsAcceptsClosedSet(t *testing.T) {
	dims := map[string]Bucket{DimensionRPM: {}, DimensionTPM: {}, DimensionRPD: {}}
	if err := ValidateDimensions(QuotaDomainAPIProject, dims); err != nil {
		t.Fatalf("ValidateDimensions(closed set) error = %v, want nil", err)
	}
}

func TestGaugeReservable(t *testing.T) {
	if !GaugeReservable(2, 1, 0) {
		t.Error("limit<=0 (not configured) should always be reservable")
	}
	if !GaugeReservable(2, 1, 3) {
		t.Error("2+1<=3 should be reservable")
	}
	if GaugeReservable(2, 2, 3) {
		t.Error("2+2>3 should refuse")
	}
}
