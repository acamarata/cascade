package hookpacks_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// TestToolGateConstants pins the tool-gate wire identifiers and timeouts:
// the RPC method, its capability name, the 5 s client deadline and a hook
// timeout that exceeds that deadline.
func TestToolGateConstants(t *testing.T) {
	if hookpacks.MethodToolGate != "fleet.sessions.tool_gate" {
		t.Errorf("MethodToolGate = %q", hookpacks.MethodToolGate)
	}
	if hookpacks.ToolGateCapability != "tool_gate" {
		t.Errorf("ToolGateCapability = %q", hookpacks.ToolGateCapability)
	}
	if hookpacks.ToolGateClientDeadline != 5 {
		t.Errorf("ToolGateClientDeadline = %d, want 5 seconds", hookpacks.ToolGateClientDeadline)
	}
	if hookpacks.ToolGateHookTimeoutSeconds <= hookpacks.ToolGateClientDeadline {
		t.Errorf("hook timeout %d s does not exceed the client deadline %d s",
			hookpacks.ToolGateHookTimeoutSeconds, hookpacks.ToolGateClientDeadline)
	}
}
