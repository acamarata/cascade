package docket

import (
	"errors"
	"testing"
)

// TestValidateID_DuplicateHalts verifies that a duplicate ticket ID halts with D1442.
func TestValidateID_DuplicateHalts(t *testing.T) {
	existing := []string{"P123-ABC-01", "P123-ABC-01", "P456-DEF-02"}
	id := "P123-ABC-01"

	decision, err := ValidateID(id, existing)

	if !errors.Is(err, ErrD1442) {
		t.Errorf("expected ErrD1442, got %v", err)
	}
	if decision != Halt {
		t.Errorf("expected Halt decision, got %d", decision)
	}
}

// TestValidateID_UnparsableHalts verifies that an unparsable ticket ID halts with D1442.
func TestValidateID_UnparsableHalts(t *testing.T) {
	cases := []string{"", " ", "  ", " \t\n"}

	for _, id := range cases {
		decision, err := ValidateID(id, []string{})
		if !errors.Is(err, ErrD1442) {
			t.Errorf("input %q: expected ErrD1442, got %v", id, err)
		}
		if decision != Halt {
			t.Errorf("input %q: expected Halt decision, got %d", id, decision)
		}
	}
}

// TestValidateID_MalformedHalts verifies that a malformed ticket ID halts with D1442.
func TestValidateID_MalformedHalts(t *testing.T) {
	cases := []string{"P-ABC-01", "P123-abc-01", "P123-ABC-1", "X123-ABC-01", "P123ABC01", "P123-ABC"}

	for _, id := range cases {
		decision, err := ValidateID(id, []string{})
		if !errors.Is(err, ErrD1442) {
			t.Errorf("input %q: expected ErrD1442, got %v", id, err)
		}
		if decision != Halt {
			t.Errorf("input %q: expected Halt decision, got %d", id, decision)
		}
	}
}

// TestValidateID_NotFoundFallsThrough verifies that a valid but missing ID falls through.
func TestValidateID_NotFoundFallsThrough(t *testing.T) {
	existing := []string{"P123-ABC-01", "P456-DEF-02"}
	id := "P789-GHI-03"

	decision, err := ValidateID(id, existing)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if decision != FallThrough {
		t.Errorf("expected FallThrough decision, got %d", decision)
	}
}

// TestValidateID_ValidProceeds verifies that a valid existing ID proceeds.
func TestValidateID_ValidProceeds(t *testing.T) {
	existing := []string{"P123-ABC-01", "P456-DEF-02"}
	id := "P123-ABC-01"

	decision, err := ValidateID(id, existing)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if decision != Proceed {
		t.Errorf("expected Proceed decision, got %d", decision)
	}
}
