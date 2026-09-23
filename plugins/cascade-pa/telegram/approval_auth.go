// Purpose (this file): STEP 0 of the R-21.210 inline-button approval order
//   — sender authorization — and the typed event a rejection at this step
//   emits. Split from approval.go under Art.10.3's 300-line cap.
//
// Inputs: the bridge subject, the Telegram user id the live callback_query
//   carries (callback_query.from.id), and the host's cascadepa.BindingStore.
//
// Outputs: authorizeApprovalSender's allow/deny answer; ApprovalUnauthorizedEvent,
//   published exactly once per rejection at this step.
//
// Constraints (R-21.210 §G.2, binding): callback_query.from.id MUST equal a
//   Telegram user id recorded on the CURRENT W/S-48.T1 paired-device
//   binding. A mismatch — a non-owner tap, a group-chat tap from a
//   non-allowlisted member, or a callback arriving after a re-pairing
//   changed the subject — rejects BEFORE any nonce resolution or GetPending
//   lookup, with the typed event below. This is deliberately the SAME
//   check dispatchCallback's own admission gate already runs
//   (m.binding.IsAllowed): running it again here, as the approval flow's
//   own STEP 0, is what lets a unit test exercise "non-owner tap" and
//   "group-chat tap" in isolation (calling the approval Handler directly,
//   the way TestCallbackNonOwnerRejected/TestCallbackGroupChatRejected do)
//   and what gives a rejection at this specific step its own typed event
//   and its own actionable reply, distinct from the module's generic
//   "not paired".
//
// SPORT: plugins/cascade-pa/telegram authorizeApprovalSender/ADDED,
//   ApprovalUnauthorizedEvent/ADDED, ApprovalEventSink/ADDED
//   (P1-E23-W5-S48-T4).

package telegram

import (
	"context"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalUnauthorizedKind is the ONE event kind this file publishes.
const ApprovalUnauthorizedKind = "bridge.approval.unauthorized"

// replyApprovalUnauthorized is STEP 0's fixed, value-free rejection: no
// subject, no sender id, no chat reference — only the fact that this
// Telegram account may not decide this approval.
const replyApprovalUnauthorized = "not authorized to decide this approval"

// ErrApprovalSenderUnauthorized is STEP 0's typed refusal.
var ErrApprovalSenderUnauthorized = cascade.New(cascade.KindPermissionDenied,
	"cascade-pa/telegram: this Telegram account is not authorized to decide this approval")

// ApprovalUnauthorizedEvent is STEP 0's rejection record: enough to
// correlate in an operator's own audit trail, and nothing a sender chose
// (no button data, no request id — a non-owner's tap never gets far enough
// to name one).
type ApprovalUnauthorizedEvent struct {
	// Subject is the bridge instance the tap arrived on.
	Subject string `json:"subject"`
	// SenderID is the Telegram user id that tapped, unauthorized.
	SenderID string `json:"sender_id"`
	// ChatKind is the Telegram chat kind ("private", "group", ...).
	ChatKind string `json:"chat_kind"`
	// At is the rejection's timestamp, from the caller's injected clock.
	At time.Time `json:"at"`
}

// ApprovalEventSink receives one ApprovalUnauthorizedEvent per STEP 0
// rejection. The host wires a real one over the C/S-04.T3 bus, mirroring
// QuarantineSink's own translation (internal/plugins/cascadepa_bridge_events.go).
type ApprovalEventSink interface {
	EmitApprovalUnauthorized(ctx context.Context, e ApprovalUnauthorizedEvent)
}

// authorizeApprovalSender is STEP 0: senderID must be on subject's CURRENT
// paired-device allowlist. A store error and "not allowed" answer
// identically (false, nil) — the caller cannot distinguish an unreadable
// store from a stranger, which is what keeps this fail-closed rather than
// an oracle for "does this bridge exist".
func authorizeApprovalSender(ctx context.Context, binding *cascadepa.BindingStore, subject, senderID string) bool {
	if binding == nil {
		return false
	}
	allowed, err := binding.IsAllowed(ctx, subject, senderID)
	if err != nil {
		return false
	}
	return allowed
}

// emitApprovalUnauthorized publishes one ApprovalUnauthorizedEvent, or does
// nothing when no sink is wired — mirroring publishQuarantine's own
// "a publish failure never changes a decision" property: STEP 0's
// rejection already happened before this runs.
func emitApprovalUnauthorized(ctx context.Context, sink ApprovalEventSink, clock cascadepa.PairClock, subject, senderID, chatKind string) {
	if sink == nil || clock == nil {
		return
	}
	sink.EmitApprovalUnauthorized(context.WithoutCancel(ctx), ApprovalUnauthorizedEvent{
		Subject: subject, SenderID: senderID, ChatKind: chatKind, At: clock.Now(),
	})
}

// Answer exposes refuse.go's guarded answerCallbackQuery path to a Handler
// registered from outside module.go — this file's NewApprovalHandler is
// the first caller. It is the SAME gated path dispatchCallback's own
// replies use; there is no second, ungated way to answer a callback in
// this package.
func (m *TelegramModule) Answer(ctx context.Context, callbackID, chatKind, text string) {
	m.answer(ctx, callbackID, chatKind, text)
}
