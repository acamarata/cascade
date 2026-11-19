// Package background provides delegate-state validation for manager enablement.
package background

import (
	"errors"
	"strings"
)

// DelegateState holds the local state of a delegate before and during turns.
type DelegateState struct {
	// Started indicates whether the delegate has begun its first turn.
	Started bool
	// Turn is the current turn number; zero is valid before the first turn.
	Turn int
	// Role identifies the delegate's role.
	Role string
	// PermsActive indicates whether manager permissions are active.
	PermsActive bool
}

// ErrInvalidDelegateState is returned when EnableManager receives invalid state.
var ErrInvalidDelegateState = errors.New("D4741: invalid delegate state")

// EnableManager validates the delegate state and activates manager permissions.
// It returns ErrInvalidDelegateState if s is nil, Started is true, Turn is not
// zero, or Role is blank or whitespace-only. On success it sets Role to
// "manager" and PermsActive to true, preserving Turn and Started.
func EnableManager(s *DelegateState) error {
	if s == nil {
		return ErrInvalidDelegateState
	}
	if s.Started {
		return ErrInvalidDelegateState
	}
	if s.Turn != 0 {
		return ErrInvalidDelegateState
	}
	if strings.TrimSpace(s.Role) == "" {
		return ErrInvalidDelegateState
	}
	s.Role = "manager"
	s.PermsActive = true
	return nil
}
