// Purpose: PendingEntry.ActionClass fail-closed coverage (R-21.230,
//
//	P1-E23-W5-S48-T4). Split from approval_queue_admit_test.go under
//	Art.10.3's 300-line cap; same fixtures (approval_queue_test.go).
//
// SPORT: internal/policy PendingEntry.ActionClass/TEST (P1-E23-W5-S48-T4).
package policy

import (
	"context"
	"testing"
)

// TestGetPendingActionClassFailsClosedWhenCapabilityIsGone proves
// classOf's fail-closed path: a capability that was de-registered between
// Enqueue and GetPending resolves to the invalid zero ActionClass, which
// RemoteApprovabilityMatrix.CanBridge already refuses — never a guessed
// or cached class from admission time.
func TestGetPendingActionClassFailsClosedWhenCapabilityIsGone(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	f.enqueue(t, "edit-a")
	if err := f.reg.Remove(ctx, approvalCap().Name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	pending, err := f.queue.GetPending(ctx)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("GetPending returned %d entries, want 1", len(pending))
	}
	if pending[0].ActionClass.Valid() {
		t.Fatalf("ActionClass = %s, want the invalid zero value once the capability is gone",
			pending[0].ActionClass)
	}
	if (RemoteApprovabilityMatrix{}).CanBridge(pending[0].ActionClass) {
		t.Error("CanBridge admitted the invalid zero class; §5.24 is an allow-list, not a default-allow")
	}
}
