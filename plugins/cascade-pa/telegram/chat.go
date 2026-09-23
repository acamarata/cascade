// Purpose (this file): TelegramBridge — the cascadepa.ChatBridge over
//   *TelegramModule, and the chat-parity handler it registers as
//   HandlerText: resolve the thread's §5.16 privacy mode, refuse
//   local-only/restricted/unresolvable before any egress, and for
//   internal/public forward through chat.append_turn (S-43.T2), the reply
//   crossing the SAME registered egress class (§5.17) SendMessage enforces.
//
// Inputs: a *TelegramModule already carrying the S-48.T1/T3 dispatch gates
//   (pairing, elevated-verb, secret-scan ran BEFORE this handler, per
//   module.go's admitMessage), a *cascadepa.BindingStore for Paired(), and
//   the three host-mediated seams egress.go declares.
//
// Outputs: chat.append_turn calls, sendMessage replies (a refusal, or the
//   honest "recorded, no reply generator yet" disclosure — replyText),
//   bridge.refused divergence records, and the ChatBridge surface a host
//   composition root drives.
//
// Constraints:
//   - THE REAL ENGINE DECIDES, NOT A TIER SWITCH HERE. handleInbound
//     probes b.module.client's own EgressGate with the resolved tier and
//     nil content — the same "capability/tier-only probe" shape
//     BotClient.fetchOne already uses (client.go) — so ALLOW/REFUSE comes
//     from the real, already-registered class, never a re-derived
//     local-only/restricted comparison (LANE-RULES §5).
//   - SAME PACKAGE, REAL HELPERS. b.module.reply/guardOutbound/now/
//     client/egress are unexported TelegramModule/BotClient members this
//     file reaches because it is package telegram, not a reimplementation.
//     A refusal reply reuses reply() (fixed TierInternal, matching every
//     other module refusal); an ALLOW reply needs the THREAD's resolved
//     tier, so it calls guardOutbound then client.SendMessage directly.
//   - NO ASSISTANT REPLY EXISTS. replyText mirrors errReplyGenerationUnavailable
//     (cascadepa_wiring.go): nothing generates one yet, so this discloses that honestly (Art.1).
//
// SPORT: plugins/cascade-pa/telegram TelegramBridge/ADDED, handleInbound/ADDED (P1-E23-W5-S48-T2).

package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// inboundBufferSize bounds Receive's channel: a slow or absent consumer
// drops the oldest queued message rather than blocking the poll loop
// (ChatBridge.Receive's own doc comment).
const inboundBufferSize = 64

// maxRefusalRecords bounds RefusalReport's in-memory history: the newest
// entries evict the oldest once reached, so a long-running bridge never
// grows this without bound.
const maxRefusalRecords = 200

// drainPollInterval is how often Drain re-checks the in-flight count.
// inFlight is an atomic counter, not a sync.WaitGroup: Drain is callable
// WHILE the poll loop still runs, so a new handleInbound's Add(1) can race
// a concurrent Wait() from zero — sync.WaitGroup's own documented unsafe
// case (an earlier draft failed -race for exactly this). An atomic counter
// has no such constraint.
const drainPollInterval = 5 * time.Millisecond

// TelegramBridge implements cascadepa.ChatBridge over a *TelegramModule.
// The stutter (telegram.TelegramBridge) is deliberate, matching
// TelegramModule's own precedent (module.go): this package names every
// Telegram-specific type with the platform prefix rather than leaving it
// implicit in the package name.
//
//nolint:revive // deliberate stutter, matches TelegramModule (module.go)
type TelegramBridge struct {
	module  *TelegramModule
	binding *cascadepa.BindingStore
	subject string

	chat       ChatService
	privacy    ThreadPrivacyResolver
	divergence DivergenceSink

	inbound    chan cascadepa.InboundMessage
	closeGuard sync.Once

	mu       sync.Mutex
	refusals []cascadepa.RefusalRecord

	inFlight atomic.Int64
}

// compile-time proof: the Telegram module satisfies the transport-neutral
// contract this ticket owns (task 2's ChatBridge, proven by this
// implementation for the whatsapp adapter W/S-49.T3 to match).
var _ cascadepa.ChatBridge = (*TelegramBridge)(nil)

// NewTelegramBridge builds a ChatBridge over module and registers this
// bridge's forward handler as module's HandlerText — the ONE call site
// that turns "admitted message" into "chat parity", replacing whatever
// HandlerText the host previously registered (the last RegisterHandler
// call for a kind wins, module.go's own map-assignment semantics). A nil
// chat/privacy/divergence resolves to its fail-closed default (egress.go).
func NewTelegramBridge(module *TelegramModule, binding *cascadepa.BindingStore, subject string,
	chat ChatService, privacy ThreadPrivacyResolver, divergence DivergenceSink) *TelegramBridge {
	b := &TelegramBridge{
		module:     module,
		binding:    binding,
		subject:    subject,
		chat:       chatServiceOrRefuseAll(chat),
		privacy:    threadPrivacyOrRefuseAll(privacy),
		divergence: divergenceOrDiscard(divergence),
		inbound:    make(chan cascadepa.InboundMessage, inboundBufferSize),
	}
	module.RegisterHandler(HandlerText, b.handleInbound)
	return b
}

// Start implements cascadepa.ChatBridge over module.Start — including
// R-16.60a's Windows tier-2 refusal, which module.Start already applies
// before touching the transport at all (see chat_windows_test.go).
func (b *TelegramBridge) Start(ctx context.Context) error { return b.module.Start(ctx) }

// Stop implements cascadepa.ChatBridge over module.Stop, then closes the
// Receive channel exactly once so a consumer's range loop terminates.
func (b *TelegramBridge) Stop(ctx context.Context) error {
	err := b.module.Stop(ctx)
	b.closeGuard.Do(func() { close(b.inbound) })
	return err
}

// Drain waits until no handleInbound call is in flight, or until ctx ends
// first. It does not cancel anything (see this type's own
// cascadepa.ChatBridge doc comment), and it polls rather than blocks — see
// this file's drainPollInterval doc comment for why inFlight is an atomic
// counter and not a sync.WaitGroup.
func (b *TelegramBridge) Drain(ctx context.Context) error {
	if b.inFlight.Load() == 0 {
		return nil
	}
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if b.inFlight.Load() == 0 {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Paired reports whether this bridge's subject has a bound counterpart. A
// lookup failure answers false — an unconfirmed pairing is not a paired
// one.
func (b *TelegramBridge) Paired() bool {
	ok, err := b.binding.Bound(context.Background(), b.subject)
	return err == nil && ok
}

// Receive returns the channel handleInbound/forward deliver every admitted
// message onto.
func (b *TelegramBridge) Receive(context.Context) (<-chan cascadepa.InboundMessage, error) {
	return b.inbound, nil
}

// RefusalReport returns a snapshot of every bridge-boundary refusal
// recorded so far.
func (b *TelegramBridge) RefusalReport() []cascadepa.RefusalRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]cascadepa.RefusalRecord, len(b.refusals))
	copy(out, b.refusals)
	return out
}

// Send delivers body into the chat threadID maps to, at threadID's own
// resolved tier — never a caller-declared one. A threadID this bridge did
// not mint refuses by name; the outbound gate is the SAME guardOutbound +
// client.SendMessage path forward uses for an ALLOW-path reply.
func (b *TelegramBridge) Send(ctx context.Context, threadID string, body []byte) error {
	chatID, ok := chatIDForThread(threadID)
	if !ok {
		return cascade.Newf(cascade.KindNotFound,
			"cascade-pa/telegram: thread %q does not map to a chat this bridge serves", threadID)
	}
	tier, perr := b.privacy.ThreadPrivacy(ctx, threadID)
	if perr != nil {
		tier = cascadepa.TierLocalOnly
	}
	text := b.module.guardOutbound(ctx, string(body), "")
	return b.module.client.SendMessage(ctx, chatID, tier, text)
}

// handleInbound is the HandlerText this bridge registers: chat parity's
// entry point, called only after module.go's own gates (secret-scan,
// pairing/binding, media, elevated-verb) have already admitted msg.
func (b *TelegramBridge) handleInbound(ctx context.Context, msg InboundMessage) error {
	b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	m := msg.Update.Message
	if m == nil || m.From == nil {
		return nil // module.go's admitMessage never reaches HandlerText without both
	}
	threadID := threadIDForChat(m.Chat.ID)
	corrID := "chat:" + strconv.FormatInt(m.Chat.ID, 10)
	chatKind := chatKindOf(m)

	tier, perr := b.privacy.ThreadPrivacy(ctx, threadID)
	if perr != nil {
		tier = cascadepa.TierLocalOnly // §5.16 fail-closed: unresolvable follows local-only
	}

	// THE REAL DECISION (see this file's header): a nil-content probe
	// through the already-registered egress class, mirroring
	// BotClient.fetchOne's own capability probe.
	if _, err := b.module.client.egress.Guard(ctx, tier, nil); err != nil {
		b.refuse(ctx, m.Chat.ID, threadID, tier, corrID, chatKind)
		return nil
	}
	return b.forward(ctx, msg, m, threadID, tier, chatKind)
}

// refuse records and replies a bridge-boundary refusal: no message content
// exits in either the reply or the divergence event.
func (b *TelegramBridge) refuse(ctx context.Context, chatID int64, threadID string,
	tier cascadepa.SensitivityTier, corrID, chatKind string) {
	reason := refusalReasonForTier(tier)
	at := b.module.now()
	b.recordRefusal(cascadepa.RefusalRecord{
		ThreadID: threadID, ResolvedTier: string(tier), Reason: reason, CorrelationID: corrID, At: at,
	})
	b.divergence.EmitRefused(ctx, RefusalEvent{
		ThreadID: threadID, ResolvedTier: string(tier), Reason: reason, CorrelationID: corrID, At: at,
	})
	b.module.reply(ctx, chatID, chatKind, reason)
}

// forward appends msg's text as a "user" turn (chat parity) and replies
// with the honest disclosure (or a recorded-but-unreachable notice on a
// ChatService failure), then delivers onto Receive's channel. It refuses,
// before any side effect, an InboundMessage whose Origin does not match
// this adapter's own constant or whose Untrusted flag is unset (R-21.227's
// never-clearable marker; delivered messages pin Untrusted to the literal
// true below, never msg's own field) — the ONE place a telegram.InboundMessage
// becomes a cascadepa.InboundMessage.
func (b *TelegramBridge) forward(ctx context.Context, msg InboundMessage, m *Message,
	threadID string, tier cascadepa.SensitivityTier, chatKind string) error {
	if msg.Origin != OriginBridgeTelegram || !msg.Untrusted {
		return cascade.Newf(cascade.KindIntegrity,
			"cascade-pa/telegram: admitted update carries origin %q untrusted=%v, want %q/true",
			msg.Origin, msg.Untrusted, OriginBridgeTelegram)
	}
	turnID, err := b.chat.AppendTurn(ctx, threadID, "user", m.Text)
	if err != nil {
		b.module.reply(ctx, m.Chat.ID, chatKind,
			"cascade-pa/telegram: this message could not be recorded right now; try again shortly")
		return err
	}
	text := b.module.guardOutbound(ctx, replyText(threadID, turnID), chatKind)
	_ = b.module.client.SendMessage(ctx, m.Chat.ID, tier, text)
	b.deliver(cascadepa.InboundMessage{
		ThreadID: threadID, SenderRef: strconv.FormatInt(m.From.ID, 10),
		Body: []byte(m.Text), Received: time.Unix(m.Date, 0).UTC(),
		Origin: cascadepa.OriginBridgeTelegram, Untrusted: true,
	})
	return nil
}

// replyText is the ALLOW path's honest disclosure: the turn was recorded,
// and nothing in this tree yet generates an assistant reply to it (see
// this file's header).
func replyText(threadID, turnID string) string {
	return fmt.Sprintf("message recorded (thread %s, turn %s); no assistant reply is available yet",
		threadID, turnID)
}

// recordRefusal appends r, evicting the oldest entry once
// maxRefusalRecords is reached.
func (b *TelegramBridge) recordRefusal(r cascadepa.RefusalRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refusals = append(b.refusals, r)
	if len(b.refusals) > maxRefusalRecords {
		b.refusals = b.refusals[len(b.refusals)-maxRefusalRecords:]
	}
}

// deliver pushes msg onto the Receive channel, dropping it rather than
// blocking the poll loop when no consumer is keeping up (doc comment on
// cascadepa.ChatBridge.Receive).
func (b *TelegramBridge) deliver(msg cascadepa.InboundMessage) {
	select {
	case b.inbound <- msg:
	default:
	}
}
