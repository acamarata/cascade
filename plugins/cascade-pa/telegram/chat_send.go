// Purpose (this file): TelegramBridge.Send — the outbound half of chat
//   parity (fix18): delivers cascade-pa content into threadID's mapped
//   Telegram chat, gated by the SAME binding this bridge admits inbound
//   senders through (K3, round 2), and, on any POLICY refusal, records and
//   reports the SAME bridge.refused divergence event handleInbound's
//   refuse() (chat.go) already emits inbound. Split from chat.go to stay
//   under Art.10.3's 300-line cap.
//
// Inputs: a threadID this module minted (threadIDForChat, egress.go, now
//   canonical-only per K3), the body to deliver, and TelegramBridge's own
//   already-wired collaborators (module, binding, privacy, divergence).
//
// Outputs: one client.SendMessage call on ALLOW, or a typed refusal plus
//   (for a POLICY refusal only) a bridge.refused event on REFUSE.
//
// Constraints:
//   - THE REAL ENGINE DECIDES. A nil-content Guard probe (the SAME idiom
//     handleInbound already uses) is the tier decision; this file never
//     re-derives ALLOW/REFUSE from the resolved tier itself (LANE-RULES
//     SECURITY SEAMS) — it only reports what that call already decided.
//   - TRANSPORT ERRORS ARE NOT POLICY REFUSALS (K5, round 2): the probe
//     above runs BEFORE client.SendMessage precisely so a real Bot
//     API/network failure — which only SendMessage's own doer.Do call can
//     produce, once Guard has already passed — is returned to the caller
//     verbatim, never counted as a bridge.refused divergence event.
//   - CONTENT-FREE. The RefusalRecord/RefusalEvent this file publishes
//     carry ThreadID/ResolvedTier/Reason/CorrelationID only — never body.
//
// SPORT: plugins/cascade-pa/telegram TelegramBridge.Send/CHANGED,
//   refuseSend/ADDED (fix18 K3/K5, P1-E23-W5-S48-T2 producer-side gap).

package telegram

import (
	"context"
	"strconv"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// reasonSendSecretRefused and reasonSendNotBound are Send's fixed,
// non-interpolated reason texts — the outbound twins of
// refusalReasonForTier's tier-refusal texts (egress.go), reused verbatim
// for both the RefusalRecord and the bridge.refused event's Reason field.
const (
	reasonSendSecretRefused = "outbound message refused at the secret gate"
	reasonSendNotBound      = "thread's chat is not the current paired binding"
)

// errSendNotBound is Send's typed refusal when threadID's chat id is not
// the bridge's current paired binding (K3, round 2): a thread id a local
// operator minted for a chat this bridge was never paired to, or a chat
// whose pairing has since been revoked, must never receive a message.
var errSendNotBound = cascade.New(cascade.KindPolicyDenied,
	"cascade-pa/telegram: Send refused; the thread's chat is not the bridge's current paired binding")

// Send delivers body into the chat threadID maps to, at threadID's own
// resolved tier — never a caller-declared one. Windows tier-2 refuses
// before any IO (R-16.60a, matching Start); an unmapped threadID refuses
// by name (a caller error, not a policy decision — no divergence event).
// From there, in order: the binding gate (K3, matching the inbound path's
// OWN "S-48.T1 binding gate runs ahead of privacy" precedent), the real
// egress Guard's tier decision (a nil-content probe, THE REAL DECISION per
// this file's header), then the secret gate. Any of the three refuses
// typed AND records the same bridge.refused event handleInbound's refuse()
// emits; a transport failure past that point (client.SendMessage's own
// doer.Do) is returned as-is, never counted as a refusal (K5).
func (b *TelegramBridge) Send(ctx context.Context, threadID string, body []byte) error {
	// K6 (round 2): Send is now counted in-flight, so Drain (chat.go) —
	// documented as waiting on "every in-flight Send/inbound-handling
	// call" — actually waits on a Send too, not only handleInbound.
	b.inFlight.Add(1)
	defer b.inFlight.Add(-1)
	if refusal := platformBridgeRefusal(); refusal != nil {
		return refusal
	}
	chatID, ok := chatIDForThread(threadID)
	if !ok {
		return cascade.Newf(cascade.KindNotFound,
			"cascade-pa/telegram: thread %q does not map to a chat this bridge serves", threadID)
	}
	tier, perr := b.privacy.ThreadPrivacy(ctx, threadID)
	if perr != nil {
		tier = cascadepa.TierLocalOnly
	}
	corrID := "send:" + strconv.FormatInt(chatID, 10)

	bound, berr := b.binding.IsAllowed(ctx, b.subject, strconv.FormatInt(chatID, 10))
	if berr != nil || !bound {
		b.refuseSend(ctx, threadID, tier, corrID, reasonSendNotBound)
		return errSendNotBound
	}
	if _, err := b.module.client.egress.Guard(ctx, tier, nil); err != nil {
		b.refuseSend(ctx, threadID, tier, corrID, refusalReasonForTier(tier))
		return err
	}
	text, refused := b.module.guardOutboundChecked(ctx, string(body), "")
	if refused {
		b.refuseSend(ctx, threadID, tier, corrID, reasonSendSecretRefused)
		return errSendBodyRefused
	}
	return b.module.client.SendMessage(ctx, chatID, tier, text)
}

// refuseSend records and reports a Send-side POLICY refusal: the outbound
// twin of refuse() (chat.go, handleInbound's inbound refusal) — same
// RefusalRecord shape, same bridge.refused divergence event, content-free.
// Never called for a transport failure (K5) — only for a gate this file's
// header lists.
func (b *TelegramBridge) refuseSend(ctx context.Context, threadID string,
	tier cascadepa.SensitivityTier, corrID, reason string) {
	at := b.module.now()
	b.recordRefusal(cascadepa.RefusalRecord{
		ThreadID: threadID, ResolvedTier: tier.String(), Reason: reason, CorrelationID: corrID, At: at,
	})
	b.divergence.EmitRefused(ctx, RefusalEvent{
		ThreadID: threadID, ResolvedTier: tier.String(), Reason: reason, CorrelationID: corrID, At: at,
	})
}
