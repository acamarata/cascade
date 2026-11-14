// Package agents tracks fleet agent identities and their states.
package agents

import "errors"

var (
	// errInvalidAgentState is returned when an agent state is invalid.
	errInvalidAgentState = errors.New("invalid agent state")
	// errAgentStateConflict is returned when a state transition conflicts with the current state.
	errAgentStateConflict = errors.New("agent state conflict")
)
