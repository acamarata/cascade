package log

import (
	"errors"
	"testing"
)

// TestReaskIgnoresOtherErrors verifies that unrelated errors, other-reason
// refusals, and typed-nil refusal pointers are not retried and preserve the
// original error. It also checks nil turn and empty prompt validation.
func TestReaskIgnoresOtherErrors(t *testing.T) {
	t.Run("nil turn returns ErrInvalidTurn", func(t *testing.T) {
		res := Reask(nil, "hello")
		if !res.Failed {
			t.Fatal("expected Failed")
		}
		if !res.Continue {
			t.Fatal("expected Continue")
		}
		if !errors.Is(res.Err, ErrInvalidTurn) {
			t.Fatalf("expected ErrInvalidTurn, got %v", res.Err)
		}
	})

	t.Run("empty prompt returns ErrInvalidPrompt", func(t *testing.T) {
		called := false
		turn := func(prompt string) (string, error) {
			called = true
			return "", nil
		}
		res := Reask(turn, "")
		if called {
			t.Fatal("turn should not be called for empty prompt")
		}
		if !res.Failed {
			t.Fatal("expected Failed")
		}
		if !res.Continue {
			t.Fatal("expected Continue")
		}
		if !errors.Is(res.Err, ErrInvalidPrompt) {
			t.Fatalf("expected ErrInvalidPrompt, got %v", res.Err)
		}
	})

	t.Run("unrelated error is not retried", func(t *testing.T) {
		calls := 0
		wantErr := errors.New("network timeout")
		turn := func(prompt string) (string, error) {
			calls++
			return "output", wantErr
		}
		res := Reask(turn, "hello")
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
		if !res.Failed {
			t.Fatal("expected Failed")
		}
		if !errors.Is(res.Err, wantErr) {
			t.Fatalf("expected %v, got %v", wantErr, res.Err)
		}
		if res.Output != "" {
			t.Fatalf("expected empty output on error, got %q", res.Output)
		}
	})

	t.Run("other-reason refusal is not retried", func(t *testing.T) {
		calls := 0
		refusal := RefusalError{Reason: "policy violation"}
		turn := func(prompt string) (string, error) {
			calls++
			return "", refusal
		}
		res := Reask(turn, "hello")
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
		if !res.Failed {
			t.Fatal("expected Failed")
		}
		if !errors.Is(res.Err, refusal) {
			t.Fatalf("expected refusal error, got %v", res.Err)
		}
	})

	t.Run("typed-nil refusal pointer preserves error", func(t *testing.T) {
		calls := 0
		var nilRefusal *RefusalError
		turn := func(prompt string) (string, error) {
			calls++
			return "data", nilRefusal
		}
		res := Reask(turn, "hello")
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
		if !res.Failed {
			t.Fatal("expected Failed")
		}
		if res.Err == nil {
			t.Fatal("expected non-nil error for typed-nil pointer")
		}
		if res.Output != "" {
			t.Fatalf("expected empty output on error, got %q", res.Output)
		}
	})
}
