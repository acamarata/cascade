package agents

import "errors"

var (
	// ErrNotImplemented indicates that the operation is not yet implemented.
	ErrNotImplemented = errors.New("agents: operation unavailable")
	// invalid_agent_state is returned when an agent state is invalid.
	invalid_agent_state = errors.New("invalid agent state")
	// agent_state_conflict is returned when a state transition conflicts with the current state.
	agent_state_conflict = errors.New("agent state conflict")
)
