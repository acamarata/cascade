package log

import (
	"errors"
	"testing"
)

// TestReaskNilTurn verifies that a nil turn yields ErrInvalidTurn.
func TestReaskNilTurn(t *testing.T) {
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
}

// TestReaskEmptyPrompt verifies that an empty prompt yields ErrInvalidPrompt
// without calling the turn.
func TestReaskEmptyPrompt(t *testing.T) {
	called := false
	turn := func(_ string) (string, error) {
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
}

// TestReaskUnrelatedErrorNotRetried verifies that an unrelated error is not
// retried and is preserved.
func TestReaskUnrelatedErrorNotRetried(t *testing.T) {
	calls := 0
	wantErr := errors.New("network timeout")
	turn := func(_ string) (string, error) {
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
}

// TestReaskOtherReasonRefusalNotRetried verifies that a refusal with another
// reason is not retried.
func TestReaskOtherReasonRefusalNotRetried(t *testing.T) {
	calls := 0
	refusal := RefusalError{Reason: "policy violation"}
	turn := func(_ string) (string, error) {
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
}

// TestReaskTypedNilRefusalPreservesError verifies that a typed-nil refusal
// pointer is not retried and still surfaces as an error.
func TestReaskTypedNilRefusalPreservesError(t *testing.T) {
	calls := 0
	var nilRefusal *RefusalError
	turn := func(_ string) (string, error) {
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
}
