package write

import (
	"errors"
	"testing"
)

// TestConductorUsesInjectedPolicy verifies that Decide delegates exactly once
// to the injected PolicyFunc with the original subject, and returns its results.
func TestConductorUsesInjectedPolicy(t *testing.T) {
	tests := []struct {
		name         string
		policyResult bool
		policyError  error
		wantBool     bool
		wantErr      bool
	}{
		{
			name:         "allowed",
			policyResult: true,
			policyError:  nil,
			wantBool:     true,
			wantErr:      false,
		},
		{
			name:         "denied",
			policyResult: false,
			policyError:  nil,
			wantBool:     false,
			wantErr:      false,
		},
		{
			name:         "error",
			policyResult: false,
			policyError:  errors.New("injected policy error"),
			wantBool:     false,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkInjectedPolicy(t, tt.policyResult, tt.policyError, tt.wantBool, tt.wantErr)
		})
	}
}

// checkInjectedPolicy runs one Decide call against a conductor built with a
// policy returning the given results, and asserts the delegation contract.
func checkInjectedPolicy(t *testing.T, policyResult bool, policyError error, wantBool, wantErr bool) {
	t.Helper()
	callCount := 0
	var capturedSubject string

	policy := func(subject string) (bool, error) {
		callCount++
		capturedSubject = subject
		return policyResult, policyError
	}

	c, err := NewConductor(policy)
	if err != nil {
		t.Fatalf("NewConductor returned unexpected error: %v", err)
	}

	subject := "test-subject"
	gotBool, gotErr := c.Decide(subject)

	if callCount != 1 {
		t.Errorf("expected exactly 1 call to policy, got %d", callCount)
	}
	if capturedSubject != subject {
		t.Errorf("policy called with subject %q, want %q", capturedSubject, subject)
	}
	if gotBool != wantBool {
		t.Errorf("Decide() bool = %v, want %v", gotBool, wantBool)
	}
	if (gotErr != nil) != wantErr {
		t.Errorf("Decide() error = %v, wantErr %v", gotErr, wantErr)
	}
	if wantErr && !errors.Is(gotErr, policyError) {
		t.Errorf("Decide() error = %v, want wrapped error containing %v", gotErr, policyError)
	}
}
