package doneopen

import (
	"errors"
	"testing"
)

func TestDoneOpenUnknownWhyRejected(t *testing.T) {
	status, err := ResolveDoneOpen(Why("bogus"))
	if status != Status("") {
		t.Fatalf("for bogus why: status = %q, want empty status", status)
	}
	if err == nil {
		t.Fatal("for bogus why: expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidWhy) {
		t.Fatalf("for bogus why: error = %v, want errors.Is ErrInvalidWhy", err)
	}
	if got, want := err.Error(), `doneopen: invalid why "bogus"`; got != want {
		t.Fatalf("for bogus why: error message = %q, want %q", got, want)
	}

	status, err = ResolveDoneOpen(Why(""))
	if status != Status("") {
		t.Fatalf("for empty why: status = %q, want empty status", status)
	}
	if err == nil {
		t.Fatal("for empty why: expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidWhy) {
		t.Fatalf("for empty why: error = %v, want errors.Is ErrInvalidWhy", err)
	}
}
