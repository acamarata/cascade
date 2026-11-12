// Package write implements control plane write operations.
package write

import (
	"errors"
	"strings"
)

// ErrEmptyGateReason is returned when a closed gate is invoked with an empty or whitespace reason.
var ErrEmptyGateReason = errors.New("D0271")

// Decision represents the outcome of a RunGate check.
type Decision struct {
	Allowed bool
	Reason  string
}

// RunGate evaluates whether an operation is permitted.
// An open gate returns Decision{Allowed:true, Reason:""} regardless of the reason argument.
// A closed gate with a non-whitespace reason returns Decision{Allowed:false, Reason:reason}, preserving the reason verbatim.
// A closed gate with an empty or whitespace reason returns Decision{} and ErrEmptyGateReason.
func RunGate(open bool, reason string) (Decision, error) {
	if open {
		return Decision{Allowed: true, Reason: ""}, nil
	}
	if strings.TrimSpace(reason) == "" {
		return Decision{}, ErrEmptyGateReason
	}
	return Decision{Allowed: false, Reason: reason}, nil
}
