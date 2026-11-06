package learn

import (
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
)

func TestLearnEventKinds(t *testing.T) {
	want := []events.EventKind{"learn.apply", "learn.held", "learn.version",
		"learn.revert", "learn.tier_mismatch", "learn.fallback"}
	got := EventKinds()
	if !slices.Equal(got, want) {
		t.Fatalf("EventKinds() = %v, want %v", got, want)
	}
	if LearnEventNamespace != "learn" {
		t.Fatalf("LearnEventNamespace = %q", LearnEventNamespace)
	}
	for _, k := range got {
		if !strings.HasPrefix(string(k), LearnEventNamespace+".") {
			t.Fatalf("kind %q is outside the learn namespace", k)
		}
	}
	got[0] = "tampered"
	if again := EventKinds(); !slices.Equal(again, want) {
		t.Fatalf("mutating the result changed the next call: %v", again)
	}
}
