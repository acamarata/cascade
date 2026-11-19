// Package background provides autonomous worker routing and guard policies.
package background

import (
	"fmt"
)

// HandleAutonomousRoute routes a boundary request according to autonomous policy.
// It returns the result and any error encountered.
//
// Policy:
// - Start with Action=req.Action, Continue=true, zero Cards/Pending/WaitOwner
// - For "boundary" action: replace with Action="noop", Code="M1887"
// - With empty Question: return result without calling supervisor
// - With nonempty Question: require guard==autonomousSilent and nonnil supervisor
// - Call supervisor.Answer once; success sets Route="supervisor" and copies Decision
// - Safest alternative for every boundary is "noop" with Continue=true
func HandleAutonomousRoute(mode Mode, guard SilenceGuard, req BoundaryRequest, supervisor BoundarySupervisor) (BoundaryResult, error) {
	result := BoundaryResult{
		Action:   req.Action,
		Continue: true,
	}

	// Replace boundary action with safest alternative
	if result.Action == "boundary" {
		result.Action = "noop"
		result.Code = "M1887"
	}

	// If no question, return result without calling supervisor
	if req.Question == "" {
		return result, nil
	}

	// With nonempty question, require silent guard and supervisor
	if guard != autonomousSilent {
		return BoundaryResult{}, ErrSupervisorUnavailable
	}

	if supervisor == nil {
		return BoundaryResult{}, ErrSupervisorUnavailable
	}

	// Call supervisor once
	decision, err := supervisor.Answer(req.Question)
	if err != nil {
		return BoundaryResult{}, fmt.Errorf("%w: %w", ErrBoundarySupervisor, err)
	}

	// Success: set route and decision
	result.Route = "supervisor"
	result.Decision = decision

	return result, nil
}
