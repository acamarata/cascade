package runtime

// Purpose: the contract of KnownConfigKeys (P1-DOC-04) — equal to the sorted
//   registry element for element, a copy the caller cannot mutate, and the
//   whole config key surface.

import (
	"slices"
	"testing"
)

func TestKnownConfigKeysSortedCopy(t *testing.T) {
	got := KnownConfigKeys()
	want := sortedKnownKeys()
	if len(got) < 40 {
		t.Fatalf("KnownConfigKeys returned %d keys, want at least 40", len(got))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("KnownConfigKeys differs from sortedKnownKeys:\ngot  %q\nwant %q", got, want)
	}

	got[0] = "mutated.key"
	if again := KnownConfigKeys(); !slices.Equal(again, want) {
		t.Fatalf("mutating a KnownConfigKeys result changed a later call:\ngot  %q\nwant %q", again, want)
	}
}
