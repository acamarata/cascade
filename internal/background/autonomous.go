package background

import "errors"

// BoundaryModeNormal is the mode constant for normal boundary delegation.
const BoundaryModeNormal Mode = "normal"

// BoundaryRequest describes a request to the boundary supervisor or normal delegate.
type BoundaryRequest struct {
	// Action is the requested action.
	Action string
	// Question is the question to ask.
	Question string
}

// BoundaryResult describes the result returned by a boundary handler.
type BoundaryResult struct {
	// Action is the resolved action.
	Action string
	// Route is the routing decision.
	Route string
	// Decision is the outcome decision.
	Decision string
	// Code is the result code.
	Code string
	// Continue indicates whether processing should continue.
	Continue bool
	// Cards is the number of cards produced.
	Cards int
	// Pending is the number of pending items.
	Pending int
	// WaitOwner indicates whether the owner must be waited on.
	WaitOwner bool
}

// BoundarySupervisor answers questions from the boundary layer.
type BoundarySupervisor interface {
	// Answer responds to the given question.
	Answer(question string) (string, error)
}

// BoundaryNormal is the callback signature for normal mode delegation.
type BoundaryNormal func(req BoundaryRequest) (BoundaryResult, error)

var (
	// ErrInvalidBoundaryRequest is returned when a boundary request cannot be processed.
	ErrInvalidBoundaryRequest = errors.New("invalid boundary request")
	// ErrSupervisorUnavailable is returned when no supervisor is available.
	ErrSupervisorUnavailable = errors.New("supervisor unavailable")
	// ErrBoundarySupervisor is returned when the supervisor fails to answer.
	ErrBoundarySupervisor = errors.New("supervisor question failed")
)

// HandleBoundary processes a boundary request according to the given mode.
// In BoundaryModeNormal it delegates exactly once to the normal callback and
// returns its result and error unchanged. A nil normal callback returns
// ErrInvalidBoundaryRequest. Unknown, empty, or ModeInteractive modes also
// return a zero result with ErrInvalidBoundaryRequest.
func HandleBoundary(mode Mode, req BoundaryRequest, normal BoundaryNormal) (BoundaryResult, error) {
	switch mode {
	case BoundaryModeNormal:
		if normal == nil {
			return BoundaryResult{}, ErrInvalidBoundaryRequest
		}
		return normal(req)
	default:
		return BoundaryResult{}, ErrInvalidBoundaryRequest
	}
}
