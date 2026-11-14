package write

import (
	"errors"
	"testing"
)

type greensCase struct {
	name string
	in   GreensLifecycle
}

func greensInvalidCases() []greensCase {
	return []greensCase{
		{
			name: "invalid kind",
			in:   GreensLifecycle{Kind: "wrong", Severity: "error", State: GreensOpen},
		},
		{
			name: "empty kind",
			in:   GreensLifecycle{Kind: "", Severity: "error", State: GreensOpen},
		},
		{
			name: "invalid severity",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "wrong", State: GreensOpen},
		},
		{
			name: "empty severity",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "", State: GreensOpen},
		},
		{
			name: "invalid state",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "error", State: "wrong"},
		},
		{
			name: "empty state",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "error", State: ""},
		},
		{
			name: "empty lifecycle",
			in:   GreensLifecycle{},
		},
	}
}

func TestGreensDecreasedSeverityError(t *testing.T) {
	lc := NewGreensLifecycle()
	if lc.Kind != "greens_decreased" {
		t.Errorf("Kind = %q, want %q", lc.Kind, "greens_decreased")
	}
	if lc.Severity != "error" {
		t.Errorf("Severity = %q, want %q", lc.Severity, "error")
	}
	if lc.State != GreensOpen {
		t.Errorf("State = %q, want %q", lc.State, GreensOpen)
	}

	tests := greensInvalidCases()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ValidateGreensLifecycle(tc.in)
			if !errors.Is(err, ErrInvalidGreensLifecycle) {
				t.Errorf("ValidateGreensLifecycle() err = %v, want %v", err, ErrInvalidGreensLifecycle)
			}
			if res != tc.in {
				t.Errorf("ValidateGreensLifecycle() = %v, want %v", res, tc.in)
			}
		})
	}
}

func TestGreensValidStates(t *testing.T) {
	validTests := []struct {
		name string
		in   GreensLifecycle
	}{
		{
			name: "open",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "error", State: GreensOpen},
		},
		{
			name: "reopened",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "error", State: GreensReopened},
		},
		{
			name: "closed",
			in:   GreensLifecycle{Kind: "greens_decreased", Severity: "error", State: GreensClosed},
		},
	}

	for _, tc := range validTests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ValidateGreensLifecycle(tc.in)
			if err != nil {
				t.Errorf("ValidateGreensLifecycle() err = %v, want nil", err)
			}
			if res != tc.in {
				t.Errorf("ValidateGreensLifecycle() = %v, want %v", res, tc.in)
			}
		})
	}
}
