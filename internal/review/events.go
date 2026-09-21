// Purpose: the reviewer's event seam (P1-E25-W5-S52-T4, CR fix D5): the
//   AC "the same-family fallback is used and LOGGED, never silent" needs a
//   destination, and internal/review is a library that must not choose a
//   logger for its host. EventPublisher is the injected seam, exactly
//   the stance internal/context's StageEventPublisher takes ("a nil
//   publisher is a supported configuration that drops every event"); the
//   PRODUCTION publisher is wired in internal/plugins/review_wiring.go.
// Inputs: an EventPublisher (nil allowed) supplied to NewProvider /
//   Review / CRC.
// Outputs: one FallbackEvent per same-family fallback, naming the
//   level, the consequence class, the observed families and the reason.
// Constraints: publishing must never change what Review returns, and a nil
//   publisher must never panic -- publishFallback is the only call site and
//   nil-checks first. The event's fields are code-chosen strings only: no
//   artifact content, no model output, no author identity ever appears here
//   (R-21.156 blindness applies to the telemetry too).
// SPORT: internal/review.events/ADD (P1-E25-W5-S52-T4).

package review

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// EventPublisher is the seam the composition root injects so the
// reviewer's fallback decision reaches the host's real log or event bus. A
// nil publisher is supported and drops every event.
type EventPublisher interface {
	// Publish delivers one event. Implementations must not block for long
	// and must not return an error: whether anyone is listening may never
	// change what Review returns.
	Publish(ctx context.Context, event Event)
}

// Event is one published reviewer event. Exactly one field is
// non-nil, matching internal/context.StageEvent's shape so a subscriber
// switches on presence rather than on a type assertion.
type Event struct {
	// Fallback is set when a review proceeded on a same-family fallback.
	Fallback *FallbackEvent
}

// FallbackEvent records the R-21.156(b) narrowed fallback actually
// being taken: the review proceeded although no distinct reviewer family
// was eligible, on a consequence class low enough that this remains legal.
type FallbackEvent struct {
	// Level is the CR level that fell back.
	Level provider.ReviewCRLevel
	// ConsequenceClass is the class under which the fallback was legal.
	ConsequenceClass ConsequenceClass
	// Families are the distinct provider families the registry offered at
	// dispatch time -- the fact that made the fallback necessary.
	Families []string
	// Reason is a fixed, code-chosen explanation string.
	Reason string
}

// fallbackReasonSingleFamily is the only reason a fallback is taken today:
// the registry offered fewer than two distinct families.
const fallbackReasonSingleFamily = "fewer than two distinct provider families are registered, " +
	"and this consequence class permits the same-family fallback (R-21.156(b))"

// publishFallback emits the fallback event, or does nothing at all when no
// publisher was injected.
func publishFallback(ctx context.Context, events EventPublisher, ev FallbackEvent) {
	if events == nil {
		return
	}
	events.Publish(ctx, Event{Fallback: &ev})
}
