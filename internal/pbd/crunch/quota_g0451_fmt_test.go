package crunch

import "testing"

// TestLogOutputContainsMapping verifies that FormatAllocation produces the correct
// log strings for acct-c/75, acct-d/95, and tertiary/80 mappings with no trailing newline.
func TestLogOutputContainsMapping(t *testing.T) {
	tests := []struct {
		name     string
		acct     string
		pct      int
		fullTilt bool
		expected string
	}{
		{
			name:     "acct-c with 75 percent",
			acct:     "acct-c",
			pct:      75,
			fullTilt: false,
			expected: "account=acct-c five_hour=75 weekly=75",
		},
		{
			name:     "acct-d with 95 percent enforces 90/90",
			acct:     "acct-d",
			pct:      95,
			fullTilt: false,
			expected: "account=acct-d five_hour=90 weekly=90",
		},
		{
			name:     "tertiary with 80 percent",
			acct:     "tertiary",
			pct:      80,
			fullTilt: false,
			expected: "account=tertiary five_hour=80 weekly=80",
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
