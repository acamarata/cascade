package background

import (
	"errors"
	"testing"
)

// TestEnableManagerFailsClosed verifies that invalid delegate state returns
// ErrInvalidDelegateState and does not mutate the input.
func TestEnableManagerFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		input *DelegateState
	}{
		{
			name:  "nil state",
			input: nil,
		},
		{
			name:  "Started is true",
			input: &DelegateState{Started: true, Turn: 0, Role: "valid"},
		},
		{
			name:  "positive Turn",
			input: &DelegateState{Started: false, Turn: 1, Role: "valid"},
		},
		{
			name:  "negative Turn",
			input: &DelegateState{Started: false, Turn: -1, Role: "valid"},
		},
		{
			name:  "blank Role",
			input: &DelegateState{Started: false, Turn: 0, Role: ""},
		},
		{
			name:  "whitespace Role",
			input: &DelegateState{Started: false, Turn: 0, Role: "   "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var orig DelegateState
			if tt.input != nil {
				orig = *tt.input
			}

			err := EnableManager(tt.input)

			if !errors.Is(err, ErrInvalidDelegateState) {
				t.Errorf("EnableManager() error = %v, want %v", err, ErrInvalidDelegateState)
			}
			if err != nil && err.Error() != "D4741: invalid delegate state" {
				t.Errorf("EnableManager() error text = %q, want %q", err.Error(), "D4741: invalid delegate state")
			}

			if tt.input != nil {
				if *tt.input != orig {
					t.Errorf("EnableManager() mutated input to %+v, want unchanged %+v", *tt.input, orig)
				}
			}
		})
	}
}
