package crunch

import (
	"errors"
	"fmt"
	"testing"
)

// TestHedgeCommitWraps pins the sentinel a later hedge slice returns.
func TestHedgeCommitWraps(t *testing.T) {
	if !errors.Is(fmt.Errorf("hedge: %w", ErrHedgeCommit), ErrHedgeCommit) {
		t.Fatal("ErrHedgeCommit is lost through %w wrapping")
	}
}
