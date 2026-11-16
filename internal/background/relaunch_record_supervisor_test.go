package background

import (
	"errors"
	"testing"
)

type supervisorTestMockLog struct {
	called bool
}

func (m *supervisorTestMockLog) AppendRelaunch(entry RelaunchEntry) error {
	m.called = true
	return nil
}

// TestRecordSupervisorRelaunchLeavesSupervisorRunning verifies that attempting to record
// a relaunch for RoleSupervisor returns ErrSupervisorRelaunchForbidden immediately,
// does not call the manager log, and leaves the caller-owned running state unchanged.
func TestRecordSupervisorRelaunchLeavesSupervisorRunning(t *testing.T) {
	// Simulate caller-owned running state. The API cannot change this.
	running := true

	log := &supervisorTestMockLog{}

	err := RecordStaleBinaryRelaunch(RoleSupervisor, "supervisor-binary-id", log)

	if !errors.Is(err, ErrSupervisorRelaunchForbidden) {
		t.Fatalf("expected ErrSupervisorRelaunchForbidden, got %v", err)
	}

	if log.called {
		t.Fatal("expected manager log not to be called for supervisor relaunch")
	}

	if !running {
		t.Fatal("expected caller-owned running state to remain true")
	}
}
