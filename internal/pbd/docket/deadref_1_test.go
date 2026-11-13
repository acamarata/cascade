package docket

import (
	"errors"
	"fmt"
	"testing"
)

// TestDeadRef1Symbols pins the contract symbols a later slice uses.
func TestDeadRef1Symbols(t *testing.T) {

	for _, e := range []error{ErrMissingRailfeed} {
		if e == nil || !errors.Is(fmt.Errorf("wrapped: %w", e), e) {
			t.Fatalf("sentinel %v is nil or lost through %%w wrapping", e)
		}
	}

}
