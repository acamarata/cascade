package background

import "time"

// Pass describes the outcome of a background process pass.
type Pass struct {
	Duration time.Duration
	Empty    bool
	Exited   bool
}

// Action represents a signal action returned by Decide.
type Action string

// Actions returned by Decide.
const (
	ActionRestart Action = "restart"
	ActionKill    Action = "kill"
	ActionStop    Action = "stop"
	ActionNone    Action = "none"
)

// Decide determines the action to take after a pass completes.
// emptyRun is the consecutive empty-pass count including the current pass.
func Decide(p Pass, emptyRun int) Action {
	if p.Duration < 0 || emptyRun < 0 {
		return ActionNone
	}
	if !p.Exited {
		return ActionNone
	}
	if !p.Empty {
		return ActionNone
	}
	return ActionRestart
}
