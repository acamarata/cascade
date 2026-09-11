package evidence

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestIndexURIAndParse(t *testing.T) {
	uri := IndexURI("run-42")
	if uri != "evidence://run-42" {
		t.Errorf("IndexURI = %q, want evidence://run-42", uri)
	}
	runID, err := ParseIndexURI(uri)
	if err != nil {
		t.Fatalf("ParseIndexURI: %v", err)
	}
	if runID != "run-42" {
		t.Errorf("ParseIndexURI = %q, want run-42", runID)
	}
}

func TestParseIndexURIRejectsForeignScheme(t *testing.T) {
	for _, uri := range []string{"claim://run-1", "evidence://", "run-1"} {
		if _, err := ParseIndexURI(uri); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("ParseIndexURI(%q) = %v, want typed invalid-input", uri, err)
		}
	}
}

func TestResolveIndexByteIdenticalAcrossCalls(t *testing.T) {
	s, _ := newStore(t)
	mustPutClaimWithEvidence(t, s, "CLM-IX-1")
	mustPutClaimWithEvidence(t, s, "CLM-IX-2")

	uri := IndexURI("run-1")
	first, err := s.ResolveIndex(context.Background(), uri)
	if err != nil {
		t.Fatalf("ResolveIndex: %v", err)
	}
	second, err := s.ResolveIndex(context.Background(), uri)
	if err != nil {
		t.Fatalf("ResolveIndex (2nd): %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("ResolveIndex not stable: %d vs %d claims", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("ResolveIndex not byte-identical at %d: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}
