package consult

import (
	"errors"
	"testing"
)

// TestRejectUnknownClass ensures that unknown, empty, whitespace, padded and differently
// cased class names are rejected with ErrUnknownTaskClass and zero constraints.
func TestRejectUnknownClass(t *testing.T) {
	cases := []string{
		"unknown-task",
		"",
		"   ",
		"  padded  ",
		"Check",
		"UNKNOWN",
	}
	for _, tc := range cases {
		got, err := ValidateWorkClass(tc)
		if got != (ClassConstraints{}) {
			t.Errorf("ValidateWorkClass(%q) returned non-zero constraints: %+v", tc, got)
		}
		if !errors.Is(err, ErrUnknownTaskClass) {
			t.Errorf("ValidateWorkClass(%q) error = %v, want ErrUnknownTaskClass", tc, err)
		}
		if err != nil && err.Error() != "D3543: unknown task class" {
			t.Errorf("ValidateWorkClass(%q) error text = %q, want %q", tc, err.Error(), "D3543: unknown task class")
		}
	}
}
