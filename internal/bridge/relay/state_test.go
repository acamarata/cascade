package relay

import (
	"errors"
	"testing"
)

// TestDeclarationsExist verifies the declared sentinel errors and the zero values of the declared types.
func TestDeclarationsExist(t *testing.T) {
	errs := []error{ErrNotConfigured, ErrLogWriteFailed, ErrQuarantined}
	for i, e := range errs {
		if e == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		for j, o := range errs {
			if i != j && errors.Is(e, o) {
				t.Errorf("sentinels %d and %d match each other", i, j)
			}
		}
	}
	var p Planner
	var s Scrub
	var c Check
	var e EgressGate
	var a AskFunc
	if p != nil || s != nil || c != nil || e != nil || a != nil {
		t.Fatal("function types should have nil zero value")
	}
	if ts := (TopicState{}); ts.Topic != "" || ts.Corrupt || ts.Stuck {
		t.Fatal("TopicState zero value incorrect")
	}
	r := Relay{}
	if r.Planner != nil || r.Scrub != nil || r.Check != nil || r.EgressGate != nil || r.Ask != nil || r.LogPath != "" {
		t.Fatal("Relay zero value incorrect")
	}
}
