package hj

import "errors"

// Action represents the routing decision for an exit event.
type Action int

const (
	// ActionUnhandled means the exit code is not handled by this router.
	ActionUnhandled Action = iota
	// ActionHandBack means the run should be handed back to the caller.
	ActionHandBack
	// ActionReportError means the error should be reported and no second host run allowed.
	ActionReportError
	// ActionFailClosed means the state is invalid or unavailable; no hand-back and no second host run.
	ActionFailClosed
)

// D3917 is the diagnostic code used for exit-3 routing decisions.
const D3917 = "D3917"

// ModelCallState holds the in-memory state about model calls made during a run.
type ModelCallState struct {
	// Unavailable indicates that the model call state could not be determined.
	Unavailable bool
	// ModelCalls is the count of model calls that have already occurred.
	ModelCalls int
}

// Result is the outcome of routing an exit event.
type Result struct {
	// Action is the routing decision.
	Action Action
	// SecondHostRun indicates whether a second host run is permitted.
	SecondHostRun bool
	// Code is the diagnostic code associated with the decision.
	Code string
	// Err contains any error that caused the routing to fail closed.
	Err error
}

// HandleExit3 inspects the model call state to route an exit code 3 event.
// If one or more model calls already occurred, it routes to error reporting and blocks a second host run.
// If exit 3 occurs before any model call, it hands the run back.
// If the model-call state is unavailable or invalid, it fails closed with no hand-back and no second host run.
// For any exit code other than 3, it returns ActionUnhandled.
func HandleExit3(exitCode int, state *ModelCallState) Result {
	if exitCode != 3 {
		return Result{Action: ActionUnhandled}
	}

	if state == nil || state.Unavailable {
		return Result{
			Action:        ActionFailClosed,
			SecondHostRun: false,
			Err:           errors.New("model call state unavailable"),
		}
	}

	if state.ModelCalls > 0 {
		return Result{
			Action:        ActionReportError,
			SecondHostRun: false,
			Code:          D3917,
		}
	}

	return Result{
		Action:        ActionHandBack,
		SecondHostRun: false,
		Code:          D3917,
	}
}
