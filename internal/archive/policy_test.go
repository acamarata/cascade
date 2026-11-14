package archive

import (
	"errors"
	"testing"
)

// TestParsePoliciesStub verifies that the stub declares the expected zero
// values and always returns ErrNotImplemented.
func TestParsePoliciesStub(t *testing.T) {
	var zero TargetPolicy
	if zero.Target != "" {
		t.Fatalf("zero TargetPolicy.Target = %q, want \"\"", zero.Target)
	}
	if zero.AllowedTypes != nil {
		t.Fatalf("zero TargetPolicy.AllowedTypes = %v, want nil", zero.AllowedTypes)
	}

	policies, err := ParsePolicies(map[string][]string{"db": {"sql"}})
	if policies != nil {
		t.Fatalf("ParsePolicies(...) policies = %v, want nil", policies)
	}
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("ParsePolicies(...) error = %v, want %v", err, ErrNotImplemented)
	}
}
