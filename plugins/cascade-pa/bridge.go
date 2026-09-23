// Purpose (this file): ChatBridge — the transport-agnostic lifecycle and
//   message-exchange contract every chat bridge adapter satisfies. The
//   Telegram module (plugins/cascade-pa/telegram) is the first and, in P1,
//   only implementation; the W/S-49.T3 WhatsApp adapter implements the
//   same interface verbatim, proven here first.
//
// Inputs: none at this package's level — a concrete adapter (telegram,
//   later whatsapp) supplies its own transport and wires itself to this
//   interface's method set.
//
// Outputs: a uniform surface a host composition root can start, stop,
//   drain, query for pairing state, and exchange messages through, without
//   knowing which chat platform is behind it.
//
// Constraints:
//   - EXACTLY THIS METHOD SET (P1-E23-W5-S48-T2's own contract): no more,
//     no fewer. A future adapter that needs a platform-specific extra
//     capability adds its own exported method beside this interface, never
//     widens it — widening this interface would force every adapter
//     (telegram today, whatsapp next) to grow the same method whether or
//     not its platform has an equivalent.
//   - InboundMessage IS NOT telegram.InboundMessage. This package's
//     InboundMessage is the platform-NEUTRAL shape Receive delivers:
//     ThreadID is already resolved, Body is already decoded text bytes.
//     telegram.InboundMessage (plugins/cascade-pa/telegram/module.go) is
//     the RAW, platform-specific decode result a transport's dispatch gate
//     hands to its own Handler before this package ever sees it — the two
//     types exist at different layers on purpose.
//   - Origin/Untrusted ARE NEVER CLEARABLE (R-21.227): every InboundMessage
//     this package's Receive delivers carries the origin it was decoded
//     under and Untrusted=true; no adapter may construct one any other
//     way (see telegram/chat.go's toInboundMessage, the ONE conversion
//     site on the Telegram side).
//
// SPORT: plugins/cascade-pa ChatBridge/ADDED, InboundMessage/ADDED,
//   RefusalRecord/ADDED (P1-E23-W5-S48-T2).

package cascadepa

import (
	"context"
	"time"
)

// The Origin values InboundMessage.Origin is restricted to (R-21.227). A
// transport-neutral consumer that persists or renders bridge content
// switches on these two, never a free-form string, so a third bridge
// adapter is a compile-time decision, not a silently accepted typo.
const (
	OriginBridgeTelegram = "bridge-telegram"
	OriginBridgeWhatsApp = "bridge-whatsapp"
)

// ChatBridge is one chat platform adapter's full lifecycle and message
// surface: start/stop/drain the transport, report pairing state, send a
// reply into an existing thread, receive admitted inbound messages, and
// report what this adapter has refused to bridge.
type ChatBridge interface {
	// Start launches the adapter's transport (a long-poll loop, a
	// websocket, ...). It returns once the transport is launched, not once
	// it stops; a platform with no daemon on this tier (R-16.60a) refuses
	// here instead of claiming a headless capability it does not have.
	Start(ctx context.Context) error
	// Stop cancels the transport and blocks until it has actually
	// stopped, or until ctx ends first.
	Stop(ctx context.Context) error
	// Drain blocks until every in-flight Send/inbound-handling call this
	// adapter is currently running has finished, or until ctx ends first.
	// Unlike Stop, Drain does not cancel anything: it is the "let what is
	// already happening finish" half of a graceful shutdown, called before
	// Stop by a host that wants in-flight work to complete rather than be
	// cut off mid-call.
	Drain(ctx context.Context) error
	// Paired reports whether this adapter's subject currently has a bound
	// counterpart (an operator's paired device). A transport error while
	// checking answers false: an adapter that cannot confirm pairing is
	// not confirmed paired.
	Paired() bool
	// Send delivers body into the chat thread this adapter has already
	// mapped to threadID. The sensitivity tier is resolved from the
	// thread's own privacy mode (never a caller-declared tier) and the
	// bridge's registered egress class enforces it before any byte
	// reaches the transport (§5.17); a thread this adapter cannot map to
	// a live chat, or a tier the class excludes, refuses instead of
	// delivering anything.
	Send(ctx context.Context, threadID string, body []byte) error
	// Receive returns a channel of every InboundMessage this adapter has
	// admitted (passed every dispatch-gate refusal the transport applies
	// before handing a message to this interface). The channel is closed
	// when the adapter stops; a full channel drops the oldest queued
	// message rather than blocking the transport's own dispatch loop.
	Receive(ctx context.Context) (<-chan InboundMessage, error)
	// RefusalReport returns every bridge-boundary refusal this adapter has
	// recorded since it started (bounded; the newest entries evict the
	// oldest once the bound is reached), for a host's own diagnostics
	// surface. It is a snapshot, not a live subscription.
	RefusalReport() []RefusalRecord
}

// InboundMessage is one admitted message, in the platform-neutral shape
// Receive delivers. See this file's header for why it is a distinct type
// from telegram.InboundMessage.
type InboundMessage struct {
	// ThreadID is the conversation thread this message resolves to.
	ThreadID string
	// SenderRef identifies the sender within the source platform (a
	// Telegram user id, a WhatsApp phone number) — a reference, never a
	// display name or any other free-text field a sender could shape.
	SenderRef string
	// Body is the message's decoded text content.
	Body []byte
	// Received is when the source platform reports the message arrived.
	Received time.Time
	// Origin names which adapter produced this message (OriginBridge*
	// above). Never empty and never any value outside that closed set.
	Origin string
	// Untrusted is always true (R-21.227): bridge content is
	// attacker-controlled and reaches every consumer as content, never as
	// instructions. No constructor in this tree sets this false.
	Untrusted bool
}

// RefusalRecord is one bridge-boundary refusal: a message or reply this
// adapter declined to forward, with enough context for an operator to see
// why, and never any content from the refused message itself.
type RefusalRecord struct {
	// ThreadID is the thread the refused operation targeted.
	ThreadID string
	// ResolvedTier is the §5.16 sensitivity tier that caused the refusal
	// (as a tier NAME: "local-only", "restricted", ...), or the tier this
	// adapter fell back to when the real tier could not be resolved
	// (§5.16's fail-closed rule: unresolvable resolves to local-only).
	ResolvedTier string
	// Reason is a short, fixed, human-readable explanation — never a
	// server-supplied or sender-supplied string.
	Reason string
	// CorrelationID lets an operator line this record up against the
	// adapter's own transport-level logs, without carrying any content.
	CorrelationID string
	// At is when the refusal happened.
	At time.Time
}
