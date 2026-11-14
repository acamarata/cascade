// Package crunch provides escalation logic for failure tracking.
package crunch

import (
	"errors"
	"strings"
)

// EscalationTicket holds the input data for an escalation decision.
type EscalationTicket struct {
	// ID is the unique identifier of the escalation request.
	ID string
	// Lane is the processing lane for the request.
	Lane string
	// GCFailures is the number of garbage collection failures.
	GCFailures int
	// ModelFailures is the number of model failures.
	ModelFailures int
	// Attempts is the number of attempts made so far.
	Attempts int
}

// EscalationResult holds the outcome of an escalation decision.
type EscalationResult struct {
	// Escalated reports whether the request was escalated.
	Escalated bool
	// Lane is the resulting lane.
	Lane string
	// AllowRetry reports whether a retry is permitted.
	AllowRetry bool
}

// ErrEscalationExhausted is returned when retry attempts are exhausted.
var ErrEscalationExhausted = errors.New("CRUN:002_EXHAUSTED")

// ErrInvalidEscalation is returned when escalation input is invalid.
var ErrInvalidEscalation = errors.New("crunch: invalid escalation input")

// Escalate evaluates the ticket and returns an escalation result or error.
func Escalate(t EscalationTicket) (EscalationResult, error) {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Lane) == "" {
		return EscalationResult{}, ErrInvalidEscalation
	}
	if t.GCFailures < 0 || t.ModelFailures < 0 || t.Attempts < 0 {
		return EscalationResult{}, ErrInvalidEscalation
	}
	if t.Attempts > 0 && t.Lane != "senior" {
		return EscalationResult{}, ErrInvalidEscalation
	}

	// Senior lane with more than 2 attempts is exhausted.
	if t.Lane == "senior" && t.Attempts > 2 {
		return EscalationResult{}, ErrEscalationExhausted
	}

	// Threshold met: escalate to senior lane.
	if t.GCFailures >= 3 || t.ModelFailures >= 2 {
		return EscalationResult{
			Escalated:  true,
			Lane:       "senior",
			AllowRetry: t.Attempts < 2,
		}, nil
	}

	// Below thresholds: preserve lane, no escalation.
	allowRetry := t.Lane != "senior" || t.Attempts != 2

	return EscalationResult{
		Escalated:  false,
		Lane:       t.Lane,
		AllowRetry: allowRetry,
	}, nil
}
