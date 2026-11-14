// Package docket provides ticket validation for the briefer pipeline.
package docket

import (
	"errors"
	"regexp"
	"strings"
)

// Decision represents the outcome of a ticket ID validation.
type Decision int

const (
	// Proceed indicates the ticket ID is valid and found.
	Proceed Decision = iota
	// FallThrough indicates the ticket ID is valid but not found.
	FallThrough
	// Halt indicates the ticket ID is invalid or duplicate.
	Halt
)

// ErrD1442 is returned when a ticket ID violates requirement D1442.
var ErrD1442 = errors.New("D1442: invalid ticket id")

var ticketPattern = regexp.MustCompile(`^P[0-9]+-[A-Z]+-[0-9]{2,}$`)

// ValidateID checks a ticket ID against existing IDs and returns a decision.
// It halts on unparsable, malformed, or duplicate IDs per D1442.
// It falls through when the ID is valid but not found.
// It proceeds when the ID is valid and found.
func ValidateID(id string, existing []string) (Decision, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, " \t\n\r") {
		return Halt, ErrD1442
	}

	if !ticketPattern.MatchString(id) {
		return Halt, ErrD1442
	}

	count := 0
	for _, e := range existing {
		if e == id {
			count++
		}
	}

	if count > 1 {
		return Halt, ErrD1442
	}

	if count == 1 {
		return Proceed, nil
	}

	return FallThrough, nil
}
