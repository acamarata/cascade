package hookpacks

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks/canon"
)

func TestKnownEventTypesSubsetOfCanon(t *testing.T) {
	if len(knownEventTypes) == 0 {
		t.Fatal("knownEventTypes is empty")
	}
	for name := range knownEventTypes {
		ev, err := canon.Parse(string(name))
		if err != nil {
			t.Errorf("knownEventTypes %q is not a canon event: %v", name, err)
			continue
		}
		if string(ev) != string(name) {
			t.Errorf("canon.Parse(%q) = %q", name, ev)
		}
	}
	if _, err := canon.Parse("stop"); err == nil {
		t.Fatal("canon.Parse accepted a case variant; subset check is vacuous")
	}
}
