// Package governor (throttle_types.go) defines the throttle ladder's
// event and pressure-source vocabulary.
//
// Purpose: ThrottleEvent (one stage transition, delivered to every
//
//	Subscribe channel) and PressureSource (the single-value interface the
//	ladder polls; *AdmissionController satisfies it via Pressure(),
//	R-21.215). ThrottleStage itself is defined in admission_types.go
//	(S-26.T2) so the two packages share one enum with no import cycle.
//
// Inputs: none directly; these types are populated by throttle.go's
//
//	evaluator.
//
// Outputs: ThrottleEvent values flow out of Subscribe's channel in the
//
//	order they occur; PressureSource.Pressure is read, never written,
//	by the ladder.
//
// Constraints: ThrottleEvent is value-typed so a subscriber can never
//
//	mutate another subscriber's copy of the same transition.
//
// SPORT: internal/fleet/governor.ThrottleLadder (ADD, per T-3
//
//	sport_updates).
package governor

import "time"

// PressureSource reports a single pressure value in [0, 1], the throttle
// ladder's only input (R-21.215). *AdmissionController satisfies this via
// its Pressure() method; the ladder defines no pressure metric of its
// own and reads no other signal.
type PressureSource interface {
	// Pressure returns the current pressure reading. Implementations
	// must never block or perform I/O: the ladder calls this once per
	// tick from its own goroutine.
	Pressure() float64
}

// ThrottleEvent is one throttle-stage transition, delivered to every
// subscriber registered via Subscribe at the moment the ladder's stage
// actually changes. No event is published for a tick that leaves the
// stage unchanged.
type ThrottleEvent struct {
	// Stage is the stage the ladder transitioned to.
	Stage ThrottleStage
	// PreviousStage is the stage the ladder transitioned from. Always
	// different from Stage: a same-stage tick publishes no event.
	PreviousStage ThrottleStage
	// Pressure is the PressureSource reading that caused this
	// transition, sampled at the same tick that produced Stage.
	Pressure float64
	// At is when this transition was evaluated, per the ladder's
	// injected runtime.Clock (never a bare time.Now, Art.7.3).
	At time.Time
}
