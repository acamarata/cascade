package crunch

import (
	"errors"
	"testing"
)

func TestNoEscalateBelowThresholds(t *testing.T) {
	res, err := Escalate(EscalationTicket{
		ID:            "id1",
		Lane:          "junior",
		GCFailures:    2,
		ModelFailures: 1,
		Attempts:      0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Escalated {
		t.Errorf("expected Escalated=false, got true")
	}
	if !res.AllowRetry {
		t.Errorf("expected AllowRetry=true, got false")
	}
	if res.Lane != "junior" {
		t.Errorf("expected Lane=junior, got %s", res.Lane)
	}

	// empty ID
	_, err = Escalate(EscalationTicket{ID: "", Lane: "junior"})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for empty ID, got %v", err)
	}

	// whitespace ID
	_, err = Escalate(EscalationTicket{ID: "   ", Lane: "junior"})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for whitespace ID, got %v", err)
	}

	// empty Lane
	_, err = Escalate(EscalationTicket{ID: "id1", Lane: ""})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for empty Lane, got %v", err)
	}

	// whitespace Lane
	_, err = Escalate(EscalationTicket{ID: "id1", Lane: "   "})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for whitespace Lane, got %v", err)
	}

	// negative GCFailures
	_, err = Escalate(EscalationTicket{ID: "id1", Lane: "junior", GCFailures: -1})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for negative GCFailures, got %v", err)
	}

	// negative ModelFailures
	_, err = Escalate(EscalationTicket{ID: "id1", Lane: "junior", ModelFailures: -1})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for negative ModelFailures, got %v", err)
	}

	// negative Attempts
	_, err = Escalate(EscalationTicket{ID: "id1", Lane: "junior", Attempts: -1})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for negative Attempts, got %v", err)
	}

	// non-senior Attempts>0
	_, err = Escalate(EscalationTicket{ID: "id1", Lane: "junior", Attempts: 1})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for non-senior Attempts>0, got %v", err)
	}
}

func TestNegativeExhaustedRetriesFailsClosed(t *testing.T) {
	res, err := Escalate(EscalationTicket{
		ID:       "id1",
		Lane:     "senior",
		Attempts: 3,
	})
	if res != (EscalationResult{}) {
		t.Errorf("expected zero result for exhausted senior attempts, got %+v", res)
	}
	if !errors.Is(err, ErrEscalationExhausted) {
		t.Errorf("expected ErrEscalationExhausted, got %v", err)
	}
	if err.Error() != "CRUN:002_EXHAUSTED" {
		t.Errorf("expected error text CRUN:002_EXHAUSTED, got %s", err.Error())
	}

	_, err = Escalate(EscalationTicket{ID: "id1", Lane: "senior", Attempts: -1})
	if !errors.Is(err, ErrInvalidEscalation) {
		t.Errorf("expected ErrInvalidEscalation for negative Attempts, got %v", err)
	}
}
