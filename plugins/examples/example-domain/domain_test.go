package main

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func assertInvalidInput(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cerr *cascade.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("error is not *cascade.Error: %v", err)
	}
	if cerr.Kind != cascade.KindInvalidInput {
		t.Fatalf("Kind = %v, want %v", cerr.Kind, cascade.KindInvalidInput)
	}
}

// TestToCanonical_Valid asserts the real (non-stubbed) raw→canonical stage
// normalizes payload keys and preserves every value and the capture
// timestamp.
func TestToCanonical_Valid(t *testing.T) {
	raw := rawRecord{
		ID:             "rec-1",
		Source:         "sensor-a",
		Payload:        map[string]string{" Kind ": "sample", "SEQ": "1"},
		CapturedAtUnix: 42,
	}
	got, err := toCanonical(raw)
	if err != nil {
		t.Fatalf("toCanonical() unexpected error: %v", err)
	}
	if got.ID != raw.ID || got.Source != raw.Source {
		t.Fatalf("toCanonical() = %+v, want id/source preserved from %+v", got, raw)
	}
	if got.NormalizedAtUnix != raw.CapturedAtUnix {
		t.Fatalf("NormalizedAtUnix = %d, want %d", got.NormalizedAtUnix, raw.CapturedAtUnix)
	}
	if got.Fields["kind"] != "sample" || got.Fields["seq"] != "1" {
		t.Fatalf("Fields not normalized/preserved: %+v", got.Fields)
	}
}

// TestToCanonical_RefusalCases is the required majority of refusal cases:
// every malformed raw input this stage must reject.
func TestToCanonical_RefusalCases(t *testing.T) {
	cases := map[string]rawRecord{
		"empty id":     {ID: "", Source: "sensor-a", Payload: map[string]string{"k": "v"}},
		"blank id":     {ID: "   ", Source: "sensor-a", Payload: map[string]string{"k": "v"}},
		"empty source": {ID: "rec-1", Source: "", Payload: map[string]string{"k": "v"}},
		"blank source": {ID: "rec-1", Source: "   ", Payload: map[string]string{"k": "v"}},
		"nil payload":  {ID: "rec-1", Source: "sensor-a", Payload: nil},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := toCanonical(raw)
			assertInvalidInput(t, err)
		})
	}
}

// TestToDerived_Valid asserts the real canonical→derived stage produces a
// deterministic, sorted field summary.
func TestToDerived_Valid(t *testing.T) {
	c := canonicalRecord{
		ID:               "rec-1",
		Source:           "sensor-a",
		Fields:           map[string]string{"seq": "1", "kind": "sample"},
		NormalizedAtUnix: 42,
	}
	got, err := toDerived(c)
	if err != nil {
		t.Fatalf("toDerived() unexpected error: %v", err)
	}
	if got.FieldCount != 2 {
		t.Fatalf("FieldCount = %d, want 2", got.FieldCount)
	}
	if got.FieldSummary != "kind,seq" {
		t.Fatalf("FieldSummary = %q, want %q (sorted)", got.FieldSummary, "kind,seq")
	}
	if got.DerivedFromUnix != c.NormalizedAtUnix {
		t.Fatalf("DerivedFromUnix = %d, want %d", got.DerivedFromUnix, c.NormalizedAtUnix)
	}
}

// TestToDerived_RefusalCases covers the derived stage's own malformed
// inputs.
func TestToDerived_RefusalCases(t *testing.T) {
	cases := map[string]canonicalRecord{
		"empty id":   {ID: "", Fields: map[string]string{"k": "v"}},
		"blank id":   {ID: "   ", Fields: map[string]string{"k": "v"}},
		"nil fields": {ID: "rec-1", Fields: nil},
		"empty map":  {ID: "rec-1", Fields: map[string]string{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := toDerived(c)
			assertInvalidInput(t, err)
		})
	}
}

// TestRunPipeline_RoundTripPreservesFields is the required end-to-end
// round-trip assertion: the id and the capture timestamp survive both
// stages, and every raw payload key is represented in the derived summary.
func TestRunPipeline_RoundTripPreservesFields(t *testing.T) {
	raw := rawRecord{
		ID:             "rec-9",
		Source:         "sensor-b",
		Payload:        map[string]string{"a": "1", "b": "2", "c": "3"},
		CapturedAtUnix: 100,
	}
	got, err := runPipeline(raw)
	if err != nil {
		t.Fatalf("runPipeline() unexpected error: %v", err)
	}
	if got.ID != raw.ID {
		t.Fatalf("ID = %q, want %q (round trip must preserve id)", got.ID, raw.ID)
	}
	if got.DerivedFromUnix != raw.CapturedAtUnix {
		t.Fatalf("DerivedFromUnix = %d, want %d (round trip must preserve timestamp)", got.DerivedFromUnix, raw.CapturedAtUnix)
	}
	if got.FieldCount != len(raw.Payload) {
		t.Fatalf("FieldCount = %d, want %d (round trip must preserve field count)", got.FieldCount, len(raw.Payload))
	}
	if got.FieldSummary != "a,b,c" {
		t.Fatalf("FieldSummary = %q, want %q", got.FieldSummary, "a,b,c")
	}
}

// TestRunPipeline_PropagatesRawRefusal proves the pipeline does not swallow
// a first-stage failure: a malformed raw record must never reach the
// derived stage.
func TestRunPipeline_PropagatesRawRefusal(t *testing.T) {
	_, err := runPipeline(rawRecord{ID: "", Source: "sensor-a", Payload: map[string]string{"k": "v"}})
	assertInvalidInput(t, err)
}
