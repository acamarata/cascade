package write

import (
	"errors"
	"testing"
)

// TestPolicyFuncInjection verifies that a Conductor uses only the injected PolicyFunc,
// that empty/whitespace subjects are rejected without calling the policy,
// and that the constructor returns a valid conductor for non-nil policies.
func TestPolicyFuncInjection(t *testing.T) {
	calls := 0
	pf := func(_ string) (bool, error) {
		calls++
		return true, nil
	}

	c, err := NewConductor(pf)
	if err != nil {
		t.Fatalf("NewConductor returned error for non-nil policy: %v", err)
	}
	if c == nil {
		t.Fatal("NewConductor returned nil conductor for non-nil policy")
	}

	// Empty subject
	calls = 0
	ok, err := c.Decide("")
	if ok {
		t.Error("Decide returned true for empty subject")
	}
	if !errors.Is(err, ErrInvalidSubject) {
		t.Errorf("Decide error for empty subject: got %v, want ErrInvalidSubject", err)
	}
	if calls != 0 {
		t.Errorf("policy called %d times for empty subject, want 0", calls)
	}

	// Whitespace subject
	calls = 0
	ok, err = c.Decide("   \t\n ")
	if ok {
		t.Error("Decide returned true for whitespace subject")
	}
	if !errors.Is(err, ErrInvalidSubject) {
		t.Errorf("Decide error for whitespace subject: got %v, want ErrInvalidSubject", err)
	}
	if calls != 0 {
		t.Errorf("policy called %d times for whitespace subject, want 0", calls)
	}
}
