package docket

import (
	"errors"
	"testing"
)

func TestValidateDependencyWithinLimit(t *testing.T) {
	tests := []struct {
		name string
		deps []string
	}{
		{"nil", nil},
		{"empty", []string{}},
		{"10 deps", make([]string, 10)},
		{"64 deps (at limit)", make([]string, 64)},
		{"blank strings", []string{"", "", ""}},
		{"duplicates", []string{"a", "a", "a"}},
	}
	for _, tt := range tests {
		if err := ValidateDeps(tt.deps); err != nil {
			t.Errorf("ValidateDeps(%s) = %v, want nil", tt.name, err)
		}
	}
}

func TestValidateDependencyOverLimit(t *testing.T) {
	// 65 dependencies (over limit)
	err := ValidateDeps(make([]string, 65))
	if !errors.Is(err, ErrTooManyDeps) {
		t.Errorf("ValidateDeps(65 deps) = %v, want ErrTooManyDeps", err)
	}
}

func TestValidateDependencyThroughValidate(t *testing.T) {
	b1 := LeanBrief{Status: "ok", Size: 100, Deps: make([]string, 64)}
	if err := Validate(b1); err != nil {
		t.Errorf("Validate(64 deps) = %v, want nil", err)
	}

	b2 := LeanBrief{Status: "ok", Size: 100, Deps: make([]string, 65)}
	err := Validate(b2)
	if !errors.Is(err, ErrTooManyDeps) {
		t.Errorf("Validate(65 deps) = %v, want ErrTooManyDeps", err)
	}
}

func TestValidateDependencyDoesNotMutateInput(t *testing.T) {
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
