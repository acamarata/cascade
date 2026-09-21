// Purpose: AutoThreader.Reassign, the misfile-correction path: move a turn
//   to a different topic's thread, record it as a fresh exemplar for that
//   topic, and emit a typed audit event naming the correction.
// Inputs: AutoThreader's own ThreadStore, ExemplarStore, publisher and
//   Clock (auto_thread.go); a threadID/turnID/turn plus the turn's current
//   and target TopicType per call.
// Outputs: nil on a completed reassignment; a typed error on caller
//   misuse, a no-op reassignment, or a propagated ThreadStore/ExemplarStore/
//   publisher failure.
// Constraints:
//
//   REASSIGN IS AN AutoThreader METHOD, not a second service with its own
//   constructor. It operates the same ThreadStore against the same
//   taxonomy Route does, and a separate Reassigner type would have meant a
//   parallel object graph over identical dependencies. Two things the
//   contract's literal "Reassign(ctx, threadID, turnID, newTopicType)
//   error" signature cannot do, both recorded for the owner rather than
//   resolved silently: (a) know the turn's CURRENT topic, which the
//   acceptance criterion's "typed error when threadID and newTopicType
//   already match the turn's current placement" must compare against, and
//   (b) supply ExemplarStore.Add's own required Turn parameter for the
//   exemplar it must add. Both arrive as added parameters here; the no-op
//   check compares newTopicType to currentTopicType directly, since
//   ThreadStore (thread_store.go) has no per-turn lookup method to ask.
//
//   AUDIT EVENT MECHANISM. "emits a typed misfile audit event to the
//   audit domain" cannot mean internal/audit.Writer.Append: audit.Kind
//   is a CLOSED 14-member enum ratified by T0 ruling R-21.235, and none
//   of the fourteen (policy.*, approval.*, config.reload, elevation.*,
//   secrets.*, vault.access) names a topic-classification correction -
//   forcing one on would misrepresent that Kind's stream for every
//   other reader of it. internal/fleet/supervision/autoadvance_audit.go
//   sets the tree's own precedent for this exact situation (reusing the
//   closest ratified Kind, policy.route, because its event genuinely
//   IS a routing decision "inside the router") but no fourteenth Kind
//   is a comparable fit here. The real, designed-for-this seam is
//   internal/events.EventKind, which internal/events/types.go
//   documents as deliberately OPEN specifically so independently-owned
//   future producers can "mint their own EventKind values... with no
//   need for an amendment" - the exact shape internal/audit's own
//   EventKindRecorded already uses to publish ITS typed notification
//   onto the same bus. EventKindMisfileReassigned below is minted the
//   same way and published into the audit domain's own namespace
//   (storage.DomainAudit), never the closed audit.Kind vocabulary. An
//   owner ruling on whether a fourteenth audit.Kind should exist instead
//   is filed rather than assumed.
// SPORT: internal/conversation/topics reassign (ADD) (P1-E21-W5-S45-T3).

package topics

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EventKindMisfileReassigned is the open-vocabulary events.EventKind this
// file publishes on a completed reassignment. See this file's header,
// AUDIT EVENT MECHANISM, for why this is not an internal/audit.Kind member.
const EventKindMisfileReassigned events.EventKind = "topics.misfile_reassigned"

// misfileEventSource identifies this package as the bus publisher, the
// same role internal/audit's own busSource constant plays for its events.
const misfileEventSource = "internal/conversation/topics"

// misfileNamespace is the events.Bus namespace a misfile event is
// published into: the audit domain, per this ticket's own "to the audit
// domain" requirement.
const misfileNamespace = string(storage.DomainAudit)

// MisfileEventPublisher is the injection seam Reassign uses to publish a
// misfile correction. Its single method's shape matches
// internal/events.Bus.Publish exactly, so a real *events.Bus satisfies it
// with zero adapter code - the same zero-adapter posture
// internal/conversation/domain.go documents for its own local Clock.
type MisfileEventPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// MisfileEvent is EventKindMisfileReassigned's JSON payload: which turn
// moved, from which topic to which, and when. Carries no turn content:
// TurnID is an opaque content address (NewTopicTurnID) and the topic types
// are caller-configured labels, so an event on the bus never widens the
// exposure of conversation text.
type MisfileEvent struct {
	ThreadID      ThreadID  `json:"thread_id"`
	TurnID        TurnID    `json:"turn_id"`
	FromTopicType TopicType `json:"from_topic_type"`
	ToTopicType   TopicType `json:"to_topic_type"`
	ReassignedAt  int64     `json:"reassigned_at"` // unix seconds, injected Clock
}

// errReassignNoOp reports that a reassignment changes nothing: newType
// already matches turnID's current topic in threadID. cascade.KindConflict
// is the same Kind internal/audit.ErrAlreadyRecorded uses for "the state
// you asked to create already exists" - a no-op reassignment is that same
// shape of refusal.
func errReassignNoOp(threadID ThreadID, turnID TurnID, newType TopicType) error {
	return cascade.Newf(cascade.KindConflict,
		"topics: reassign: turn %q in thread %q is already topic %q", turnID, threadID, newType)
}

// Reassign moves turnID (currently in threadID, classified as
// currentTopicType) to the thread newTopicType selects, records turn as a
// fresh exemplar for newTopicType, and publishes a MisfileEvent. Returns
// errReassignNoOp without touching the store, the exemplar set, or the bus
// when newTopicType already equals currentTopicType. Every dependency
// failure is returned unmodified and stops the sequence before its later
// steps run: a failed MoveTurn never adds an exemplar or publishes an
// event for a move that did not happen, and a failed Add never publishes
// an event that overstates what was recorded.
func (a *AutoThreader) Reassign(
	ctx context.Context, threadID ThreadID, turnID TurnID, turn Turn, currentTopicType, newTopicType TopicType,
) error {
	if ctx == nil {
		return cascade.New(cascade.KindInvalidInput, "topics: Reassign: ctx must not be nil")
	}
	if err := validateTopicType(newTopicType); err != nil {
		return err
	}
	if newTopicType == currentTopicType {
		return errReassignNoOp(threadID, turnID, newTopicType)
	}
	if err := a.store.MoveTurn(ctx, threadID, turnID, newTopicType); err != nil {
		return err
	}
	if err := a.exemplars.Add(ctx, newTopicType, turn); err != nil {
		return err
	}
	return a.publishMisfile(ctx, threadID, turnID, currentTopicType, newTopicType)
}

// publishMisfile JSON-encodes and sends the MisfileEvent for a completed
// reassignment, split out of Reassign to keep it under the function-length
// cap (Art.10.3).
func (a *AutoThreader) publishMisfile(
	ctx context.Context, threadID ThreadID, turnID TurnID, from, to TopicType,
) error {
	payload, err := json.Marshal(MisfileEvent{
		ThreadID: threadID, TurnID: turnID,
		FromTopicType: from, ToTopicType: to,
		ReassignedAt: a.clock.Now().Unix(),
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "topics: reassign: encoding misfile event")
	}
	_, err = a.publisher.Publish(ctx, misfileNamespace, EventKindMisfileReassigned, misfileEventSource, payload)
	return err
}
