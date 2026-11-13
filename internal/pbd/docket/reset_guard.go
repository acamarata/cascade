package docket

import "errors"

// TicketState is the state value used by the reset guard and phase runner.
type TicketState string

// StateFailed is the only state from which a reset may be attempted.
const StateFailed TicketState = "failed"

// StateGreen is a protected state that cannot be reset.
const StateGreen TicketState = "green"

// StateNeedsReview is a protected state that cannot be reset.
const StateNeedsReview TicketState = "needs-review"

// ErrResetProtectedState is returned when a reset is attempted on a protected state.
var ErrResetProtectedState = errors.New("reset: state is protected")

// ErrResetCapExceeded is returned when a failed state has already reached the reset cap.
var ErrResetCapExceeded = errors.New("reset: cap exceeded")

// ErrResetInvalidInput is returned for invalid states or negative reset counts.
var ErrResetInvalidInput = errors.New("reset: invalid input")

// CheckReset reports whether a reset is allowed for the given state and reset count.
// It is a pure validation function and does not mutate any state or counters.
func CheckReset(t TicketState, resets int) error {
	if t == StateGreen || t == StateNeedsReview {
		return ErrResetProtectedState
	}

	if t != StateFailed || resets < 0 {
		return ErrResetInvalidInput
	}

	if resets >= 2 {
		return ErrResetCapExceeded
	}

	return nil
}
