// Purpose: the spill advance path -- what happens when the lane a caller
//
//	was just using comes back 429/exhausted. Advance marks that lane's
//	rate-limit window, emits one bus event per advance (a deterministic,
//	replayable record of the demotion), and asks QuotaPolicy.NextLane to
//	order the remaining candidates. A missing/unverifiable
//	[conductor.quota] section (ParseQuotaConfig's divergent return) is
//	reported the same way, once, at construction.
//
// Inputs: Advance takes the lane that just failed, the caller's exclusion
//
//	set, and a reason string for the event payload.
//
// Outputs: the next LaneID to try, or ErrAllLanesExhausted.
// Constraints: every event Timestamp comes from the Bus's own injected
//
//	Clock (internal/events.New's contract) -- this file never reads the
//	wall clock. Fail-closed: Advance never returns a lane it has not
//	verified is unexcluded and unwindowed; on exhaustion it returns the
//	typed error, never a zero-value LaneID treated as valid by a caller
//	that forgets to check err.
//
// SPORT: provider · J · S-21 · T-1 · quota/spill routing policy
//
//	(P1-E10-W3-S21-T1).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
)

// QuotaEventNamespace is the internal/events.Bus namespace every
// QuotaPolicy spill/divergence event publishes to.
const QuotaEventNamespace = "conductor.quota"

// The three EventKinds this ticket emits (internal/events.EventKind is a
// deliberately open vocabulary -- see events/types.go's doc comment --
// so these are minted here, not added to a closed enum elsewhere).
const (
	// EventKindSpillAdvance is published each time Advance demotes the
	// current lane and moves to the next one in spill_order.
	EventKindSpillAdvance events.EventKind = "quota.spill.advance"
	// EventKindSpillExhausted is published when Advance finds no
	// remaining candidate lane.
	EventKindSpillExhausted events.EventKind = "quota.spill.exhausted"
	// EventKindQuotaDivergence is published once, at policy construction,
	// when [conductor.quota] was missing or unverifiable and the policy
	// fell back to the single-lane default rather than downgrading
	// silently.
	EventKindQuotaDivergence events.EventKind = "quota.divergence"
)

// spillAdvancePayload is EventKindSpillAdvance's JSON payload.
type spillAdvancePayload struct {
	FromLane LaneID `json:"from_lane"`
	ToLane   LaneID `json:"to_lane"`
	Reason   string `json:"reason"`
}

// spillExhaustedPayload is EventKindSpillExhausted's JSON payload.
type spillExhaustedPayload struct {
	FromLane LaneID   `json:"from_lane"`
	Excluded []LaneID `json:"excluded"`
	Reason   string   `json:"reason"`
}

// EventPublisher is the minimal internal/events.Bus surface Advance and
// PublishDivergence need. Declared locally (rather than depending on
// *events.Bus directly) so a test can substitute a recording fake without
// standing up a real Store-backed Bus.
type EventPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// Advance marks current as rate-limited for the policy's demotion window,
// publishes EventKindSpillAdvance (or EventKindSpillExhausted, if no
// candidate remains), and returns the next lane NextLane selects over the
// same excluded set plus current. reason is free text describing why
// (e.g. "429", "pool_exhausted") and is carried verbatim in the event
// payload for operator debugging -- never a credential or any part of
// one.
func (p *QuotaPolicy) Advance(ctx context.Context, bus EventPublisher, source string, current LaneID, excluded []LaneID, reason string) (LaneID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.markLimited(current)

	nextExcluded := append(append([]LaneID{}, excluded...), current)
	next, err := p.NextLane(ctx, nextExcluded)
	if err != nil {
		return p.publishExhausted(ctx, bus, source, current, nextExcluded, reason)
	}
	return p.publishAdvance(ctx, bus, source, current, next, reason)
}

// markLimited stamps current's rate-limit window using the policy's
// injected clock.
func (p *QuotaPolicy) markLimited(current LaneID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limitedUntil[current] = p.clock.Now().Add(p.window)
}

// publishAdvance emits EventKindSpillAdvance and returns (next, nil).
func (p *QuotaPolicy) publishAdvance(ctx context.Context, bus EventPublisher, source string, from, to LaneID, reason string) (LaneID, error) {
	payload, err := json.Marshal(spillAdvancePayload{FromLane: from, ToLane: to, Reason: reason})
	if err != nil {
		return "", err
	}
	if _, err := bus.Publish(ctx, QuotaEventNamespace, EventKindSpillAdvance, source, payload); err != nil {
		return "", err
	}
	return to, nil
}

// publishExhausted emits EventKindSpillExhausted and returns the typed
// exhaustion error.
func (p *QuotaPolicy) publishExhausted(ctx context.Context, bus EventPublisher, source string, from LaneID, excluded []LaneID, reason string) (LaneID, error) {
	payload, err := json.Marshal(spillExhaustedPayload{FromLane: from, Excluded: excluded, Reason: reason})
	if err != nil {
		return "", err
	}
	if _, pubErr := bus.Publish(ctx, QuotaEventNamespace, EventKindSpillExhausted, source, payload); pubErr != nil {
		return "", pubErr
	}
	return "", ErrAllLanesExhausted
}

// PublishDivergence emits EventKindQuotaDivergence: [conductor.quota] was
// missing or failed to resolve against the registry, and the policy is
// running on the fail-closed single-lane default rather than downgrading
// silently. Callers construct a QuotaPolicy via NewQuotaPolicy and call
// this once, immediately after, when ParseQuotaConfig's divergent return
// was true.
func PublishDivergence(ctx context.Context, bus EventPublisher, source, detail string) error {
	payload, err := json.Marshal(struct {
		Detail string `json:"detail"`
	}{Detail: detail})
	if err != nil {
		return err
	}
	_, err = bus.Publish(ctx, QuotaEventNamespace, EventKindQuotaDivergence, source, payload)
	return err
}
