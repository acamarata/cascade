package plugins

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Purpose (this file): the bridge's JOURNAL — where a pairing lockout and an
//   admitted-but-unroutable message are recorded, so neither is a thing that
//   happens silently on a real host.
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
// Outputs: one persisted event per lockout and per unroutable admitted
//   message.
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

// The two kinds this file publishes.
const (
	// EventKindBridgePairLockout records the attempt that burned a pairing
	// code under R-16.37's five-wrong-candidate ceiling.
	EventKindBridgePairLockout events.EventKind = "bridge.pair_lockout"
	// EventKindBridgeMessageUnrouted records a message that passed every
	// admission gate and still had nowhere to go, because no chat route is
	// registered yet (W/S-48.T2 registers the real one).
	EventKindBridgeMessageUnrouted events.EventKind = "bridge.message_unrouted"
)

// unroutedDetail is the operator-facing explanation the unrouted record
// carries. It names the ticket that closes the gap, so the record answers
// "why did my message do nothing" without anybody reading this file.
const unroutedDetail = "bridge: message admitted, no chat route registered (S-48.T2)"

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

// unroutedHandler is the telegram.Handler the composition root registers for
// BOTH transports until W/S-48.T2's chat route lands.
//
// It exists so that "admitted and then nothing happened" is a recorded fact
// rather than an invisible one: without a registered handler the module drops
// an admitted message at its dispatch site, which is indistinguishable from a
// refusal to everybody outside the process.
func (j *bridgeJournal) unroutedHandler(kind string) telegram.Handler {
	return func(ctx context.Context, msg telegram.InboundMessage) error {
		payload, err := json.Marshal(struct {
			ChatRef  string `json:"chat_ref"`
			UpdateID string `json:"update_id"`
			Handler  string `json:"handler_kind"`
			Origin   string `json:"origin"`
			Detail   string `json:"detail"`
		}{
			ChatRef: correlationOf(msg), UpdateID: strconv.FormatInt(msg.Update.UpdateID, 10),
			Handler: kind, Origin: msg.Origin, Detail: unroutedDetail,
		})
		if err != nil {
			return nil
		}
		j.publish(ctx, EventKindBridgeMessageUnrouted, payload)
		return nil
	}
}

// correlationOf reports the chat or callback the update arrived on, as a
// correlation id. It is NOT the message text and not the sender's name: an id is
// enough to line the record up against a bot's own logs. An update that is
// neither reads "unknown" rather than an empty field, so a record never looks
// like it lost a value it never had.
func correlationOf(msg telegram.InboundMessage) string {
	if msg.Update.Message != nil {
		return "chat:" + strconv.FormatInt(msg.Update.Message.Chat.ID, 10)
	}
	if msg.Update.CallbackQuery != nil {
		return "callback:" + msg.Update.CallbackQuery.ID
	}
	return "unknown"
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

// compile-time proof the journal really is the sink the plugin declares, so a
// signature change on either side fails here rather than at the wiring site.
var _ telegram.LockoutSink = (*bridgeJournal)(nil)
