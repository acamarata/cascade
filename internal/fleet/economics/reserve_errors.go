// Purpose: the reservation subsystem's sentinel errors, each wrapping
//
//	exactly one frozen pkg/cascade.Kind (06 Sec2 -- no new taxonomy kind
//	is defined here). Split from reserve_types.go to keep it under the
//	300-line cap.
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import "github.com/acamarata/cascade/pkg/cascade"

// Sentinel errors, each wrapping exactly one frozen pkg/cascade.Kind.
var (
	// ErrUnknownReservationState is returned by ParseReservationState for
	// any input outside the five declared members.
	ErrUnknownReservationState = cascade.New(cascade.KindInvalidInput, "economics: unknown reservation state")
	// ErrUnknownReservationKind is returned by ParseReservationKind for
	// any input outside interactive|batch.
	ErrUnknownReservationKind = cascade.New(cascade.KindInvalidInput, "economics: unknown reservation kind")
	// ErrUnknownTaskClass is returned by TokensOutDefault outside the
	// frozen nine-class enum (R-21.50).
	ErrUnknownTaskClass = cascade.New(cascade.KindInvalidInput, "economics: unknown task class")
	// ErrReservationTransition is returned for any state move outside
	// the legal transition table.
	ErrReservationTransition = cascade.New(cascade.KindConflict, "economics: illegal reservation state transition")
	// ErrQuotaUnavailable is returned when a dimension's derived
	// availability cannot absorb the requested estimate.
	ErrQuotaUnavailable = cascade.New(cascade.KindUnavailable, "economics: quota unavailable for reservation")
	// ErrReservationRollback wraps every error a rollback attempt
	// produced, joined via errors.Join, so a caller can still test
	// errors.Is(err, ErrReservationRollback) after a partial-failure
	// rollback.
	ErrReservationRollback = cascade.New(cascade.KindInternal, "economics: reservation rollback reported one or more errors")
)
