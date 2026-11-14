package crunch

import (
	"errors"
	"testing"
)

// TestConcurrencyScalesWithHealth verifies that SizeConcurrency returns the
// expected admission capacity for each supported health signal.
func TestConcurrencyScalesWithHealth(t *testing.T) {
	tests := []struct {
		name string
		h    Health
		want int
	}{
		{name: "healthy allows full concurrency", h: HealthHealthy, want: 4},
		{name: "degraded halves concurrency", h: HealthDegraded, want: 2},
		{name: "down stops admission", h: HealthDown, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SizeConcurrency(tc.h)
			if err != nil {
				t.Fatalf("SizeConcurrency(%q) returned unexpected error: %v", tc.h, err)
			}
			if got != tc.want {
				t.Fatalf("SizeConcurrency(%q) = %d, want %d", tc.h, got, tc.want)
			}
			if tc.h == HealthDown && got != 0 {
				t.Fatalf("SizeConcurrency(%q) = %d, want zero admission", tc.h, got)
			}
		})
	}
}

// TestInvalidHealthSignalFailsClosed verifies that unrecognized health signals
// fail closed with zero concurrency and ErrInvalidHealth.
func TestInvalidHealthSignalFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		h    Health
	}{
		{name: "empty", h: ""},
		{name: "padded healthy", h: " healthy "},
		{name: "p degraded", h: "degraded "},
		{name: "case variant healthy", h: "Healthy"},
		{name: "case variant degraded", h: "DEGRADED"},
		{name: "case variant down", h: "Down"},
		{name: "unknown", h: "critical"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SizeConcurrency(tc.h)
			if got != 0 {
				t.Fatalf("SizeConcurrency(%q) = %d, want 0", tc.h, got)
			}
			if err == nil {
				t.Fatalf("SizeConcurrency(%q) returned nil error, want ErrInvalidHealth", tc.h)
			}
			if !errors.Is(err, ErrInvalidHealth) {
				t.Fatalf("errors.Is(SizeConcurrency(%q) error, ErrInvalidHealth) = false, want true", tc.h)
			}
			if want := "P2-DKT-08"; err.Error() != want {
				t.Fatalf("SizeConcurrency(%q) error text = %q, want %q", tc.h, err.Error(), want)
			}
		})
	}
}
