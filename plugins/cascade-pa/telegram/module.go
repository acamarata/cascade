// Purpose: TelegramModule's lifecycle (start/stop/drain) and the dispatch
//   gate in front of every registered Handler.
//
// Inputs: a subject id, a *BotClient, the cascadepa binding store, the pair
//   coordinator, and the host's cascadepa.ElevationPolicy.
//
// Outputs: Start/Stop manage the poll goroutine; dispatch enforces, IN
//   ORDER: the R-21.203/R-21.105 secret-value refusal (refuse.go, BEFORE any
//   pairing/binding lookup — D4), pairing/allowlist (fail-closed), media
//   refusal (§R-21.227), elevated-verb refusal (§5.14 LOCAL-ONLY), then
//   Origin/Untrusted stamping — IDENTICALLY on the text path and callback
//   path.
//
// Constraints, each a defect this file was rewritten to close:
//   - ONE GATE, TWO TRANSPORTS. Every content gate (secret-value, media,
//     elevated-verb) applies identically on dispatchMessage and
//     dispatchCallback; a callback used to skip checks its byte-identical
//     text sibling enforced.
//   - CLASSIFICATION IS THE HOST'S. The elevated-verb decision comes from
//     cascadepa.RefusesElevated over the policy the host injects, never from
//     a prefix list kept here (see cascadepa/bridge_policy.go).
//   - A CODE IS REDEEMABLE ONLY WHILE UNBOUND (T0 D5). On a bound bot a
//     non-allowlisted sender gets the same "not paired" reply whether or not
//     the text is "/pair <code>", and nothing is consumed or counted — a
//     stranger cannot burn the owner's outstanding code, and cannot tell a
//     bound bot from an unbound one.
//   - STOP'S CANCELLATION REACHES EVERYTHING. dispatch takes the poll
//     goroutine's ctx, so Stop's cancellation reaches every handler/reply.
//
// SPORT: plugins/cascade-pa/telegram TelegramModule/ADDED,
//   InboundMessage/ADDED (P1-E23-W5-S48-T1).

package telegram

import (
	"context"
	"strconv"
	"sync"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// OriginBridgeTelegram is the fixed, never-clearable Origin stamp every
// decoded Update carries (no setter takes any other value).
const OriginBridgeTelegram = "bridge-telegram"

// InboundMessage wraps a decoded Update with the R-21.227 provenance stamp.
// Every consumer that persists or renders bridge content carries both
// fields through.
type InboundMessage struct {
	Update    Update
	Origin    string
	Untrusted bool
}

// stampInbound is the ONLY constructor for InboundMessage: no path produces
// one with Untrusted=false.
func stampInbound(u Update) InboundMessage {
	return InboundMessage{Update: u, Origin: OriginBridgeTelegram, Untrusted: true}
}

// Handler processes one InboundMessage already admitted past pairing,
// allowlist, media refusal and the elevated-verb refusal.
type Handler func(ctx context.Context, msg InboundMessage) error

// The handler kinds RegisterHandler accepts.
const (
	HandlerText          = "text"
	HandlerCallbackQuery = "callback_query"
)

// The typed refusals this module returns or replies with.
var (
	// errWindowsTier2 is R-16.60a's refusal. Its text is the contract's
	// verbatim string and module_windows_test.go asserts that exact text
	// independently, so blanking this message fails the windows lane.
	errWindowsTier2 = cascade.New(cascade.KindUnsupported,
		"bridge requires the daemon (Windows tier-2)")
	errElevationOverBridge = cascade.New(cascade.KindElevationRequired,
		"cascade-pa/telegram: elevated verbs are refused over the bridge (local-only)")
)

// The subject-facing replies. Every one of them is fixed text: a refusal
// that varied with internal state would be an oracle.
const (
	replyNotPaired     = "not paired"
	replyAlreadyPaired = "already paired"
	replyMediaRefused  = "media not supported over the bridge"
)

// TelegramModule owns one bot's lifecycle: the poll goroutine and the
// fail-closed dispatch gate in front of every registered Handler. The
// stutter (telegram.TelegramModule) is deliberate: the ticket contract names
// it exactly this way, matching egress.EgressClass's precedent.
//
//nolint:revive // deliberate stutter, see comment above
type TelegramModule struct {
	subject   string
	client    *BotClient
	binding   *cascadepa.BindingStore
	pairer    *pairCoordinator
	elevation cascadepa.ElevationPolicy
	handlers  map[string]Handler
	// clock backs quarantine.go's now() (T0 D8): required, never
	// m.pairer.clock — a nil pairer must not panic minting a timestamp.
	clock cascadepa.PairClock

	// secretScanner/quarantine back R-21.203/R-21.105's refusal gate
	// (refuse.go, quarantine.go); nil by default — scanner() substitutes
	// the fail-closed refusingSecretScanner; a nil quarantine makes
	// publishQuarantine report ErrNoQuarantineSink rather than discard.
	secretScanner SecretScanner
	quarantine    QuarantineSink

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewTelegramModule constructs a module for subject (the bridge instance
// identity IssueCode/Bind key on). A nil elevation policy resolves to the
// refuse-everything default. clock is required (T0 D8): see quarantine.go.
func NewTelegramModule(subject string, client *BotClient, binding *cascadepa.BindingStore,
	pairer *pairCoordinator, elevation cascadepa.ElevationPolicy, clock cascadepa.PairClock) *TelegramModule {
	return &TelegramModule{
		subject: subject, client: client, binding: binding, pairer: pairer,
		elevation: cascadepa.ElevationOrRefuseAll(elevation),
		handlers:  make(map[string]Handler),
		clock:     clock,
	}
}

// RegisterHandler wires kind (HandlerText or HandlerCallbackQuery) to h.
func (m *TelegramModule) RegisterHandler(kind string, h Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[kind] = h
}

// handler reads one registered handler under the lock.
func (m *TelegramModule) handler(kind string) Handler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handlers[kind]
}

// Start launches the long-poll loop. Windows tier-2 refuses immediately,
// before touching BotClient at all (R-16.60a, module_windows.go).
func (m *TelegramModule) Start(ctx context.Context) error {
	if refusal := platformBridgeRefusal(); refusal != nil {
		return refusal
	}
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return cascade.New(cascade.KindConflict, "cascade-pa/telegram: module already started")
	}
	pctx, cancel := context.WithCancel(ctx)
	m.cancel, m.done = cancel, make(chan struct{})
	done := m.done
	m.mu.Unlock()

	go func() {
		defer close(done)
		_ = m.client.Poll(pctx, m.dispatch)
	}()
	return nil
}

// Stop cancels the poll loop and blocks (drain) until it exits, or until ctx
// ends first.
func (m *TelegramModule) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dispatch is BotClient's onUpdate callback — the entry point for every gate
// this ticket's acceptance criteria describe. ctx is the POLL goroutine's,
// so Stop's cancellation reaches every handler and every reply.
func (m *TelegramModule) dispatch(ctx context.Context, u Update) {
	switch {
	case u.Message != nil:
		m.dispatchMessage(ctx, u)
	case u.CallbackQuery != nil:
		m.dispatchCallback(ctx, u)
	}
}

// dispatchMessage gates one Message Update. The secret-value scan (D4) runs
// FIRST, before any pairing/binding lookup.
func (m *TelegramModule) dispatchMessage(ctx context.Context, u Update) {
	msg := u.Message
	if refused, outcome, unconfigured := m.refusesSecret(msg.Text); refused {
		m.refuseInboundText(ctx, msg, outcome, unconfigured)
		return
	}
	if msg.From == nil {
		// A channel post or anonymous-admin message carries no From: there
		// is no sender identity to pair or allowlist, so there is nothing to
		// admit.
		return
	}
	senderID := strconv.FormatInt(msg.From.ID, 10)
	bound, err := m.binding.Bound(ctx, m.subject)
	if err != nil {
		m.reply(ctx, msg.Chat.ID, chatKindOf(msg), replyNotPaired)
		return
	}
	if !bound {
		m.dispatchUnbound(ctx, msg, senderID)
		return
	}
	allowed, err := m.binding.IsAllowed(ctx, m.subject, senderID)
	if err != nil || !allowed {
		// Identical to a stranger's plain text, /pair included: no code is
		// consumed and no attempt is counted (T0 D5).
		m.reply(ctx, msg.Chat.ID, chatKindOf(msg), replyNotPaired)
		return
	}
	if _, isPair := isPairCommand(msg.Text); isPair {
		m.reply(ctx, msg.Chat.ID, chatKindOf(msg), replyAlreadyPaired)
		return
	}
	m.admitMessage(ctx, u, msg)
}

// dispatchUnbound handles a message on a bot with no binding at all: a
// "/pair <code>" attempt is verified, everything else is refused.
func (m *TelegramModule) dispatchUnbound(ctx context.Context, msg *Message, senderID string) {
	if code, isPair := isPairCommand(msg.Text); isPair {
		m.pairer.handlePairCommand(ctx, m, m.subject, msg.Chat.ID, senderID, code)
		return
	}
	m.reply(ctx, msg.Chat.ID, chatKindOf(msg), replyNotPaired)
}

// admitMessage runs the remaining content gates (secret-value already ran,
// at the top of dispatchMessage) and hands off to the text handler.
func (m *TelegramModule) admitMessage(ctx context.Context, u Update, msg *Message) {
	if msg.HasRefusedMedia() {
		m.reply(ctx, msg.Chat.ID, chatKindOf(msg), replyMediaRefused)
		return
	}
	// commandShaped=false: a message's text is a verb only when it carries a
	// leading slash, which VerbCandidates detects itself. Ordinary prose must
	// stay dispatchable.
	if cascadepa.RefusesElevated(m.elevation, msg.Text, false) {
		m.reply(ctx, msg.Chat.ID, chatKindOf(msg), errElevationOverBridge.Error())
		return
	}
	if h := m.handler(HandlerText); h != nil {
		_ = h(ctx, stampInbound(u))
	}
}

// dispatchCallback is dispatchMessage's callback twin (ONE GATE, TWO
// TRANSPORTS), answered over answerCallbackQuery. The secret-value scan
// runs FIRST, before the allowlist lookup (D4).
func (m *TelegramModule) dispatchCallback(ctx context.Context, u Update) {
	cq := u.CallbackQuery
	if refused, outcome, unconfigured := m.refusesSecret(cq.Data); refused {
		m.refuseInboundCallback(ctx, cq, outcome, unconfigured)
		return
	}
	senderID := strconv.FormatInt(cq.From.ID, 10)
	allowed, err := m.binding.IsAllowed(ctx, m.subject, senderID)
	if err != nil || !allowed {
		m.answer(ctx, cq.ID, chatKindOf(cq.Message), replyNotPaired)
		return
	}
	if cq.Message != nil && cq.Message.HasRefusedMedia() {
		m.answer(ctx, cq.ID, chatKindOf(cq.Message), replyMediaRefused)
		return
	}
	// commandShaped=true: a callback's data is always machine-generated
	// command data, so it is classified even without a leading slash.
	if cascadepa.RefusesElevated(m.elevation, cq.Data, true) {
		m.answer(ctx, cq.ID, chatKindOf(cq.Message), errElevationOverBridge.Error())
		return
	}
	if h := m.handler(HandlerCallbackQuery); h != nil {
		_ = h(ctx, stampInbound(u))
	}
}

// reply/answer live in refuse.go; exported Answer lives in approval_auth.go.
