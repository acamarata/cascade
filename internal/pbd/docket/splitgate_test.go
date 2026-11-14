package docket

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSplitAllMappedExactScopePasses(t *testing.T) {
	parent := SplitParent{
		Acceptance: []string{"A1"},
		Scope:      []string{"auth"},
	}
	children := []Child{
		{
			Acceptance: []string{"A1"},
			Scope:      []string{"auth"},
		},
	}

	if err := ValidateSplit(parent, children); err != nil {
		t.Fatalf("ValidateSplit returned error for a valid split: %v", err)
	}
}

func TestValidateSplitUnmappedAcceptanceRejectsD2466(t *testing.T) {
	parent := SplitParent{
		Acceptance: []string{"A2"},
		Scope:      []string{"auth"},
	}
	children := []Child{
		{
			Acceptance: []string{"A1"},
			Scope:      []string{"auth"},
		},
	}

	err := ValidateSplit(parent, children)
	if err == nil {
		t.Fatal("ValidateSplit returned nil for an unmapped acceptance line")
	}
	if !errors.Is(err, ErrUnmappedAcceptance) {
		t.Fatalf("ValidateSplit error = %v, want ErrUnmappedAcceptance", err)
	}
	if !strings.Contains(err.Error(), "D2466") {
		t.Fatalf("ValidateSplit error = %q, want it to carry D2466", err.Error())
	}
}

func TestValidateSplitChildScopeOutsideParentRejectsD2467(t *testing.T) {
	parent := SplitParent{
		Acceptance: []string{"A1"},
		Scope:      []string{"auth"},
	}
	children := []Child{
		{
			Acceptance: []string{"A1"},
			Scope:      []string{"auth", "billing"},
		},
	}

	err := ValidateSplit(parent, children)
	if err == nil {
		t.Fatal("ValidateSplit returned nil for a child scope outside the parent scope")
	}
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("ValidateSplit error = %v, want ErrScopeMismatch", err)
	}
	if !strings.Contains(err.Error(), "D2467") {
		t.Fatalf("ValidateSplit error = %q, want it to carry D2467", err.Error())
	}
}

func TestValidateSplitMissingScopeCoverageRejectsD2467(t *testing.T) {
	parent := SplitParent{
		Acceptance: []string{"A1"},
		Scope:      []string{"auth", "billing"},
	}
	children := []Child{
		{
			Acceptance: []string{"A1"},
			Scope:      []string{"auth"},
		},
	}

	err := ValidateSplit(parent, children)
	if err == nil {
		t.Fatal("ValidateSplit returned nil for missing scope coverage")
	}
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("ValidateSplit error = %v, want ErrScopeMismatch", err)
	}
	if !strings.Contains(err.Error(), "D2467") {
		t.Fatalf("ValidateSplit error = %q, want it to carry D2467", err.Error())
	}
}
