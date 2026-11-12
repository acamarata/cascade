package pbd

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
