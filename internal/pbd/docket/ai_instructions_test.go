package docket

import (
	"errors"
	"testing"
)

func TestResolveAIInstructionsOrder(t *testing.T) {
	got, err := ResolveAIInstructions("P", "A", "T", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "P\nA\nT"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveAIInstructionsTicketOnly(t *testing.T) {
	got, err := ResolveAIInstructions("", "", "T", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "T"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveAIInstructionsOverCap(t *testing.T) {
	prc := "1234567890123456789012345" // 25 chars
	pac := "1234567890123456789012345" // 25 chars
	_, err := ResolveAIInstructions(prc, pac, "", 10)
	if !errors.Is(err, ErrAIInstrOverCap) {
		t.Errorf("expected ErrAIInstrOverCap, got %v", err)
	}
}

func TestResolveAIInstructionsEmptyTicket(t *testing.T) {
	got, err := ResolveAIInstructions("P", "A", "", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "P\nA"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
