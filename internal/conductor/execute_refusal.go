package conductor

// Purpose: what the caller and the audit row see when Router.Select fails.
//   Only the lane-table refusal (emptiedRefusal: tier, first removed lane and
//   type, count, the guarded config key restricted_external_lanes) keeps its
//   detail; every other Select error, including the thread-privacy refusal
//   that carries a thread id, becomes the bare ErrNoLane. A quota refusal
//   (KindQuotaExhausted) keeps the cause naming the lanes at their ceiling
//   (P1-CAP-07): it is still ErrNoLane by identity.
// Inputs: the error Router.Select returned.
// Outputs: the error Execute and ExecuteStream audit and return.
// Constraints: fail closed either way, only the message differs. The match is
//   by Go type, never by errors.Is against the sentinel: (*cascade.Error).Is
//   compares Kind alone, so any policy_denied error would pass an Is check.
// SPORT: internal/conductor (P1-SEC-02).

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
	if cascade.HasKind(err, cascade.KindQuotaExhausted) {
		return cascade.Wrap(cascade.KindQuotaExhausted, &quotaNoLane{cause: err}, "")
	}
	return ErrNoLane
}

// quotaNoLane is a Select refusal of a lane set the quota rules emptied: it
// is ErrNoLane and the quota sentinel Select returned, both by identity, and
// its message names the lanes at their ceiling. It wraps the cause of the
// one Select call the door made; Select is never asked again.
type quotaNoLane struct{ cause error }

func (q *quotaNoLane) Error() string   { return ErrNoLane.Msg + ": " + q.cause.Error() }
func (q *quotaNoLane) Unwrap() []error { return []error{ErrNoLane, q.cause} }
