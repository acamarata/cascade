package background

import (
	"errors"
	"fmt"
	"testing"
)

// These symbols are the contract a later P2 slice wires; the references keep them compiled and tested until then.
var (
	_ BoundarySupervisor
	_ = ActionStop
	_ = ActionKill
)

// TestDeadRef1Symbols pins the contract symbols a later slice uses.
func TestDeadRef1Symbols(t *testing.T) {

	for _, e := range []error{ErrRateLimited, ErrUnknownChat, ErrBoundarySupervisor, ErrSupervisorUnavailable, ErrEmptyJob, ErrDuplicateRetryEntry, ErrRetryJournal, ErrLiveCountLimit} {
		if e == nil || !errors.Is(fmt.Errorf("wrapped: %w", e), e) {
			t.Fatalf("sentinel %v is nil or lost through %%w wrapping", e)
		}
	}

}
