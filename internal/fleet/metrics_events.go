// Purpose: how Metrics learns that a task needed a human — the
//
//	consumer loop over the attention queue's own event stream.
//
// WHY THE ATTENTION QUEUE IS THE ONE SOURCE. The contract names three:
//
//	attention-queue promotions (S-39.T1), auto-advance resolutions
//	(S-39.T2), and PEWS task-lifecycle interrupt-phase transitions
//	(N/S-30.T1). Verified against the tree:
//
//	  - attention promotions DO publish: fleet.attention.changed, and
//	    that is this loop;
//	  - auto-advance resolutions publish NOTHING to the bus. The one
//	    production sink that sees every verdict is
//	    supervision.AutoAdvanceRecorder, which already has a production
//	    caller, so the count is taken there by direct call rather than
//	    through a bus event invented for the purpose;
//	  - the PEWS lifecycle has NO interrupt phase. S-30.T1 built a closed
//	    six-state machine over a closed five-event set
//	    (claim/step/cr/qa/done); adding a sixth event to a ratified closed
//	    enum is a change to THAT contract, not this one. The contract's own
//	    next sentence names the attention queue as "the authoritative
//	    source of mid-flight task interruptions visible to fleet
//	    supervision" — so there is one source, and unifying it with a
//	    second that does not exist is not work this ticket can do.
//
// WHAT COUNTS AS AN INTERRUPTION. A NEW, unacknowledged item. The topic
//
//	carries every change, acknowledgements included, and counting those
//	would double every interruption the moment a human dealt with it. New
//	items are recognised by id, and ids already counted are ignored, so a
//	redelivered event — the bus replays from a durable cursor — cannot
//	inflate the number either.
//
// Constraints: the loop is bounded by ctx and by the subscription's own
//
//	channel closing. It never blocks on anything but those two, and a
//	fatal subscription error ends it rather than spinning.
//
// SPORT: fleet.Metrics/ADDED (P1-E18-W4-S40-T3).

package fleet

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
)

// AttentionNamespace is the event namespace attention promotions are
// published on. Declared here rather than imported so this package does
// not depend on an unexported constant, and asserted against the real
// publisher in the tests.
const AttentionNamespace = "fleet.attention"

// metricsCursor is the durable subscription cursor name this consumer
// commits under. Distinct from every other subscriber's, so two
// subscribers never race one cursor (Bus.Subscribe refuses that outright).
const metricsCursor = "fleet-metrics"

// maxCountedIDs bounds the already-counted id set, on the same reasoning
// as maxTrackedTasks: the set exists to stop a redelivery double-counting,
// and a redelivery arrives near its original, so an old id can be
// forgotten without risk.
const maxCountedIDs = 4096

// counted tracks attention item ids already counted, bounded and
// oldest-first like perTask.
type counted struct {
	seen  map[string]bool
	order []string
}

// add records id and reports whether it was new.
func (c *counted) add(id string) bool {
	if c.seen == nil {
		c.seen = make(map[string]bool)
	}
	if c.seen[id] {
		return false
	}
	for len(c.seen) >= maxCountedIDs && len(c.order) > 0 {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
	c.seen[id] = true
	c.order = append(c.order, id)
	return true
}

// ConsumeAttention counts every new attention promotion delivered on sub
// until ctx ends or the subscription closes.
//
// It returns the subscription's fatal error if it had one, and nil on a
// clean stop. A caller runs this in its own goroutine; it is the only
// writer to the per-task map besides a direct RecordInterruption call, and
// both are guarded.
func (m *Metrics) ConsumeAttention(ctx context.Context, sub *events.Subscription) error {
	if sub == nil {
		return nil
	}
	seen := &counted{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-sub.Errs:
			return err
		case ev, open := <-sub.Events:
			if !open {
				return nil
			}
			m.countAttentionEvent(ev, seen)
		}
	}
}

// countAttentionEvent counts one delivered event if it is a new promotion.
//
// A payload this cannot parse is IGNORED rather than counted or fatal: a
// metric must not take a daemon down, and guessing that an unreadable
// event was an interruption would invent a number.
func (m *Metrics) countAttentionEvent(ev events.Event, seen *counted) {
	if ev.Kind != supervision.ChangedKind {
		return
	}
	var item supervision.AttentionItem
	if err := json.Unmarshal(ev.Payload, &item); err != nil {
		return
	}
	if item.ID == "" || item.Acked() {
		return
	}
	if !seen.add(item.ID) {
		return
	}
	m.RecordInterruption(item.SourceRef)
}

// RecordAutoAdvance is the seam supervision's recorder calls, so the
// auto-advance count comes from the ONE sink that already sees every
// verdict rather than from a bus event invented for this metric.
//
// Only an auto-APPROVAL counts. A refusal is the opposite of the thing
// being measured — "resolved without user interruption" — and a refusal is
// itself what produces an interruption, which the attention path counts.
func (m *Metrics) RecordAutoAdvance(decision supervision.PolicyDecision, verdict supervision.Verdict) {
	if verdict != supervision.VerdictAutoApproved {
		return
	}
	m.RecordAutoResolved(decision.Level)
}
