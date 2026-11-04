package initconfig

// Purpose: the contract of KnownKeys (P1-DOC-04) — sorted, a copy the
//   caller cannot mutate, and the whole schema surface.

import (
	"slices"
	"testing"
)

func TestKnownKeysSortedCopy(t *testing.T) {
	first := KnownKeys()
	if len(first) < 10 {
		t.Fatalf("KnownKeys returned %d keys, want at least 10", len(first))
	}
	if !slices.IsSorted(first) {
		t.Fatalf("KnownKeys not sorted: %q", first)
	}

	first[0] = "mutated.key"
	second := KnownKeys()
	if !slices.IsSorted(second) {
		t.Fatalf("KnownKeys not sorted after caller mutation: %q", second)
	}
	if slices.Contains(second, "mutated.key") {
		t.Fatal("mutating a KnownKeys result changed a later call")
	}
}
