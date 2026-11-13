package write

import (
	"testing"
	"time"
)

// TestProcessRestartNotOwnerKind verifies process_restart kind isolation.
func TestProcessRestartNotOwnerKind(t *testing.T) {
	var store AuditStore
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	err := RecordProcessRestart(&store, "T-1", at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := store.List("process_restart")
	if len(events) != 1 {
		t.Fatalf("expected 1 process_restart event, got %d", len(events))
	}
	if events[0].Kind != "process_restart" {
		t.Fatalf("expected kind process_restart, got %s", events[0].Kind)
	}

	ownerEvents := store.List("owner_restart")
	if ownerEvents == nil {
		t.Fatalf("expected non-nil empty slice for owner_restart")
	}
	if len(ownerEvents) != 0 {
		t.Fatalf("expected 0 owner_restart events, got %d", len(ownerEvents))
	}
}
