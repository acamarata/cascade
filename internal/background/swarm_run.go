package background

import (
	"errors"
	"fmt"
)

// ErrInvalidAttempt indicates an invalid attempt count or a nil brief callback.
var ErrInvalidAttempt = errors.New("invalid swarm attempt")

// ErrBrief indicates that the brief callback failed.
var ErrBrief = errors.New("brief failed")

// RunSwarmAttempt builds one brief and maps one attempt into ordered events.
func RunSwarmAttempt(a Attempt, brief func() (string, error)) (SwarmResult, error) {
	if a.N <= 0 || brief == nil {
		return SwarmResult{}, ErrInvalidAttempt
	}
	base, err := brief()
	if err != nil {
		return SwarmResult{}, fmt.Errorf("%w: %w", ErrBrief, err)
	}
	next := base
	if a.VerifyTail != "" {
		if next != "" {
			next += "\n\n"
		}
		next += a.VerifyTail
	}
	author, outcome := MapAttempt(a)
	events := []Event{author}
	if outcome.Status == "needs-owner" {
		events = append(events, Event{Stage: "escalate", Kind: "escalate"})
	}
	events = append(events, Event{Stage: "done", Kind: "done"})
	return SwarmResult{Events: events, Outcome: outcome, NextBrief: next}, nil
}
