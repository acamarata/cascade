// Purpose: the Telegram-side half of the pairing flow — answering
//   "/pair <code>" against the shared cascadepa.PairCodeStore verifier and
//   writing the binding on success. The verifier itself (constants,
//   generation, lockout state machine, durability) lives in
//   plugins/cascade-pa/pairing.go so W/S-49.T3's WhatsApp adapter binds the
//   identical semantics without importing this package.
//
// Inputs: a candidate code parsed from an inbound Message.Text by module.go's
//   isPairCommand, plus the sender/chat ids that Message carries.
//
// Outputs: on Bound, binds senderID through the BindingStore and confirms
//   with the DIGEST-DERIVED subject (never a token, never a raw operator
//   string). On Refused, replies "pairing failed". On LockedOut, replies
//   "pairing failed" and emits the typed LockoutEvent.
//
// Constraints:
//   - A BINDING IS WRITTEN ON EXACTLY ONE OUTCOME. Mismatch, expiry and
//     lockout all write nothing.
//   - A FAILED BIND IS A FAILED PAIRING. Bind now fails when the
//     paired-device record cannot be written, and this file reports that as
//     "pairing failed" rather than confirming a pairing the device registry
//     never recorded.
//   - THE SUBJECT IS NEVER ECHOED RAW. The confirmation names the subject
//     that reached this module, which assemble.go derives as a digest of the
//     bot token; there is no path here that could print a token, and none
//     that reflects operator-supplied text back into a message.
//   - EVERY REPLY CROSSES THE SAME OUTBOUND GATE (T0 D5). say() sends through
//     module.reply, not a bare BotClient — the identical R-21.203 refusal
//     gate and egress firewall reply/answer apply elsewhere in this package.
//
// SPORT: plugins/cascade-pa/telegram pairCoordinator/ADDED,
//   LockoutEvent/ADDED (P1-E23-W5-S48-T1).

package telegram

import (
	"context"
	"strings"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// replyPairingFailed is the one answer every non-bind outcome produces. It
// does not say WHY: distinguishing "wrong code" from "no code outstanding"
// would tell a stranger whether the owner is mid-pairing.
const replyPairingFailed = "pairing failed"

// replyPaired confirms a successful pairing. It names the subject, which is
// a token digest (assemble.go's SubjectFromToken), never the token.
const replyPaired = "paired: "

// isPairCommand reports whether text is a "/pair <code>" attempt and, if so,
// the candidate code.
func isPairCommand(text string) (code string, ok bool) {
	const prefix = "/pair "
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
		return "", false
	}
	return strings.TrimSpace(trimmed[len(prefix):]), true
}

// LockoutEvent is the typed event R-16.37 requires on a code's fifth wrong
// attempt.
type LockoutEvent struct {
	Subject string
	At      time.Time
}

// LockoutSink receives LockoutEvent. The host wires a real one over its own
// journal (internal/plugins/cascadepa_bridge_events.go); the unconfigured
// default drops the event rather than blocking the refusal, because losing an
// audit line must never mean admitting a pairing.
//
// It takes a context because a real sink WRITES: the host's implementation
// persists the event, and a write with no context is a write nothing can bound.
// The host is the one that decides the record must outlive a cancelled request,
// not this package.
type LockoutSink interface {
	EmitLockout(ctx context.Context, e LockoutEvent)
}

type discardLockoutSink struct{}

func (discardLockoutSink) EmitLockout(context.Context, LockoutEvent) {}

// pairCoordinator ties the shared PairCodeStore, the BindingStore and a
// LockoutSink together for one module's "/pair" handling.
type pairCoordinator struct {
	codes   *cascadepa.PairCodeStore
	binding *cascadepa.BindingStore
	sink    LockoutSink
	clock   cascadepa.PairClock
}

// newPairCoordinator constructs a coordinator. A nil sink discards events.
func newPairCoordinator(codes *cascadepa.PairCodeStore, binding *cascadepa.BindingStore,
	sink LockoutSink, clock cascadepa.PairClock) *pairCoordinator {
	if sink == nil {
		sink = discardLockoutSink{}
	}
	return &pairCoordinator{codes: codes, binding: binding, sink: sink, clock: clock}
}

// handlePairCommand verifies candidate against subject's outstanding code. It
// is the ONLY call site that may write a binding, and it does so on exactly
// one outcome: PairOutcome.Bound with a successful Bind.
//
// module (not a bare *BotClient) is what every reply routes through (D5): a
// pairing reply is an outbound bridge write like any other, so it crosses
// the SAME R-21.203 refusal gate reply/answer apply (refuse.go's
// guardOutbound), not a second path that calls the transport directly.
func (p *pairCoordinator) handlePairCommand(ctx context.Context, module *TelegramModule,
	subject string, chatID int64, senderID, candidate string) {
	outcome, err := p.codes.VerifyAndConsume(ctx, subject, candidate)
	if err != nil {
		p.say(ctx, module, chatID, replyPairingFailed)
		return
	}
	switch {
	case outcome.Bound:
		p.bind(ctx, module, subject, chatID, senderID)
	case outcome.LockedOut:
		p.sink.EmitLockout(ctx, LockoutEvent{Subject: subject, At: p.clock.Now()})
		p.say(ctx, module, chatID, replyPairingFailed)
	default:
		p.say(ctx, module, chatID, replyPairingFailed)
	}
}

// bind writes the binding and confirms it, or reports the failure honestly.
func (p *pairCoordinator) bind(ctx context.Context, module *TelegramModule, subject string, chatID int64, senderID string) {
	if _, err := p.binding.Bind(ctx, subject, senderID); err != nil {
		p.say(ctx, module, chatID, replyPairingFailed)
		return
	}
	p.say(ctx, module, chatID, replyPaired+subject)
}

// say sends one pairing-flow reply through module.reply — the SAME
// R-21.203 outbound gate (guardOutbound) and egress firewall every other
// reply in this package applies (D5). chatKind is "" here: a pairing
// reply's text is always one of the two fixed strings above, never
// credential-shaped, so no outbound quarantine event is expected on this
// path in production.
func (p *pairCoordinator) say(ctx context.Context, module *TelegramModule, chatID int64, text string) {
	module.reply(ctx, chatID, "", text)
}
