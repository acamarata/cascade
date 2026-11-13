package docket

import (
	"errors"
	"testing"
)

func TestValidateDependencyWithinLimit(t *testing.T) {
	// Test nil dependencies
	if err := ValidateDeps(nil); err != nil {
		t.Errorf("ValidateDeps(nil) = %v, want nil", err)
	}

	// Test empty dependencies
	if err := ValidateDeps([]string{}); err != nil {
		t.Errorf("ValidateDeps([]) = %v, want nil", err)
	}

	// Test 10 dependencies
	deps10 := make([]string, 10)
	if err := ValidateDeps(deps10); err != nil {
		t.Errorf("ValidateDeps(10 deps) = %v, want nil", err)
	}

	// Test 64 dependencies (at limit)
	deps64 := make([]string, 64)
	if err := ValidateDeps(deps64); err != nil {
		t.Errorf("ValidateDeps(64 deps) = %v, want nil", err)
	}

	// Test 65 dependencies (over limit)
	deps65 := make([]string, 65)
	err := ValidateDeps(deps65)
	if !errors.Is(err, ErrTooManyDeps) {
		t.Errorf("ValidateDeps(65 deps) = %v, want ErrTooManyDeps", err)
	}

	// Test with blank strings (should pass)
	blankDeps := []string{"", "", ""}
	if err := ValidateDeps(blankDeps); err != nil {
		t.Errorf("ValidateDeps(blank strings) = %v, want nil", err)
	}

	// Test with duplicate strings (should pass)
	dupDeps := []string{"a", "a", "a"}
	if err := ValidateDeps(dupDeps); err != nil {
		t.Errorf("ValidateDeps(duplicates) = %v, want nil", err)
	}

	// Test through Validate with valid deps
	b1 := LeanBrief{Status: "ok", Size: 100, Deps: make([]string, 64)}
	if err := Validate(b1); err != nil {
		t.Errorf("Validate(64 deps) = %v, want nil", err)
	}

	// Test through Validate with too many deps
	b2 := LeanBrief{Status: "ok", Size: 100, Deps: make([]string, 65)}
	err = Validate(b2)
	if !errors.Is(err, ErrTooManyDeps) {
		t.Errorf("Validate(65 deps) = %v, want ErrTooManyDeps", err)
	}

	// Verify inputs remain unchanged
	depsCheck := []string{"a", "b", "c"}
	original := make([]string, len(depsCheck))
	copy(original, depsCheck)
	_ = ValidateDeps(depsCheck)
	for i := range depsCheck {
		if depsCheck[i] != original[i] {
			t.Errorf("ValidateDeps mutated input at index %d", i)
		}
	}
}
