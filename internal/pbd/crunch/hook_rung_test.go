package crunch

import (
	"errors"
	"testing"
)

// TestRungHookSelectsModel verifies that a hook can override the default rung
// and that a nil hook returns the default.
func TestRungHookSelectsModel(t *testing.T) {
	hook := func(attempt int) (string, error) {
		if attempt == 2 {
			return "B", nil
		}
		return "A", nil
	}

	got, err := SelectRung(2, hook)
	if err != nil {
		t.Fatalf("SelectRung(2, hook) unexpected error: %v", err)
	}
	if got != "B" {
		t.Fatalf("SelectRung(2, hook) = %q, want B", got)
	}

	got, err = SelectRung(0, nil)
	if err != nil {
		t.Fatalf("SelectRung(0, nil) unexpected error: %v", err)
	}
	if got != "A" {
		t.Fatalf("SelectRung(0, nil) = %q, want A", got)
	}
}

// TestRungHookInvalidModel verifies that invalid inputs and outputs produce ErrInvalidHook,
// and that hook errors propagate unchanged.
func TestRungHookInvalidModel(t *testing.T) {
	_, err := SelectRung(-1, nil)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("SelectRung(-1, nil) error = %v, want ErrInvalidHook", err)
	}

	badHook := func(attempt int) (string, error) {
		return "C", nil
	}
	_, err = SelectRung(0, badHook)
	if !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("SelectRung(0, badHook) error = %v, want ErrInvalidHook", err)
	}

	hookErr := HookError{Code: ErrCodeD1542, Msg: "boom"}
	errHook := func(attempt int) (string, error) {
		return "", hookErr
	}
	_, err = SelectRung(0, errHook)
	if !errors.Is(err, hookErr) {
		t.Fatalf("SelectRung(0, errHook) error = %v, want %v", err, hookErr)
	}
}
