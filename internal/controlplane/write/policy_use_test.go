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
			callCount := 0
			var capturedSubject string

			policy := func(subject string) (bool, error) {
				callCount++
				capturedSubject = subject
				return tt.policyResult, tt.policyError
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
			if gotBool != tt.wantBool {
				t.Errorf("Decide() bool = %v, want %v", gotBool, tt.wantBool)
			}
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("Decide() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(gotErr, tt.policyError) {
				t.Errorf("Decide() error = %v, want wrapped error containing %v", gotErr, tt.policyError)
			}
		})
	}
}
