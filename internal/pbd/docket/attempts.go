// Package docket provides attempt tracking for dockets.
package docket

import "errors"

// Attempt represents a single attempt within an AttemptTicket.
type Attempt struct {
	N       int
	State   string
	Outcome string
}

// AttemptTicket holds a sequence of attempts.
type AttemptTicket struct {
	Attempts []Attempt
}

// ErrInvalidAttempt is returned when input validation fails.
var ErrInvalidAttempt = errors.New("attempt: invalid input")

// OpenAttempt validates the ticket and new attempt, marks any existing open
// attempt as abandoned, and appends the new attempt with state "open".
func OpenAttempt(ticket *AttemptTicket, newAttempt Attempt) error {
	if ticket == nil {
		return ErrInvalidAttempt
	}

	// Validate new attempt fields.
	if newAttempt.N <= 0 {
		return ErrInvalidAttempt
	}
	if newAttempt.Outcome != "" {
		return ErrInvalidAttempt
	}
	if newAttempt.State != "" && newAttempt.State != "open" {
		return ErrInvalidAttempt
	}

	// Validate existing attempts and check N ordering.
	seen := make(map[int]bool)
	for _, a := range ticket.Attempts {
		if a.N <= 0 {
			return ErrInvalidAttempt
		}
		if seen[a.N] {
			return ErrInvalidAttempt
		}
		seen[a.N] = true

		if a.Outcome == "" {
			if a.State != "open" && a.State != "abandoned" {
				return ErrInvalidAttempt
			}
		} else {
			// Terminal attempt: State must be non-empty (preserved byte for byte).
			if a.State == "" {
				return ErrInvalidAttempt
			}
		}

		if newAttempt.N <= a.N {
			return ErrInvalidAttempt
		}
	}

	// Build result slice from a copy to avoid mutating the original on success.
	result := make([]Attempt, len(ticket.Attempts), len(ticket.Attempts)+1)
	copy(result, ticket.Attempts)

	// Mark any existing open attempt without outcome as abandoned.
	for i := range result {
		if result[i].Outcome == "" && result[i].State == "open" {
			result[i].State = "abandoned"
		}
	}

	// Normalize empty state to "open" for the new attempt.
	newAttempt.State = "open"
	result = append(result, newAttempt)

	ticket.Attempts = result
	return nil
}
