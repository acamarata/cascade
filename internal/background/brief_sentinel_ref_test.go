package background

import (
	"errors"
	"fmt"
	"testing"
)

// TestBriefTaskNotFoundWraps pins the sentinel a later queue slice returns.
func TestBriefTaskNotFoundWraps(t *testing.T) {
	if !errors.Is(fmt.Errorf("lookup: %w", ErrBriefTaskNotFound), ErrBriefTaskNotFound) {
		t.Fatal("ErrBriefTaskNotFound is lost through %w wrapping")
	}
}
