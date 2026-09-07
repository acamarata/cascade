// Package governor (admission_errors.go) defines the admission
// controller's sentinel errors.
//
// Purpose: three taxonomy-wrapped sentinels an Admit caller distinguishes
//
//	via errors.Is: ErrQueueFull and ErrThrottled report resource
//	exhaustion (retry later, possibly with backoff), ErrDraining reports
//	a refusal because the controller is shutting down (retry against a
//	different node/controller, not this one).
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
