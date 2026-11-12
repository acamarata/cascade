package pbd_test

// The registry test lives outside package pbd: internal/plugins imports
// this package to inject the host evidence reader, so an internal test
// importing internal/plugins would be an import cycle.

import (
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd"
)

// TestBuiltinRegistration proves this package's init() really reaches the
// host's compile-time registry: internal/plugins.BuiltinRegistry (the real
// production consumer C/S-05.T7 built) loads it, and the mounted
// "validate" command dispatches through the genuine RunCommand path, not a
// placeholder.
func TestBuiltinRegistration(t *testing.T) {
	var reg plugins.BuiltinRegistry
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	entry, ok := reg.Get(pbd.PluginIDForTest)
	if !ok {
		t.Fatalf("plugin %q not found in builtin registry", pbd.PluginIDForTest)
	}
	// P1-E14-W3-S28-T4 (contract lint) adds `lint` alongside T2's already-
	// landed `validate`, P1-E14-W3-S28-T3 (authoring) adds create/edit/
	// move, P1-E14-W3-S29-T4 adds status/board, P1-E14-W3-S30-T1 adds
	// claim/step/done, and P1-E14-W3-S30-T2 adds dispatch; this proves
	// exactly these eleven are mounted.
	wantCommands := map[string]bool{}
	for _, name := range pbd.CommandNamesForTest {
		wantCommands[name] = true
	}
	if len(entry.Manifest.Provides.Commands) != len(wantCommands) {
		t.Fatalf("commands = %+v, want exactly %v", entry.Manifest.Provides.Commands, wantCommands)
	}
	for _, c := range entry.Manifest.Provides.Commands {
		if !wantCommands[c.Name] {
			t.Errorf("unexpected command %q", c.Name)
		}
	}

	cmd, ok := reg.NewCobraCommand(pbd.PluginIDForTest, pbd.ValidateCommandNameForTest)
	if !ok {
		t.Fatal("NewCobraCommand: not found")
	}
	if cmd.Use != pbd.ValidateCommandNameForTest {
		t.Errorf("cmd.Use = %q", cmd.Use)
	}

	// Prove the mounted command's RunE really reaches this package's
	// RunCommand: an empty tree root arg refuses (RunCommand's own
	// input-validation error), not a cobra/registry placeholder.
	if err := cmd.RunE(cmd, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RunE(nil args) = %v, want KindInvalidInput", err)
	}
}
