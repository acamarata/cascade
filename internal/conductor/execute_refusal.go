package conductor

// Purpose: what the caller and the audit row see when Router.Select fails.
//   Only the lane-table refusal (emptiedRefusal: tier, first removed lane and
//   type, count, the guarded config key restricted_external_lanes) keeps its
//   detail; every other Select error, including the thread-privacy refusal
//   that carries a thread id, becomes the bare ErrNoLane.
// Inputs: the error Router.Select returned.
// Outputs: the error Execute and ExecuteStream audit and return.
// Constraints: fail closed either way, only the message differs. The match is
//   by Go type, never by errors.Is against the sentinel: (*cascade.Error).Is
//   compares Kind alone, so any policy_denied error would pass an Is check.
// SPORT: internal/conductor (P1-SEC-02).

import "errors"

// laneRefusal marks an ErrSensitivityViolation built by emptiedRefusal, whose
// message is limited to tier, lane name and type, count and the config key.
type laneRefusal struct{ err error }

func (r *laneRefusal) Error() string { return r.err.Error() }

// Unwrap keeps errors.Is(err, ErrSensitivityViolation) and the Kind intact.
func (r *laneRefusal) Unwrap() error { return r.err }

// selectRefusal maps a Router.Select error to the refusal the executor reports.
func selectRefusal(err error) error {
	var lr *laneRefusal
	if errors.As(err, &lr) {
		return lr // the bare marker: text added by an outer wrapper is dropped
	}
	return ErrNoLane
}
