package graph

import (
	"testing"
)

// TestResolveRunnerOverrideWins verifies that an override takes precedence over an ID.
func TestResolveRunnerOverrideWins(t *testing.T) {
	reg := []Runner{
		{ID: "n1", Host: "10.0.0.1"},
		{ID: "n2", Host: "10.0.0.2"},
	}
	r, err := ResolveRunner(reg, "n2", "n1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ID != "n2" {
		t.Fatalf("expected runner n2, got %s", r.ID)
	}
}

// TestResolveRunnerByIDThenHost verifies that an ID is used when override is empty,
// and host is used when both override and ID are empty.
func TestResolveRunnerByIDThenHost(t *testing.T) {
	reg := []Runner{
		{ID: "n1", Host: "10.0.0.1"},
		{ID: "n3", Host: "10.0.0.5"},
	}

	r, err := ResolveRunner(reg, "", "n1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ID != "n1" {
		t.Fatalf("expected runner n1, got %s", r.ID)
	}

	r, err = ResolveRunner(reg, "", "", "10.0.0.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Host != "10.0.0.5" {
		t.Fatalf("expected runner at host 10.0.0.5, got %s", r.Host)
	}
}

// TestResolveRunnerProductionHostRefused verifies that resolving a runner on a
// production host returns an error and an empty Runner.
func TestResolveRunnerProductionHostRefused(t *testing.T) {
	reg := []Runner{
		{ID: "n1", Host: "5.75.235.42"},
	}
	r, err := ResolveRunner(reg, "", "", "5.75.235.42")
	if err == nil {
		t.Fatalf("expected error for production host, got nil")
	}
	if r.ID != "" || r.Host != "" {
		t.Fatalf("expected empty runner, got %+v", r)
	}
}

// TestResolveRunnerNoMatchErrors verifies that an unknown ID and host returns an error.
func TestResolveRunnerNoMatchErrors(t *testing.T) {
	reg := []Runner{
		{ID: "n1", Host: "10.0.0.1"},
	}
	_, err := ResolveRunner(reg, "", "unknown", "10.0.0.99")
	if err == nil {
		t.Fatalf("expected error for no match, got nil")
	}
}
