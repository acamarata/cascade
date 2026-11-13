package crunch

import "testing"

// TestLogOutputContainsMapping verifies that FormatAllocation produces the correct
// log strings for A3/75, A4/95, and CO/80 mappings with no trailing newline.
func TestLogOutputContainsMapping(t *testing.T) {
	tests := []struct {
		name     string
		acct     string
		pct      int
		fullTilt bool
		expected string
	}{
		{
			name:     "A3 with 75 percent",
			acct:     "A3",
			pct:      75,
			fullTilt: false,
			expected: "account=A3 five_hour=75 weekly=75",
		},
		{
			name:     "A4 with 95 percent enforces 90/90",
			acct:     "A4",
			pct:      95,
			fullTilt: false,
			expected: "account=A4 five_hour=90 weekly=90",
		},
		{
			name:     "CO with 80 percent",
			acct:     "CO",
			pct:      80,
			fullTilt: false,
			expected: "account=CO five_hour=80 weekly=80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alloc, err := MapQuota(tt.acct, tt.pct, tt.fullTilt)
			if err != nil {
				t.Fatalf("MapQuota(%q, %d, %v) returned error: %v", tt.acct, tt.pct, tt.fullTilt, err)
			}

			result := FormatAllocation(alloc)
			if result != tt.expected {
				t.Errorf("FormatAllocation() = %q, want %q", result, tt.expected)
			}
		})
	}
}
