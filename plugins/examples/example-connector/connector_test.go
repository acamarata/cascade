package main

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestExampleConnector_Describe asserts the real (non-stubbed) Describe
// implementation returns the connector's own source name verbatim.
func TestExampleConnector_Describe(t *testing.T) {
	c := exampleConnector{source: "sensor-a"}
	if got := c.Describe(); got != "sensor-a" {
		t.Fatalf("Describe() = %q, want %q", got, "sensor-a")
	}
}

// TestExampleConnector_Fetch_Valid asserts Fetch produces a real,
// deterministic, non-empty batch for a well-formed connector.
func TestExampleConnector_Fetch_Valid(t *testing.T) {
	c := exampleConnector{source: "sensor-a"}
	records, err := c.Fetch()
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("Fetch() returned %d records, want 2", len(records))
	}
	for i, r := range records {
		if r.Source != "sensor-a" {
			t.Errorf("records[%d].Source = %q, want %q", i, r.Source, "sensor-a")
		}
		if r.ID == "" {
			t.Errorf("records[%d].ID is empty", i)
		}
		if len(r.Payload) == 0 {
			t.Errorf("records[%d].Payload is empty", i)
		}
	}
}

// TestExampleConnector_Fetch_EmptySource is the required refusal case: a
// connector built with an empty (or whitespace-only) source name must fail
// closed with a typed cascade.KindInvalidInput error, never a silent empty
// batch.
func TestExampleConnector_Fetch_EmptySource(t *testing.T) {
	for _, source := range []string{"", "   "} {
		c := exampleConnector{source: source}
		records, err := c.Fetch()
		if err == nil {
			t.Fatalf("Fetch() with source %q: expected error, got nil", source)
		}
		if records != nil {
			t.Fatalf("Fetch() with source %q: expected nil records on error, got %v", source, records)
		}
		var cerr *cascade.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("Fetch() with source %q: error is not *cascade.Error: %v", source, err)
		}
		if cerr.Kind != cascade.KindInvalidInput {
			t.Fatalf("Fetch() with source %q: Kind = %v, want %v", source, cerr.Kind, cascade.KindInvalidInput)
		}
	}
}
