package relay

import (
	"errors"
	"testing"
)

// TestDeclarationsExist verifies that all exported types, functions,
// and sentinel errors are declared with correct zero values and behavior.
func TestDeclarationsExist(t *testing.T) {
	if ErrNotImplemented == nil || ErrNotImplemented.Error() != "relay: operation unavailable" {
		t.Fatal("ErrNotImplemented sentinel missing or wrong message")
	}
	if ErrNotConfigured == nil {
		t.Fatal("ErrNotConfigured missing")
	}
	if ErrLogWriteFailed == nil {
		t.Fatal("ErrLogWriteFailed missing")
	}
	if ErrQuarantined == nil {
		t.Fatal("ErrQuarantined missing")
	}

	var p Planner
	var s Scrub
	var c Check
	var e EgressGate
	var a AskFunc

	if p != nil || s != nil || c != nil || e != nil || a != nil {
		t.Fatal("function types should have nil zero value")
	}

	ts := TopicState{}
	if ts.Topic != "" || ts.Corrupt != false || ts.Stuck != false {
		t.Fatal("TopicState zero value incorrect")
	}

	r := Relay{}
	if r.Planner != nil || r.Scrub != nil || r.Check != nil || r.EgressGate != nil || r.Ask != nil || r.LogPath != "" {
		t.Fatal("Relay zero value incorrect")
	}

	_, err := LoadState("dummy")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("LoadState should return ErrNotImplemented")
	}

	err = SaveState("dummy", nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("SaveState should return ErrNotImplemented")
	}

	err = Quarantine("dummy")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("Quarantine should return ErrNotImplemented")
	}
}
