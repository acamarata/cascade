// Package governor (escalation_errors.go) defines the escalation ladder's
// sentinel errors (P1-E13-W3-S27-T3).
//
// Constraints: errors only from pkg/cascade's frozen 14-kind taxonomy
// (R-14.2 / A-T7); never a bare errors.New/fmt.Errorf at this boundary.
package governor

import "github.com/acamarata/cascade/pkg/cascade"

// EscalationExhausted is returned by EscalationLadder.Advance once the
// terminal RungHuman rung has been reached: either this call's own Human
// notification failed, or an earlier call already recorded a Human-rung
// event for this entity. Either way, the ladder has nothing further it can
// do automatically — a human must resolve the entity — and returns this
// error instead of looping or silently succeeding. It reports resource
// exhaustion, the same taxonomy Kind the admission controller's own
// exhausted-budget sentinels use (ErrQueueFull, ErrThrottled).
var EscalationExhausted = cascade.New(cascade.KindQuotaExhausted, "governor: escalation ladder exhausted at the human rung")

// ErrEscalationInvalidInput is returned by Advance for a call-site error:
// an empty entity id.
var ErrEscalationInvalidInput = cascade.New(cascade.KindInvalidInput, "governor: escalation requires an entity id")

// ErrEscalationJournalUnavailable is returned when the JournalStore fails
// to read or record a rung transition. Advance returns this without having
// advanced the rung: a failed Append never lands, so the next Advance call
// still reads the same last-recorded rung.
var ErrEscalationJournalUnavailable = cascade.New(cascade.KindUnavailable, "governor: escalation journal unavailable")

// ErrEscalationRungFailed is returned when a non-terminal rung's seam call
// fails. The ladder has already advanced to the next rung by the time this
// is returned; RungHuman's failure returns EscalationExhausted instead.
var ErrEscalationRungFailed = cascade.New(cascade.KindUnavailable, "governor: escalation rung failed")
