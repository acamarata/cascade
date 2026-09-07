package pbd

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
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

	entry, ok := reg.Get(pluginID)
	if !ok {
		t.Fatalf("plugin %q not found in builtin registry", pluginID)
	}
	if len(entry.Manifest.Provides.Commands) != 1 || entry.Manifest.Provides.Commands[0].Name != validateCommandName {
		t.Fatalf("commands = %+v, want exactly [%q]", entry.Manifest.Provides.Commands, validateCommandName)
	}

	cmd, ok := reg.NewCobraCommand(pluginID, validateCommandName)
	if !ok {
		t.Fatal("NewCobraCommand: not found")
	}
	if cmd.Use != validateCommandName {
		t.Errorf("cmd.Use = %q", cmd.Use)
	}

	// Prove the mounted command's RunE really reaches this package's
	// RunCommand: an empty tree root arg refuses (RunCommand's own
	// input-validation error), not a cobra/registry placeholder.
	if err := cmd.RunE(cmd, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RunE(nil args) = %v, want KindInvalidInput", err)
	}
}

func TestHandlersDispatchToolAndIntentRefuse(t *testing.T) {
	h := handlers{}
	if _, err := h.DispatchTool(context.Background(), "anything", nil); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("DispatchTool err = %v, want KindUnsupported", err)
	}
	if _, err := h.DispatchIntent(context.Background(), "anything", nil); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("DispatchIntent err = %v, want KindUnsupported", err)
	}
}

func TestRunCommandUnknownName(t *testing.T) {
	h := handlers{}
	if err := h.RunCommand(context.Background(), "not-validate", nil); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("err = %v, want KindUnsupported", err)
	}
}

func TestRunCommandCleanTree(t *testing.T) {
	root := t.TempDir()
	mkTicket(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)

	h := handlers{}
	if err := h.RunCommand(context.Background(), validateCommandName, []string{root}); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
}
