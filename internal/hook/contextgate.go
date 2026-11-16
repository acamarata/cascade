// Package hook provides session gating types and error contracts.
package hook

// SessionContext holds information about the current session.
type SessionContext struct {
	// IsApp indicates whether the session is an application session.
	IsApp bool
}

// Blocked tool name constants used for session gating.
const (
	opstool  = "opstool"
	cascade1 = "cascade1"
	decide   = "decide"
)

// GateError represents an error returned when a gate rule is violated.
type GateError struct {
	// RuleName is the name of the rule that was violated.
	RuleName string
}

// Error returns the rule name as the error message.
func (e GateError) Error() string {
	return e.RuleName
}
