package conductpolicy

import (
	"errors"
	"testing"
)

// TestErrorsDistinct asserts that the five error variables are pairwise distinct
// and that none of them has an empty message.
func TestErrorsDistinct(t *testing.T) {
	errs := []error{
		ErrUnknownWorkKind,
		ErrUnknownReason,
		ErrSameLane,
		ErrEmptyAnswer,
		ErrFanoutNeedsOverride,
	}

	names := []string{
		"ErrUnknownWorkKind",
		"ErrUnknownReason",
		"ErrSameLane",
		"ErrEmptyAnswer",
		"ErrFanoutNeedsOverride",
	}

	for i, e := range errs {
		if e == nil {
			t.Fatalf("%s is nil", names[i])
		}
		if e.Error() == "" {
			t.Errorf("%s has an empty message", names[i])
		}
	}

	for i := 0; i < len(errs); i++ {
		for j := i + 1; j < len(errs); j++ {
			if errors.Is(errs[i], errs[j]) {
				t.Errorf("%s and %s are errors.Is-equal", names[i], names[j])
			}
		}
	}
}
