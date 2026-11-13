package log

import (
	"testing"
)

// TestKPIRunIDFreshPerStart verifies that two command starts without
// --kpi-run return two different non-empty run ids.
func TestKPIRunIDFreshPerStart(t *testing.T) {
	r := NewRunID("")

	id1 := r.Start()
	id2 := r.Start()

	if id1 == "" || id2 == "" {
		t.Fatal("run ids must not be empty")
	}
	if id1 == id2 {
		t.Fatalf("expected different run ids, both were %q", id1)
	}
}

// TestKPIRunIDOperatorOverride verifies that --kpi-run="run-42" causes
// Start to return "run-42" on every invocation.
func TestKPIRunIDOperatorOverride(t *testing.T) {
	r := NewRunID("run-42")

	for i := 0; i < 3; i++ {
		got := r.Start()
		if got != "run-42" {
			t.Fatalf("iteration %d: expected %q, got %q", i, "run-42", got)
		}
	}
}
