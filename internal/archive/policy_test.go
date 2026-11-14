package archive

import "testing"

// TestTargetPolicyZeroValue verifies the zero value of the declared TargetPolicy type.
func TestTargetPolicyZeroValue(t *testing.T) {
	var zero TargetPolicy
	if zero.Target != "" {
		t.Fatalf("zero TargetPolicy.Target = %q, want \"\"", zero.Target)
	}
	if zero.AllowedTypes != nil {
		t.Fatalf("zero TargetPolicy.AllowedTypes = %v, want nil", zero.AllowedTypes)
	}
}
