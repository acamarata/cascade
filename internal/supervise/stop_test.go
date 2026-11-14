package supervise

import (
	"context"
	"errors"
	"testing"
)

// TestStopRestartDeclarations verifies the Supervisor zero value and that the Stop and Restart stubs return ErrNotImplemented.
func TestStopRestartDeclarations(t *testing.T) {
	ctx := context.Background()
	var s Supervisor

	if s != (Supervisor{}) {
		t.Fatalf("zero-value Supervisor = %#v, want %#v", s, Supervisor{})
	}

	if err := Stop(ctx, &s); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Stop = %v, want %v", err, ErrNotImplemented)
	}

	if err := Restart(ctx, &s); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Restart = %v, want %v", err, ErrNotImplemented)
	}
}
