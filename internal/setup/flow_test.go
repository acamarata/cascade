package setup

import (
	"context"
	"errors"
	"testing"
)

// TestFlowTypesAndStateTransitions verifies the setup flow types, state tracking,
// method contracts, and specific error definitions.
func TestFlowTypesAndStateTransitions(t *testing.T) {
	t.Run("initial state is empty", func(t *testing.T) {
		f := NewFlow()
		s := f.Status()
		if s.SignIn || s.OllamaStarted || s.BridgeStarted || s.GrantApplied || s.DoctorReady || s.Ready {
			t.Fatal("expected all state fields to be false initially")
		}
	})

	t.Run("start resets state", func(t *testing.T) {
		f := NewFlow()
		ctx := context.Background()
		if err := f.Start(ctx); err != nil {
			t.Fatalf("Start returned unexpected error: %v", err)
		}
		s := f.Status()
		if s.Ready {
			t.Fatal("expected Ready to be false after Start")
		}
	})

	t.Run("resume does not error on initial state", func(t *testing.T) {
		f := NewFlow()
		ctx := context.Background()
		if err := f.Resume(ctx); err != nil {
			t.Fatalf("Resume returned unexpected error: %v", err)
		}
	})

	t.Run("error types are defined and distinct", func(t *testing.T) {
		errs := []error{
			ErrSignInFailed,
			ErrOllamaStartFailed,
			ErrBridgeStartFailed,
			ErrGrantApplyFailed,
		}
		for i, e1 := range errs {
			if e1 == nil {
				t.Fatalf("error at index %d is nil", i)
			}
			for j, e2 := range errs {
				if i != j && errors.Is(e1, e2) {
					t.Fatalf("errors at index %d and %d should be distinct", i, j)
				}
			}
		}
	})

	t.Run("state tracks all five steps", func(t *testing.T) {
		s := State{
			SignIn:        true,
			OllamaStarted: true,
			BridgeStarted: true,
			GrantApplied:  true,
			DoctorReady:   true,
			Ready:         true,
		}
		if !s.SignIn || !s.OllamaStarted || !s.BridgeStarted || !s.GrantApplied || !s.DoctorReady || !s.Ready {
			t.Fatal("state should track all five steps plus ready flag")
		}
	})
}
