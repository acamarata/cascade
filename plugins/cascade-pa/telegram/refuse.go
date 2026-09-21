// Purpose (this file): the R-21.203/R-21.105 refusal gate — the ONE call
//   site both bridge transports (dispatchMessage's text, dispatchCallback's
//   callback data) and both outbound senders (reply, answer) use to keep
//   credential material off the bridge in either direction.
//
// Inputs: raw bridge text — an inbound Message.Text/CallbackQuery.Data, or
//   the text this module is about to send.
//
// Outputs: a refusal decision, plus the ONE value-free reply every refusal
//   this file produces (replyBridgeSecretRefused): no vault key, no
//   namespace value, no matched substring — only the local-CLI remediation
//   instruction.
//
// Constraints, and why the shape is this and not a direct detector import:
//   THIS PACKAGE NEVER IMPORTS internal/ (R-14.69, Art.10.2,
//   internal/build/arch_test.go's plugins-providers-import-pkg-only gate —
//   S-48.T2's own full_desc draws the identical boundary for the egress
//   class: "cascade-pa NEVER imports internal/"). SecretScanner is
//   therefore a pkg-facing seam over the H/S-16 credential-value detector
//   (internal/secrets.Detector.ScanCertain), matching EgressGate's
//   identical shape in client.go: this package holds no detection logic of
//   its own (LANE-RULES §5 — never re-derive the check with a local
//   regex). Scan takes no error: the real seam (Detector.ScanCertain) has
//   none either, so a bool-plus-safe-string outcome is the only shape a
//   real adapter (internal/plugins/cascadepa_bridge_deps.go's
//   bridgeSecretScanner) ever has to translate into — a fabricated error
//   branch with no reachable producer was the earlier draft's defect
//   (T0 D2, 2026-09-21 CR).
//
//   THE UNWIRED DEFAULT REFUSES — mirroring unconfiguredEgressGate
//   (client.go): a missing capability is refused, never admitted (T0 D1).
//   Unlike a real detection (which IS quarantined), a refusal caused by a
//   missing scanner publishes NO quarantine event: ErrNoSecretScanner
//   marks that state so refuseInboundText/refuseInboundCallback/
//   guardOutbound can tell "nothing wired" from "the wired scanner found
//   something" — only the second is ever recorded, since the first has no
//   credential class to record and recording one would fabricate a
//   detection that never happened. The host composition root
//   (internal/plugins/cascadepa_bridge_wiring.go) always wires the real
//   detector, so this default is a defensive floor, not the production
//   path.
//
// SPORT: plugins/cascade-pa/telegram SecretScanner/ADDED,
//   refusesSecret/ADDED, guardOutbound/ADDED (P1-E23-W5-S48-T3).

package telegram

import (
	"context"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SecretScanner is the pkg-facing seam over the H/S-16 credential-value
// detector. The host composition root binds the real *secrets.Detector
// behind it through an adapter (the typed internal/secrets.Class may never
// cross into this package); this package never holds one itself.
type SecretScanner interface {
	// Scan reports whether content is credential-shaped at the detector's
	// configured confidence threshold, and the credential CLASS matched, as
	// a plain string (safe to log: metadata about a span, never its bytes,
	// offset or length).
	Scan(content []byte) SecretScanOutcome
}

// SecretScanOutcome is one scan's safe-to-log result.
type SecretScanOutcome struct {
	// Detected reports whether content is credential-shaped.
	Detected bool
	// Class names the credential class matched (e.g. "api-key",
	// "high-entropy"), empty when Detected is false or the class is
	// unknown.
	Class string
}

// refusingSecretScanner is TelegramModule's default when no capability is
// wired: every scan reports Detected=true, mirroring unconfiguredEgressGate
// (client.go)'s fail-closed shape exactly — a missing capability refuses,
// it never admits.
type refusingSecretScanner struct{}

func (refusingSecretScanner) Scan([]byte) SecretScanOutcome {
	return SecretScanOutcome{Detected: true}
}

// scanner returns the module's configured SecretScanner, or the refusing
// default when none was wired.
func (m *TelegramModule) scanner() SecretScanner {
	if m.secretScanner == nil {
		return refusingSecretScanner{}
	}
	return m.secretScanner
}

// ErrNoSecretScanner is the fail-closed reason a message is refused when no
// SecretScanner is wired — a missing capability, not a detection. Callers
// use its presence to decide NOT to publish a quarantine event: there is
// nothing to record, only an unconfigured capability, and recording one
// would fabricate a detection that never happened.
var ErrNoSecretScanner = cascade.New(cascade.KindPolicyDenied,
	"cascade-pa/telegram: no bridge secret scanner is wired; every message refuses")

// replyBridgeSecretRefused is the ONE reply every refusal in this file
// produces, inbound or outbound. It is value-free by construction: fixed
// text with no interpolation, so it can never carry a key name, a
// namespace value or a matched substring — the same "fixed text, never an
// oracle" property module.go's other replies hold.
const replyBridgeSecretRefused = "message refused: it looks like it may contain credential material. " +
	"finish this locally with `cascade vault set`, or use the local approval surface."

// refusesSecret runs the scanner over raw and reports the refusal decision
// and the outcome, plus — for the quarantine writer — the reason this
// refusal is NOT a real detection: ErrNoSecretScanner when no scanner is
// wired, nil when a real scan ran (whether or not it detected anything).
func (m *TelegramModule) refusesSecret(raw string) (refused bool, outcome SecretScanOutcome, unconfigured error) {
	if m.secretScanner == nil {
		unconfigured = ErrNoSecretScanner
	}
	outcome = m.scanner().Scan([]byte(raw))
	return outcome.Detected, outcome, unconfigured
}

// refuseInboundText handles a secret-shaped text message (dispatchMessage
// calls this FIRST, before any pairing or binding lookup — R-21.203/D4):
// nothing about the message — not its content, not a hash, not a
// fingerprint — is stored or reaches a handler. The sender gets the
// value-free warning always; a quarantine event publishes only when
// unconfigured is nil (a real detection, not a missing capability).
func (m *TelegramModule) refuseInboundText(ctx context.Context, msg *Message, outcome SecretScanOutcome, unconfigured error) {
	if unconfigured == nil {
		// A publish failure never changes this refusal (quarantine.go's
		// publishQuarantine doc): the error is deliberately discarded.
		_ = m.publishQuarantine(ctx, quarantineOriginInboundText, chatKindOf(msg), outcome)
	}
	m.reply(ctx, msg.Chat.ID, chatKindOf(msg), replyBridgeSecretRefused)
}

// refuseInboundCallback is refuseInboundText's callback-transport twin —
// the "ONE GATE, TWO TRANSPORTS" rule this package's module.go applies to
// every other content gate, extended to this one.
func (m *TelegramModule) refuseInboundCallback(ctx context.Context, cq *CallbackQuery, outcome SecretScanOutcome, unconfigured error) {
	if unconfigured == nil {
		_ = m.publishQuarantine(ctx, quarantineOriginInboundCallback, chatKindOf(cq.Message), outcome)
	}
	m.answer(ctx, cq.ID, chatKindOf(cq.Message), replyBridgeSecretRefused)
}

// guardOutbound is the SEND-TIME gate reply and answer both apply before
// any outbound write (R-21.203's OUTBOUND REFUSAL): it re-scans text and,
// on a secret-shaped result, substitutes the value-free warning and
// publishes a quarantine event (when a real scanner is wired) rather than
// letting the caller's text reach the transport. chatKind is the caller's
// best available context for the quarantine record (R-21.105/D9).
//
// The equality check is not an optimization: without it, the unconfigured
// default (which flags EVERY text, D1) would re-flag
// replyBridgeSecretRefused itself when refuseInboundText/
// refuseInboundCallback send it, attempting a second, spurious quarantine
// publish for a message that was never sent to begin with.
func (m *TelegramModule) guardOutbound(ctx context.Context, text, chatKind string) string {
	if text == replyBridgeSecretRefused {
		return text
	}
	refused, outcome, unconfigured := m.refusesSecret(text)
	if !refused {
		return text
	}
	if unconfigured == nil {
		_ = m.publishQuarantine(ctx, quarantineOriginOutbound, chatKind, outcome)
	}
	return replyBridgeSecretRefused
}

// reply sends one operational message. Every module-generated reply is
// declared TierInternal and still crosses the firewall, which is what
// redacts a stored secret that reached a reply string by any route.
//
// guardOutbound runs FIRST (R-21.203's OUTBOUND REFUSAL): a
// credential-shaped reply is refused and replaced before the firewall, let
// alone the transport, ever sees it — the bridge is never a vault read
// path in either direction. chatKind is the caller's best context for a
// quarantine record this call might produce: every call site in this
// package passes the Message/CallbackQuery's own chat kind when one is
// available, and "" only where none is (pairing.go's say, whose fixed-text
// replies are never credential-shaped in the first place).
func (m *TelegramModule) reply(ctx context.Context, chatID int64, chatKind, text string) {
	text = m.guardOutbound(ctx, text, chatKind)
	_ = m.client.SendMessage(ctx, chatID, cascadepa.TierInternal, text)
}

// answer answers one callback, gated identically to reply.
func (m *TelegramModule) answer(ctx context.Context, callbackID, chatKind, text string) {
	text = m.guardOutbound(ctx, text, chatKind)
	_ = m.client.AnswerCallbackQuery(ctx, callbackID, cascadepa.TierInternal, text)
}

// chatKindOf reports msg's chat kind ("private", "group", "supergroup",
// "channel"), or "unknown" for a nil Message — mirroring
// internal/plugins/cascadepa_bridge_events.go's correlationOf: a record
// never looks like it lost a value it never had.
func chatKindOf(msg *Message) string {
	if msg == nil {
		return "unknown"
	}
	return msg.Chat.Type
}
