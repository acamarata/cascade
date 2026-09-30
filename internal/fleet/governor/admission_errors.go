// Package governor (admission_errors.go) defines the admission
// controller's sentinel errors.
//
// Purpose: three taxonomy-wrapped sentinels an Admit caller distinguishes
//
//	via errors.Is: ErrQueueFull and ErrThrottled report resource
//	exhaustion (retry later, possibly with backoff), ErrDraining reports
//	a refusal because the controller is shutting down (retry against a
//	different node/controller, not this one). IsDraining and
//	IsSignalRefusal tell ErrDraining apart from the signal refusals,
//	which share its Kind, by sentinel identity.
//
// Constraints: errors only from pkg/cascade's frozen 14-kind taxonomy
//
//	(R-14.2); never a bare errors.New/fmt.Errorf at this boundary.
package governor

import "github.com/acamarata/cascade/pkg/cascade"

// ErrQueueFull is returned by Admit when the bounded priority queue is
// already at AdmissionConfig.QueueCap capacity. It reports resource
// exhaustion: the caller may retry later.
var ErrQueueFull = cascade.New(cascade.KindQuotaExhausted, "governor: admission queue is full")

// ErrDraining is returned by Admit once Drain has closed the admission
// gate, and delivered to every waiter still queued when Drain was called.
// It reports a refusal distinct from resource exhaustion: this controller
// is shutting down and will not admit anything else.
var ErrDraining = cascade.New(cascade.KindUnavailable, "governor: admission controller is draining")

// ErrThrottled is returned by Admit when the installed StageProvider
// reports StageHalt. Nothing is queued when this is returned: it reports
// resource exhaustion at the throttle-ladder's most severe stage, just as
// ErrQueueFull does at the raw queue-capacity stage.
var ErrThrottled = cascade.New(cascade.KindQuotaExhausted, "governor: admission halted by throttle stage")

// IsDraining reports whether err's chain carries ErrDraining itself. It
// compares identity, not Kind: errors.Is on a cascade error matches any
// error of the same Kind, and ErrNoResourceSignal and
// ErrStaleResourceSignal share ErrDraining's KindUnavailable.
func IsDraining(err error) bool {
	return chainHas(err, ErrDraining)
}

// IsSignalRefusal reports whether err's chain carries ErrNoResourceSignal
// or ErrStaleResourceSignal itself: a refusal for a missing or stale
// resource signal, which clears once the sampler reports again. It
// compares identity, not Kind (see IsDraining).
func IsSignalRefusal(err error) bool {
	return chainHas(err, ErrNoResourceSignal, ErrStaleResourceSignal)
}

// chainHas walks err's unwrap chain, including multi-error branches, and
// reports whether any link is one of targets by identity.
func chainHas(err error, targets ...*cascade.Error) bool {
	if err == nil {
		return false
	}
	for _, t := range targets {
		if err == error(t) {
			return true
		}
	}
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if chainHas(e, targets...) {
				return true
			}
		}
		return false
	case interface{ Unwrap() error }:
		return chainHas(u.Unwrap(), targets...)
	}
	return false
}
