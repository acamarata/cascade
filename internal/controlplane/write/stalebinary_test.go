package write

import (
	"strings"
	"testing"
)

// recordingRestarter is a fake Restarter that records how many times its
// Restart method is invoked.
type recordingRestarter struct {
	restartCalls int
}

// Restart records the call and returns nil. It never touches any real
// process, CLI, launchd, or network resource.
func (r *recordingRestarter) Restart() error {
	r.restartCalls++
	return nil
}

// TestStaleBinaryFindingKind verifies that the finding is classified as
// daemon_stale_binary.
func TestStaleBinaryFindingKind(t *testing.T) {
	r := &recordingRestarter{}
	f := StaleBinaryFinding(r)

	if f.Kind != "daemon_stale_binary" {
		t.Fatalf("finding Kind = %q, want %q", f.Kind, "daemon_stale_binary")
	}
}

// TestFindingStatesNoAutoRestart verifies that the finding text explicitly
// states that no automatic restart is performed.
func TestFindingStatesNoAutoRestart(t *testing.T) {
	r := &recordingRestarter{}
	f := StaleBinaryFinding(r)

	if !strings.Contains(f.Text, "no automatic restart") {
		t.Fatalf("finding Text = %q, want it to contain %q", f.Text, "no automatic restart")
	}
}

// TestFindingRequiresOwnerAction verifies that the finding text requires
// owner action for a restart.
func TestFindingRequiresOwnerAction(t *testing.T) {
	r := &recordingRestarter{}
	f := StaleBinaryFinding(r)

	if !strings.Contains(f.Text, "owner") {
		t.Fatalf("finding Text = %q, want it to contain %q", f.Text, "owner")
	}

	if !strings.Contains(f.Text, "restart") {
		t.Fatalf("finding Text = %q, want it to contain %q", f.Text, "restart")
	}
}

// TestFindingTriggersNoRestart verifies that constructing the finding never
// calls the provided Restarter.
func TestFindingTriggersNoRestart(t *testing.T) {
	r := &recordingRestarter{}

	_ = StaleBinaryFinding(r)

	if r.restartCalls != 0 {
		t.Fatalf("restartCalls = %d, want 0", r.restartCalls)
	}
}
