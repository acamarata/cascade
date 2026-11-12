package background

import (
	"encoding/json"
	"testing"
)

// TestEmitStageEvent verifies the pure mapping from Attempt to Event and Outcome.
func TestEmitStageEvent(t *testing.T) {
	a := Attempt{N: 1, VerifyTail: "", Err: ""}
	ev, out := MapAttempt(a)

	expectedEv := Event{Stage: "author", Kind: "stage"}
	if ev != expectedEv {
		t.Fatalf("expected event %+v, got %+v", expectedEv, ev)
	}
	if ev.Name != "" {
		t.Fatalf("expected Name empty, got %q", ev.Name)
	}
	if !ev.At.IsZero() {
		t.Fatalf("expected At zero, got %v", ev.At)
	}

	expectedOut := Outcome{Status: "done"}
	if out != expectedOut {
		t.Fatalf("expected outcome %+v, got %+v", expectedOut, out)
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if _, ok := decoded["name"]; ok {
		t.Fatalf("name should be omitted from JSON when empty")
	}
	if decoded["stage"] != "author" {
		t.Fatalf("expected stage 'author', got %v", decoded["stage"])
	}
	if decoded["kind"] != "stage" {
		t.Fatalf("expected kind 'stage', got %v", decoded["kind"])
	}
	if atVal, ok := decoded["at"]; ok && atVal != "0001-01-01T00:00:00Z" && atVal != "" {
		t.Fatalf("at should be omitted or zero in JSON, got %v", atVal)
	}
}

// TestMapAttemptErr verifies the mapping when an error is present.
func TestMapAttemptErr(t *testing.T) {
	a := Attempt{N: 2, VerifyTail: "tail", Err: "something failed"}
	ev, out := MapAttempt(a)

	expectedEv := Event{Stage: "author", Kind: "stage"}
	if ev != expectedEv {
		t.Fatalf("expected event %+v, got %+v", expectedEv, ev)
	}

	expectedOut := Outcome{Status: "needs-owner", Detail: "something failed"}
	if out != expectedOut {
		t.Fatalf("expected outcome %+v, got %+v", expectedOut, out)
	}
}
