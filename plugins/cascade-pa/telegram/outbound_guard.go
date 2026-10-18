// Purpose (this file): the owner-DM-only guard. Every outbound Bot API send
//   passes outboundChatAllowed in the transport (BotClient), so the bridge
//   speaks only to a paired owner's private chat, and answers only the
//   callback ids it recorded at admission.
//
// Inputs: an OwnerChatSource (the module's binding-store adapter), a
//   DivergenceSink and an injected clock, set once with SetOwnerGuard.
//
// Outputs: nil from outboundChatAllowed only for an allowlisted sender's DM;
//   KindPolicyDenied with zero transport calls and one content-free
//   bridge.refused (reason chat-not-paired) for everything else.
//
// Constraints:
//   - FAIL CLOSED. A client whose guard is unset refuses every send; a store
//     error refuses (never admits); a group, supergroup or channel id is
//     negative and refuses without a store lookup.
//   - ONE CHOKE POINT. SendMessage, sendApprovalButtons and
//     AnswerCallbackQuery are the only methods that reach Doer.Do for a send;
//     TestEveryOutboundPathUsesTheChatGuard fails when another one appears.
//   - THE REFUSAL NAMES NOTHING. The event carries a reason and a time: no
//     thread id, chat id, sender id or text, so the audit trail is not itself
//     a leak path.
//   - THE CALLBACK SET IS BOUNDED. An id is recorded only for an owner-DM
//     callback, removed on its one answer and expired after callbackIDTTL on
//     the injected clock.
//
// SPORT: plugins/cascade-pa/telegram OwnerChatSource/ADDED,
//   BotClient.SetOwnerGuard/ADDED (P1-CPA-17).

package telegram

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The bridge.refused reasons this file emits. reasonChatNotPaired is the send
// guard's; reasonUpdateNotOwnerDM marks an inbound update consumed silently, so
// an audit trail can tell "never admitted" from "a send was attempted and
// refused".
const (
	reasonChatNotPaired    = "chat-not-paired"
	reasonUpdateNotOwnerDM = "update-not-owner-dm"
	reasonCallbackRefused  = "callback-not-admitted"
)

// callbackIDTTL is how long an admitted callback id stays answerable. An
// answered id is remembered for answeredRetention so a replayed admission
// inside that window cannot revive it; the real replay barrier is the durable
// update-id ledger (client.go deliver) and Telegram's unique callback ids, this
// is a bounded second line. maxCallbackIDs caps the live plus answered ids.
const (
	callbackIDTTL     = 60 * time.Second
	answeredRetention = 10 * callbackIDTTL
	maxCallbackIDs    = 1024
)

// errChatNotPaired is the typed refusal outboundChatAllowed returns.
var errChatNotPaired = cascade.New(cascade.KindPolicyDenied,
	"cascade-pa/telegram: chat is not the paired owner chat")

// errCallbackNotAdmitted refuses an answer for a callback id that was never
// recorded, was already answered or has expired.
var errCallbackNotAdmitted = cascade.New(cascade.KindPolicyDenied,
	"cascade-pa/telegram: callback was not admitted from the paired owner chat")

// OwnerChatSource answers whether chatID is the paired owner's DM.
type OwnerChatSource interface {
	AllowsChat(ctx context.Context, chatID int64) (bool, error)
}

// ownerGuard is the wired guard: the allowlist view, the audit sink, the clock
// and the bounded set of admitted callback ids.
type ownerGuard struct {
	src   OwnerChatSource
	sink  DivergenceSink
	clock func() time.Time

	mu        sync.Mutex
	callbacks map[string]time.Time // admitted, unanswered: id -> deadline
	answered  map[string]time.Time // answered: id -> when it may be forgotten
}

// SetOwnerGuard arms the guard once. A nil src, sink or clock is refused with
// KindInvalidInput and leaves the client fail-closed; a second call is refused
// with KindConflict and leaves the first guard in place.
func (c *BotClient) SetOwnerGuard(src OwnerChatSource, sink DivergenceSink, clock func() time.Time) error {
	if src == nil || sink == nil || clock == nil {
		return cascade.New(cascade.KindInvalidInput,
			"cascade-pa/telegram: owner guard needs a chat source, a refusal sink and a clock")
	}
	g := &ownerGuard{src: src, sink: sink, clock: clock,
		callbacks: map[string]time.Time{}, answered: map[string]time.Time{}}
	if !c.guard.CompareAndSwap(nil, g) {
		return cascade.New(cascade.KindConflict, "cascade-pa/telegram: owner guard is already armed")
	}
	return nil
}

// outboundChatAllowed is called first by every send method. It returns nil
// only when the guard's source says chatID is an allowlisted sender's DM.
func (c *BotClient) outboundChatAllowed(ctx context.Context, chatID int64) error {
	g := c.guard.Load()
	if g == nil {
		return errChatNotPaired
	}
	if ok, err := g.src.AllowsChat(ctx, chatID); err != nil || !ok {
		g.emitRefused(ctx, reasonChatNotPaired)
		return errChatNotPaired
	}
	return nil
}

// emitRefused publishes one content-free bridge.refused. It uses a context
// that outlives a cancelled poll so the record is not lost at shutdown.
func (g *ownerGuard) emitRefused(ctx context.Context, reason string) {
	g.sink.EmitRefused(context.WithoutCancel(ctx), RefusalEvent{Reason: reason, At: g.clock()})
}

// emitRefusedInbound publishes the one bridge.refused for an update that was
// consumed silently. A client with no guard has no sink: nothing to record.
func (c *BotClient) emitRefusedInbound(ctx context.Context, reason string) {
	if g := c.guard.Load(); g != nil {
		g.emitRefused(ctx, reason)
	}
}

// allowsOwnerDM reports whether chatID is the paired owner's DM. Without a
// guard, or on a store error, it is false.
func (c *BotClient) allowsOwnerDM(ctx context.Context, chatID int64) bool {
	g := c.guard.Load()
	if g == nil {
		return false
	}
	ok, err := g.src.AllowsChat(ctx, chatID)
	return err == nil && ok
}

// recordCallback remembers an admitted callback id until it is answered or
// callbackIDTTL passes, and reports whether it is now answerable. Expired ids
// are dropped first so the set stays bounded; an id already recorded keeps its
// original deadline, and an id answered within answeredRetention is not revived.
func (c *BotClient) recordCallback(id string) bool {
	g := c.guard.Load()
	if g == nil || id == "" {
		return false
	}
	now := g.clock()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(now)
	if _, seen := g.callbacks[id]; seen {
		return false
	}
	if _, done := g.answered[id]; done || len(g.callbacks)+len(g.answered) >= maxCallbackIDs {
		return false
	}
	g.callbacks[id] = now.Add(callbackIDTTL)
	return true
}

// consumeCallback removes id from the answerable set, remembers it as answered
// for answeredRetention, and reports whether it was recorded and live.
func (c *BotClient) consumeCallback(id string) bool {
	g := c.guard.Load()
	if g == nil {
		return false
	}
	now := g.clock()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(now)
	_, ok := g.callbacks[id]
	delete(g.callbacks, id)
	if ok {
		g.answered[id] = now.Add(answeredRetention)
	}
	return ok
}

// sweepLocked drops every id whose deadline has passed. g.mu must be held.
func (g *ownerGuard) sweepLocked(now time.Time) {
	for _, set := range []map[string]time.Time{g.callbacks, g.answered} {
		for id, deadline := range set {
			if !now.Before(deadline) {
				delete(set, id)
			}
		}
	}
}

// ownerChatSource is the module's OwnerChatSource: a private chat has
// chat.id == user id, and group and channel ids are negative.
type ownerChatSource struct{ m *TelegramModule }

// AllowsChat reports whether chatID is positive and its user id is on the
// subject's allowlist. A store error refuses.
func (s ownerChatSource) AllowsChat(ctx context.Context, chatID int64) (bool, error) {
	if chatID <= 0 || s.m.binding == nil {
		return false, nil
	}
	return s.m.binding.IsAllowed(ctx, s.m.subject, strconv.FormatInt(chatID, 10))
}

// refusalSink forwards to the module's current DivergenceSink so the host can
// attach the real one after construction; the default discards.
type refusalSink struct{ m *TelegramModule }

// EmitRefused forwards e to the module's sink.
func (s refusalSink) EmitRefused(ctx context.Context, e RefusalEvent) {
	s.m.refusal().EmitRefused(ctx, e)
}

// SetRefusalSink attaches the host's bridge.refused sink. A nil sink restores
// the discarding default.
func (m *TelegramModule) SetRefusalSink(d DivergenceSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refusalDest = divergenceOrDiscard(d)
}

// refusal reads the current sink under the lock.
func (m *TelegramModule) refusal() DivergenceSink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refusalDest == nil {
		return discardDivergenceSink{}
	}
	return m.refusalDest
}

// armOwnerGuard wires the module's guard into its client at construction.
func (m *TelegramModule) armOwnerGuard() {
	if m.client == nil || m.clock == nil {
		return
	}
	_ = m.client.SetOwnerGuard(ownerChatSource{m}, refusalSink{m}, m.clock.Now)
}

// isOwnerDMMessage reports the shape every admitted message has: a sender, a
// private chat, and chat.id == from.id.
func isOwnerDMMessage(msg *Message) bool {
	return msg.From != nil && msg.From.ID > 0 && msg.Chat.Type == "private" && msg.Chat.ID == msg.From.ID
}

// admitCallback reports whether cq came from the paired owner's DM and its id
// was newly recorded, so exactly one answer is allowed. Otherwise it records
// the one bridge.refused for the update (update-not-owner-dm for a non-owner
// origin, callback-not-admitted for an owner callback whose id was a replay,
// over the cap or empty) and returns false: the caller stays silent.
func (m *TelegramModule) admitCallback(ctx context.Context, cq *CallbackQuery) bool {
	if cq.Message == nil || cq.Message.Chat.Type != "private" ||
		cq.Message.Chat.ID != cq.From.ID || !m.client.allowsOwnerDM(ctx, cq.From.ID) {
		m.client.emitRefusedInbound(ctx, reasonUpdateNotOwnerDM)
		return false
	}
	if !m.client.recordCallback(cq.ID) {
		m.client.emitRefusedInbound(ctx, reasonCallbackRefused)
		return false
	}
	return true
}

// dispatchUnbound handles a private-chat message on a bot with no binding: a
// "/pair <code>" attempt is verified and everything else is consumed silently.
func (m *TelegramModule) dispatchUnbound(ctx context.Context, msg *Message, senderID string) {
	code, isPair := isPairCommand(msg.Text)
	if !isPair {
		m.consumeSilently(ctx)
		return
	}
	// D4: a secret-shaped "/pair <token>" never reaches the verifier.
	if refused, outcome, unconfigured := m.refusesSecret(msg.Text); refused {
		if unconfigured == nil {
			_ = m.publishQuarantine(ctx, quarantineOriginInboundText, chatKindOf(msg), outcome)
		}
		m.consumeSilently(ctx)
		return
	}
	m.pairer.handlePairCommand(ctx, m, m.subject, msg.Chat.ID, senderID, code)
}

// consumeSilently ends an update with no reply of any kind and one
// content-free bridge.refused.
func (m *TelegramModule) consumeSilently(ctx context.Context) {
	m.client.emitRefusedInbound(ctx, reasonUpdateNotOwnerDM)
}
