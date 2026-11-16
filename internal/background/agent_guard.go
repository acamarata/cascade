package background

import "fmt"

// AgentOp represents an agent operation type.
type AgentOp string

const (
	// OpList is the list operation.
	OpList AgentOp = "list"
	// OpShow is the show operation.
	OpShow AgentOp = "show"
	// OpSpawn is the spawn operation.
	OpSpawn AgentOp = "spawn"
	// OpMessage is the message operation.
	OpMessage AgentOp = "message"
)

// GuardError represents a guard denial with a code and message.
type GuardError struct {
	Code string
	Msg  string
}

// Error returns the formatted error string.
func (e GuardError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

// CheckWebchatQuery fails closed webchat queries once a research agent performed local search.
func CheckWebchatQuery(searchPerformed bool) error {
	if searchPerformed {
		return GuardError{Code: "D3206", Msg: "webchat query denied after local search"}
	}
	return nil
}

// CheckAgentOp denies agent operations unless a valid Super Code run token is present.
func CheckAgentOp(op AgentOp, runToken string) error {
	switch op {
	case OpList, OpShow, OpSpawn, OpMessage:
	default:
		return GuardError{Code: "D3318", Msg: "unknown agent operation"}
	}
	if runToken == "" {
		return GuardError{Code: "D3318", Msg: "agent operation denied without run token"}
	}
	return nil
}
