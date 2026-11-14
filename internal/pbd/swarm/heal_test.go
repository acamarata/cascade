package swarm

import (
	"errors"
	"testing"
	"time"
)

// TestStaleBaseDetected_HELM13 verifies the types and errors required by ticket p2-helm-13.
func TestStaleBaseDetected_HELM13(t *testing.T) {
	now := time.Now()
	sr := StaleRun{
		RunName:      "run-123",
		Reason:       "stale-base",
		AttemptCount: 1,
		Timestamp:    now,
	}

	if sr.RunName != "run-123" {
		t.Errorf("expected RunName 'run-123', got %q", sr.RunName)
	}
	if sr.Reason != "stale-base" {
		t.Errorf("expected Reason 'stale-base', got %q", sr.Reason)
	}
	if sr.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", sr.AttemptCount)
	}
	if !sr.Timestamp.Equal(now) {
		t.Errorf("expected Timestamp %v, got %v", now, sr.Timestamp)
	}

	hc := DefaultHealConfig()
	if !hc.DryRun {
		t.Errorf("expected HealConfig DryRun to default to true")
	}
	if hc.CurrentMain != "" {
		t.Errorf("expected default HealConfig CurrentMain to be empty, got %q", hc.CurrentMain)
	}

	if ErrNoCurrentMain == nil {
		t.Errorf("ErrNoCurrentMain must be defined")
	}
	if !errors.Is(ErrNoCurrentMain, ErrNoCurrentMain) {
		t.Errorf("ErrNoCurrentMain must match itself via errors.Is")
	}

	if ErrEmptyRunName == nil {
		t.Errorf("ErrEmptyRunName must be defined")
	}
	if !errors.Is(ErrEmptyRunName, ErrEmptyRunName) {
		t.Errorf("ErrEmptyRunName must match itself via errors.Is")
	}
}

// TestStaleBaseDetected exercises additional type construction and validation scenarios.
func TestStaleBaseDetected(t *testing.T) {
	sr := StaleRun{
		RunName:      "run-456",
		Reason:       "supervisor-refused",
		AttemptCount: 3,
		Timestamp:    time.Now(),
	}
	if sr.RunName == "" {
		t.Error("RunName should not be empty")
	}

	hc := HealConfig{
		DryRun:      false,
		CurrentMain: "main",
	}
	if hc.DryRun {
		t.Error("DryRun should be false when explicitly set")
	}
	if hc.CurrentMain != "main" {
		t.Errorf("expected CurrentMain 'main', got %q", hc.CurrentMain)
	}

	hr := HealResult{
		RenamedTo:       "run-456-v2",
		ReopenedTickets: []string{"TICKET-1", "TICKET-2"},
		CarriedAttempts: 3,
		Applied:         true,
	}
	if !hr.Applied {
		t.Error("Applied should be true")
	}
	if len(hr.ReopenedTickets) != 2 {
		t.Errorf("expected 2 ReopenedTickets, got %d", len(hr.ReopenedTickets))
	}
	if hr.CarriedAttempts != 3 {
		t.Errorf("expected CarriedAttempts 3, got %d", hr.CarriedAttempts)
	}
	if hr.RenamedTo != "run-456-v2" {
		t.Errorf("expected RenamedTo 'run-456-v2', got %q", hr.RenamedTo)
	}

	hrZero := HealResult{}
	if hrZero.Applied {
		t.Error("zero-value HealResult should have Applied false")
	}
}
