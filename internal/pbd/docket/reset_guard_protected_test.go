package docket

import (
	"errors"
	"testing"
)

// TestResetGreenProtected verifies that StateGreen returns ErrResetProtectedState
// regardless of the resets count, checking protection before cap logic.
func TestResetGreenProtected(t *testing.T) {
	tests := []struct {
		name   string
		resets int
	}{
		{"zero resets", 0},
		{"two resets", 2},
		{"negative resets", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReset(StateGreen, tt.resets)
			if !errors.Is(err, ErrResetProtectedState) {
				t.Errorf("CheckReset(StateGreen, %d) = %v, want ErrResetProtectedState", tt.resets, err)
			}
			if err != nil && err.Error() != "reset: state is protected" {
				t.Errorf("CheckReset(StateGreen, %d).Error() = %q, want %q", tt.resets, err.Error(), "reset: state is protected")
			}
		})
	}
}

// TestResetNeedsReviewProtected verifies that StateNeedsReview returns ErrResetProtectedState
// regardless of the resets count, checking protection before cap logic.
func TestResetNeedsReviewProtected(t *testing.T) {
	tests := []struct {
		name   string
		resets int
	}{
		{"zero resets", 0},
		{"two resets", 2},
		{"negative resets", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReset(StateNeedsReview, tt.resets)
			if !errors.Is(err, ErrResetProtectedState) {
				t.Errorf("CheckReset(StateNeedsReview, %d) = %v, want ErrResetProtectedState", tt.resets, err)
			}
			if err != nil && err.Error() != "reset: state is protected" {
				t.Errorf("CheckReset(StateNeedsReview, %d).Error() = %q, want %q", tt.resets, err.Error(), "reset: state is protected")
			}
		})
	}
}
