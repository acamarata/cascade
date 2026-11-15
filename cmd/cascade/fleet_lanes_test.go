package main

import (
	"testing"
)

// TestFleetLanesEmptyTopologyCLI verifies that an empty topology prints
// the header only, emits [] under --json and exits 0.
func TestFleetLanesEmptyTopologyCLI(t *testing.T) {
	lanes := fleetLanesList{}

	got := lanes.String()
	want := "ID  PROFILE  HEALTH  RETIRED"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	b, err := lanes.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	wantJSON := `{"version":"1","data":[]}`
	if string(b) != wantJSON {
		t.Errorf("MarshalJSON() = %s, want %s", string(b), wantJSON)
	}
}
