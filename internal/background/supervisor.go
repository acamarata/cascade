package background

import (
	"errors"
	"fmt"
)

// MaxSupervisorReviewRounds is the maximum number of supervisor review
// rounds allowed before the review must fail with error M1885.
const MaxSupervisorReviewRounds = 2

// ErrSupervisorReview is the M1885 error returned when an independent
// supervisor review cannot approve the requested completion.
var ErrSupervisorReview = errors.New("M1885: supervisor review failed")

// SupervisorReview enforces an independent supervisor review before work is
// marked done. It approves only rounds in the range 1 through
// MaxSupervisorReviewRounds when the reviewer is independent. Any other
// input returns false and an error carrying the M1885 code.
func SupervisorReview(round int, independentReviewer bool) (bool, error) {
	if !independentReviewer {
		return false, fmt.Errorf("%w: reviewer is not independent", ErrSupervisorReview)
	}

	if round < 1 || round > MaxSupervisorReviewRounds {
		return false, fmt.Errorf(
			"%w: round %d is outside the allowed range 1-%d",
			ErrSupervisorReview,
			round,
			MaxSupervisorReviewRounds,
		)
	}

	return true, nil
}
