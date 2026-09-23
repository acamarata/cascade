// Purpose (this file): the three host-mediated seams chat.go's forward
//   handler needs beyond BotClient's existing per-call EgressGate (client.go)
//   — a thread's §5.16 privacy mode, the S-43.T2 chat.append_turn service,
//   and the divergence-event sink a refusal publishes to — plus the pure
//   thread-id/reason-text helpers chat.go composes them with.
//
// Inputs: a chat id (this module's own unit of "which conversation"), and
//   the host's real implementations of the three interfaces below.
//
// Outputs: a deterministic thread id for a chat, the fixed refusal reason
//   text for a resolved tier, and the fail-closed defaults every interface
//   here resolves to when the host has not wired a real one.
//
// Constraints:
//   - THIS PACKAGE NEVER IMPORTS internal/ (R-14.69, same boundary
//     client.go's EgressGate and refuse.go's SecretScanner already draw):
//     ChatService and ThreadPrivacyResolver are pkg-facing seams over
//     internal/conversation's real adapter/store, bound by the host
//     composition root (internal/plugins/cascadepa_bridge_chat_wiring.go).
//   - EVERY DEFAULT REFUSES. A nil ThreadPrivacyResolver or ChatService
//     resolves to a capability that fails every call, mirroring
//     unconfiguredEgressGate (client.go) and refusingSecretScanner
//     (refuse.go, T0 D1): a half-wired host refuses traffic, it never
//     admits it.
//   - THE REASON TEXT IS NOT THE SECURITY DECISION. Guard/InterceptClass
//     (client.go, the REAL egress-class engine) decides whether a tier may
//     leave; refusalReasonForTier only chooses which of the contract's two
//     fixed strings to reply with, from a tier chat.go already resolved —
//     it never decides ALLOW/REFUSE itself (LANE-RULES §5: never re-derive
//     a security check).
//
// SPORT: plugins/cascade-pa/telegram ChatService/ADDED,
//   ThreadPrivacyResolver/ADDED, DivergenceSink/ADDED (P1-E23-W5-S48-T2).

package telegram

import (
	"context"
	"strconv"
	"strings"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ChatService is chat.append_turn's pkg-facing seam: the S-43.T2 adapter's
// wire method, reached host-mediated (R-14.69) rather than by importing
// internal/conversation directly. The real implementation dials the
// daemon's own chat.* RPC namespace, matching internal/plugins's
// cascadePAClient precedent for the identical method.
type ChatService interface {
	// AppendTurn records one turn (role, text) on threadID and returns the
	// turn id the store assigned.
	AppendTurn(ctx context.Context, threadID, role, text string) (turnID string, err error)
}

// ThreadPrivacyResolver resolves threadID's §5.16 sensitivity tier — the
// S-44.T2 conversation.Store.ThreadPrivacy call, host-mediated for the
// identical R-14.69 reason ChatService is.
type ThreadPrivacyResolver interface {
	ThreadPrivacy(ctx context.Context, threadID string) (cascadepa.SensitivityTier, error)
}

// DivergenceSink records one bridge.refused divergence event (C/S-04.T3
// bus). The real implementation is bound at the composition root, over the
// same BridgeEventPublisher the S-48.T1/T3 journal already records
// lockouts and quarantines through.
type DivergenceSink interface {
	EmitRefused(ctx context.Context, e RefusalEvent)
}

// RefusalEvent is one bridge.refused record: {thread_id, resolved_tier,
// reason, correlation_id} per the contract, never message content.
type RefusalEvent struct {
	ThreadID      string
	ResolvedTier  string
	Reason        string
	CorrelationID string
	At            time.Time
}

// errNoThreadPrivacyResolver/errNoChatService are the fail-closed reasons a
// forward refuses when the host has not wired a real capability — a
// missing capability, exactly like ErrNoSecretScanner (refuse.go) and
// ErrNoEgressCapability (client.go).
var (
	errNoThreadPrivacyResolver = cascade.New(cascade.KindUnavailable,
		"cascade-pa/telegram: no thread privacy resolver is wired; every forward refuses")
	errNoChatService = cascade.New(cascade.KindUnavailable,
		"cascade-pa/telegram: no chat service is wired; every forward refuses")
)

// unconfiguredThreadPrivacyResolver is ThreadPrivacyResolver's fail-closed
// default: an unwired host resolves every thread to TierLocalOnly (never
// bridgeable) rather than to a tier that happens to be allowed.
type unconfiguredThreadPrivacyResolver struct{}

func (unconfiguredThreadPrivacyResolver) ThreadPrivacy(
	context.Context, string,
) (cascadepa.SensitivityTier, error) {
	return cascadepa.TierLocalOnly, errNoThreadPrivacyResolver
}

// threadPrivacyOrRefuseAll substitutes the fail-closed default for a nil
// resolver, mirroring ElevationOrRefuseAll's naming.
func threadPrivacyOrRefuseAll(r ThreadPrivacyResolver) ThreadPrivacyResolver {
	if r == nil {
		return unconfiguredThreadPrivacyResolver{}
	}
	return r
}

// unconfiguredChatService is ChatService's fail-closed default.
type unconfiguredChatService struct{}

func (unconfiguredChatService) AppendTurn(context.Context, string, string, string) (string, error) {
	return "", errNoChatService
}

// chatServiceOrRefuseAll substitutes the fail-closed default for a nil
// service.
func chatServiceOrRefuseAll(c ChatService) ChatService {
	if c == nil {
		return unconfiguredChatService{}
	}
	return c
}

// discardDivergenceSink is DivergenceSink's nil-safe default: it discards
// the record rather than panicking. This never widens what is admitted —
// unlike ThreadPrivacyResolver/ChatService, a divergence event is written
// AFTER a refusal decision the real egress class has already made
// (client.go's Guard/InterceptClass), so a lost record changes no
// decision, matching bridgeJournal.publish's own "a publish failure never
// changes a decision" rule (internal/plugins/cascadepa_bridge_events.go).
// Production always wires a real sink (composition root); see this
// package's HONEST GAPS note in the ticket journal for the same discard-on-
// nil tradeoff T3's QuarantineSink made before its own confirming review.
type discardDivergenceSink struct{}

func (discardDivergenceSink) EmitRefused(context.Context, RefusalEvent) {}

// divergenceOrDiscard substitutes the nil-safe default for a nil sink.
func divergenceOrDiscard(d DivergenceSink) DivergenceSink {
	if d == nil {
		return discardDivergenceSink{}
	}
	return d
}

// bridgeThreadPrefix namespaces every thread id this module mints from a
// Telegram chat id, so a bridge thread can never collide with a thread a
// CLI or another adapter created.
const bridgeThreadPrefix = "bridge-telegram:"

// threadIDForChat derives the deterministic conversation thread id for
// chatID: one Telegram chat maps to exactly one thread, always, so a
// privacy mode set on that thread (via `cascade chat --thread
// bridge-telegram:<id> --private`, an operator's own local action) governs
// every future message from that chat. There is no thread-creation path on
// this side: an operator opts a chat into internal/public tiers from the
// local CLI, matching this ticket's fail-closed default (an unmarked
// thread reads as restricted, per internal/conversation/privacy.go, and
// restricted is excluded from the bridge's AllowedTiers).
func threadIDForChat(chatID int64) string {
	return bridgeThreadPrefix + strconv.FormatInt(chatID, 10)
}

// chatIDForThread reverses threadIDForChat, for ChatBridge.Send's
// thread->chat direction. ok is false for any threadID this module did not
// mint (a foreign or malformed id) — Send refuses those by name rather
// than guessing a chat.
func chatIDForThread(threadID string) (chatID int64, ok bool) {
	rest, found := strings.CutPrefix(threadID, bridgeThreadPrefix)
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// The contract's two fixed refusal reply texts (task 4 / AC), plus the
// text for a refusal the real egress engine issued for a reason OTHER
// than the thread's own tier (see refusalReasonForTier below) — reused
// verbatim for both the Telegram reply and the divergence event's Reason
// field.
const (
	reasonThreadLocalOnly   = "thread is local-only, cannot bridge"
	reasonThreadRestricted  = "thread is restricted, cannot bridge"
	reasonBridgeUnavailable = "message could not be sent right now"
)

// refusalReasonForTier picks the reply text for an ALREADY-RESOLVED tier,
// given that Guard already refused it (chat.go calls this only after that
// real decision). It does not decide whether to refuse — see this file's
// header. The four cases matter: TierRestricted/TierLocalOnly are the
// contract's own §5.16 exclusions and get their named text; TierInternal
// and TierPublic are tiers the bridge class DOES admit, so a refusal at
// one of those tiers is not a privacy exclusion at all — it is the real
// engine refusing for an unrelated reason (no capability wired, a
// substitution failure), and claiming "thread is local-only" there would
// misreport why the message did not go out. Anything outside the closed
// four-tier vocabulary (an unresolvable tier chat.go has already mapped
// to TierLocalOnly per §5.16, or a resolver returning a malformed value)
// falls through to the fail-closed local-only text.
func refusalReasonForTier(tier cascadepa.SensitivityTier) string {
	switch tier {
	case cascadepa.TierRestricted:
		return reasonThreadRestricted
	case cascadepa.TierInternal, cascadepa.TierPublic:
		return reasonBridgeUnavailable
	case cascadepa.TierLocalOnly:
		return reasonThreadLocalOnly
	default: // an unresolvable or malformed tier: still fail-closed local-only
		return reasonThreadLocalOnly
	}
}
