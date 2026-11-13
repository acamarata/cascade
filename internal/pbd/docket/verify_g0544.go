package docket

import (
	"fmt"
	"strings"
)

// Error represents a ticket parsing error.
type Error struct {
	Code string
}

// Error returns the string representation of the error.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: verify count invalid", e.Code)
}

// ErrD2225 is returned when a ticket does not contain exactly one verify command.
var ErrD2225 = &Error{Code: "D2225"}

// ParseTicket parses the given ticket lines and validates that it contains
// exactly one verify command.
func ParseTicket(lines []string) error {
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "verify:") {
			count++
		}
	}
	if count != 1 {
		return ErrD2225
	}
	return nil
}
