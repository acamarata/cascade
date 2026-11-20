package statesave_test

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/background/statesave"
)

func TestSaveErrorPaths(t *testing.T) {
	t.Run("nil callback returns Halted and ErrInvalidSave prompt", func(t *testing.T) {
		state, q := statesave.Save(nil)
		if state != statesave.Halted {
			t.Errorf("expected state Halted, got %v", state)
		}
		if q == nil {
			t.Fatal("expected non-nil question")
		}
		if q.Prompt != statesave.ErrInvalidSave.Error() {
			t.Errorf("expected prompt %q, got %q", statesave.ErrInvalidSave.Error(), q.Prompt)
		}
	})

	t.Run("callback returning error returns Halted and error prompt", func(t *testing.T) {
		customErr := errors.New("disk full")
		state, q := statesave.Save(func() error { return customErr })
		if state != statesave.Halted {
			t.Errorf("expected state Halted, got %v", state)
		}
		if q == nil {
			t.Fatal("expected non-nil question")
		}
		if q.Prompt != customErr.Error() {
			t.Errorf("expected prompt %q, got %q", customErr.Error(), q.Prompt)
		}
	})
}
