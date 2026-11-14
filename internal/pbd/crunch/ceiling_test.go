package crunch

import "testing"

// TestSpendCeilingMatchesCrunchCeiling verifies that SpendCeiling is the
// identity function for every supplied ceiling, including zero, negative
// values, and values above the daily ticket limit.
func TestSpendCeilingMatchesCrunchCeiling(t *testing.T) {
	cases := []struct {
		name string
		in   int
	}{
		{name: "above limit", in: 500},
		{name: "zero", in: 0},
		{name: "negative", in: -1},
		{name: "negative large", in: -987654321},
		{name: "limit", in: DailyTicketLimit},
		{name: "one above limit", in: DailyTicketLimit + 1},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := SpendCeiling(tc.in); got != tc.in {
				t.Errorf("SpendCeiling(%d) = %d, want %d", tc.in, got, tc.in)
			}
		})
	}
}
