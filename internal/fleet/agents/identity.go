package agents

// AgentState represents the lifecycle state of an Agent.
type AgentState string

const (
	// ACTIVE is the normal operating state.
	ACTIVE AgentState = "ACTIVE"
	// SUSPENDED is a paused state.
	SUSPENDED AgentState = "SUSPENDED"
	// RETIRED is a terminal state.
	RETIRED AgentState = "RETIRED"
)

// Agent represents a fleet agent identity.
type Agent struct {
	ID    string
	State AgentState
}

// NewAgent creates a new Agent stub.
func NewAgent(_ string) (*Agent, error) {
	return nil, ErrNotImplemented
}

// Transition changes the agent to the target state.
func (a *Agent) Transition(_ AgentState) error {
	return ErrNotImplemented
}
