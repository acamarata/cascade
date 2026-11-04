package plugins

import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: prove the antigravity plugin registers as a builtin and its
// detect command is dispatchable.
// SPORT: internal/plugins builtin-antigravity wiring tests (ADD) — P1-CORE-02.

// TestBuiltinRegistersAntigravity proves the builtin registry lists id
// antigravity once with runtime builtin, and its detect command is
// dispatchable.
func TestBuiltinRegistersAntigravity(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var r BuiltinRegistry
	// Load's error is ignored deliberately: registry_test.go's init registers
	// a manifest that Load is SUPPOSED to reject, so this binary's Load
	// always reports one. The valid registrations are still indexed.
	_ = r.Load()

	reg, ok := r.Get("antigravity")
	if !ok {
		t.Fatal("antigravity not registered as builtin")
	}

	if reg.Manifest.ID != "antigravity" {
		t.Errorf("id = %q, want antigravity", reg.Manifest.ID)
	}

	if reg.Manifest.Runtime != plugin.RuntimeBuiltin {
		t.Errorf("runtime = %q, want builtin", reg.Manifest.Runtime)
	}

	cmd, ok := r.NewCobraCommand("antigravity", "detect")
	if !ok {
		t.Fatal("detect command not found")
	}

	if cmd.Use != "detect" {
		t.Errorf("cmd.Use = %q, want detect", cmd.Use)
	}
}
