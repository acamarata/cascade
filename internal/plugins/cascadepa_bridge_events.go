package plugins

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Purpose (this file): the bridge's JOURNAL — where a pairing lockout, a
//   quarantined secret and an unauthorized approval attempt are recorded, so
//   none of them is a thing that happens silently on a real host. (The
//   unroutedHandler/EventKindBridgeMessageUnrouted "nothing is wired yet"
//   placeholder this file held before W/S-48.T2's chat route landed is
//   removed: NewCascadePABridgeChatHandler is the real HandlerText
//   registration now, so an admitted message always has somewhere to go.)
//
// WHY THE EVENT BUS AND NOT internal/audit (recorded, not papered over).
//   internal/audit is the tree's other candidate and the better long-term home,
//   but its Kind enum is CLOSED at the fourteen values R-21.235 ratified and
//   Append REFUSES an unknown kind — none of the fourteen names a bridge
//   event, and minting a fifteenth amends another package's ratified contract,
//   which is a ruling this ticket does not hold. internal/events is the seam
//   the tree's other subsystems record their own notable events on
//   (internal/nodes' netwatch and prober, internal/ci's runner,
//   internal/providers/health), its EventKind is documented as deliberately
//   OPEN for exactly that (internal/events/types.go), and Bus.Publish PERSISTS
//   each event through provider.Store rather than logging it — so these are
//   durable, queryable records, not log lines. A bridge audit Kind is the right
//   follow-up; it is named in this ticket's journal rather than assumed.
//
// Inputs: the daemon's live *events.Bus (as the narrow Publisher seam below).
// Outputs: one persisted event per lockout, quarantine, and unauthorized
//   approval attempt.
//
// Constraints:
//   - NO MESSAGE CONTENT IN A PAYLOAD. A bridge message is untrusted,
//     attacker-supplied text; the records carry the subject, the update id and
//     the handler kind, which is what an operator needs to correlate, and
//     nothing a sender chose.
//   - THE RECORD SURVIVES THE REQUEST. Both emitters publish under
//     context.WithoutCancel: a lockout that happened must not go unrecorded
//     because the daemon was shutting down while it was being refused.
//   - A PUBLISH FAILURE NEVER CHANGES A DECISION. Losing a journal line must
//     not mean admitting a pairing or forwarding a message, so the error is
//     returned to the caller's own discretion and never gates the refusal.
//
// SPORT: internal/plugins:cascadepa-bridge-events (ADD) — P1-E23-W5-S48-T1.

// bridgeEventNamespace is the bus namespace these records live in.
const bridgeEventNamespace = "bridge"

// bridgeEventSource identifies the publisher on the bus.
const bridgeEventSource = "cascade-pa/telegram"

// The three kinds this file publishes.
const (
	// EventKindBridgePairLockout records the attempt that burned a pairing
	// code under R-16.37's five-wrong-candidate ceiling.
	EventKindBridgePairLockout events.EventKind = "bridge.pair_lockout"
	// EventKindBridgeSecretQuarantined records a R-21.105 quarantine: a
	// secret-shaped bridge message, or an outbound write attempt, refused
	// by refuse.go's gate (P1-E23-W5-S48-T3). Its value is
	// telegram.QuarantineKind itself — the ONE contract value
	// quarantine.go declares — never a second, independently-typed
	// literal that could drift from it (confirming CR, 2026-09-21: an
	// earlier draft published "bridge.secret_quarantined", an underscore
	// variant of the dotted "bridge.secret.quarantined" the ticket's AC
	// and every doc name).
	EventKindBridgeSecretQuarantined events.EventKind = telegram.QuarantineKind
	// EventKindBridgeApprovalUnauthorized records R-21.210's STEP 0
	// rejection: a callback_query.from.id that does not match the
	// current paired-device binding (P1-E23-W5-S48-T4). Its value is
	// telegram.ApprovalUnauthorizedKind itself, for the identical reason
	// EventKindBridgeSecretQuarantined above takes telegram.QuarantineKind.
	EventKindBridgeApprovalUnauthorized events.EventKind = telegram.ApprovalUnauthorizedKind
)

// BridgeEventPublisher is the journal seam. *events.Bus satisfies it; it is
// declared narrowly here so the bridge's recorders can be driven by a test
// double without a provider.Store behind them.
type BridgeEventPublisher interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind,
		source string, payload []byte) (events.Event, error)
}

// bridgeJournal records the bridge's security-relevant events on the bus.
type bridgeJournal struct {
	bus BridgeEventPublisher
}

// newBridgeJournal builds a recorder over bus. A nil bus is refused by the
// CALLER (NewCascadePABridge), not tolerated here: a bridge whose lockouts go
// nowhere is the defect this file closes, so there is no discarding default in
// production.
func newBridgeJournal(bus BridgeEventPublisher) *bridgeJournal {
	return &bridgeJournal{bus: bus}
}

// EmitLockout implements telegram.LockoutSink over the bus.
func (j *bridgeJournal) EmitLockout(ctx context.Context, e telegram.LockoutEvent) {
	payload, err := json.Marshal(struct {
		Subject string `json:"subject"`
		At      string `json:"at"`
		Reason  string `json:"reason"`
	}{Subject: e.Subject, At: e.At.UTC().Format("2006-01-02T15:04:05.000Z"),
		Reason: "five wrong pairing candidates; the outstanding code was burned"})
	if err != nil {
		return
	}
	j.publish(ctx, EventKindBridgePairLockout, payload)
}

// publish writes one record. WithoutCancel, so a shutdown mid-refusal still
// records what happened; the error is deliberately not propagated into a
// dispatch decision (see this file's header).
func (j *bridgeJournal) publish(ctx context.Context, kind events.EventKind, payload []byte) {
	if j == nil || j.bus == nil {
		return
	}
	_, _ = j.bus.Publish(context.WithoutCancel(ctx), bridgeEventNamespace, kind, bridgeEventSource, payload)
}

// marshalQuarantineEvent is json.Marshal, indirected so a test can drive
// EmitQuarantine's error branch: QuarantineEvent's real fields are plain
// strings and a time.Time, so a real populated value can never fail to
// encode, and that branch would otherwise be permanently dead code.
var marshalQuarantineEvent = json.Marshal

// EmitQuarantine implements telegram.QuarantineSink over the bus: it maps
// telegram.QuarantineKind onto the real, typed EventKind above (the same
// translation EmitLockout already performs for LockoutEvent) and
// republishes QuarantineEvent's own already-safe fields verbatim — this
// adds no field TestQuarantinePayloadHasNoCredentialRef has not already
// checked (P1-E23-W5-S48-T3).
func (j *bridgeJournal) EmitQuarantine(ctx context.Context, e telegram.QuarantineEvent) {
	payload, err := marshalQuarantineEvent(e)
	if err != nil {
		return
	}
	j.publish(ctx, EventKindBridgeSecretQuarantined, payload)
}

// marshalApprovalUnauthorizedEvent is json.Marshal, indirected for the same
// dead-branch reason marshalQuarantineEvent is: ApprovalUnauthorizedEvent's
// real fields are plain strings and a time.Time, so a real populated value
// can never fail to encode.
var marshalApprovalUnauthorizedEvent = json.Marshal

// EmitApprovalUnauthorized implements telegram.ApprovalEventSink over the
// bus: it maps telegram.ApprovalUnauthorizedKind onto the real, typed
// EventKind above and republishes the event's own already-safe fields
// verbatim (P1-E23-W5-S48-T4).
func (j *bridgeJournal) EmitApprovalUnauthorized(ctx context.Context, e telegram.ApprovalUnauthorizedEvent) {
	payload, err := marshalApprovalUnauthorizedEvent(e)
	if err != nil {
		return
	}
	j.publish(ctx, EventKindBridgeApprovalUnauthorized, payload)
}

// compile-time proof the journal really is the sink the plugin declares, so a
// signature change on either side fails here rather than at the wiring site.
var (
	_ telegram.LockoutSink       = (*bridgeJournal)(nil)
	_ telegram.QuarantineSink    = (*bridgeJournal)(nil)
	_ telegram.ApprovalEventSink = (*bridgeJournal)(nil)
)
