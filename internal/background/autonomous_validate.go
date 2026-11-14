// Package background provides autonomous worker guards.
package background

import "strings"

// HandleAutonomous validates and processes boundary requests in autonomous mode.
// In ModeAutonomous it validates the action and question first; no callbacks or
// effects occur on error. The normal callback is never called and may be nil.
// Unknown or empty mode and ModeInteractive return a zero result with
// ErrInvalidBoundaryRequest without invoking any callbacks.
// Routing agent questions is conditioned solely on guard == autonomousSilent
// after mode validation.
// Traceability: D4699, D4702, D4704.
func HandleAutonomous(mode Mode, guard SilenceGuard, req BoundaryRequest, supervisor BoundarySupervisor, _ BoundaryNormal) (BoundaryResult, error) {
	// D4699: mode validation
	if mode != ModeAutonomous {
		return BoundaryResult{}, ErrInvalidBoundaryRequest
	}

	// D4702: action validation
	switch req.Action {
	case "shell", "write", "network", "publish", "delete", "owner_prompt":
		return BoundaryResult{Action: "noop", Code: "M1887", Continue: true}, nil
	case "read", "noop":
		// allowed non-boundary actions remain unchanged
	default:
		// empty or unknown action is invalid
		return BoundaryResult{}, ErrInvalidBoundaryRequest
	}

	// D4704: question validation - whitespace-only question is invalid when nonempty
	if req.Question != "" && strings.TrimSpace(req.Question) == "" {
		return BoundaryResult{}, ErrInvalidBoundaryRequest
	}

	// Routing agent questions conditioned solely on guard == autonomousSilent
	if req.Question != "" && guard == autonomousSilent && supervisor != nil {
		ans, err := supervisor.Answer(req.Question)
		if err != nil {
			return BoundaryResult{}, err
		}
		return BoundaryResult{Action: req.Action, Decision: ans}, nil
	}

	return BoundaryResult{Action: req.Action}, nil
}
