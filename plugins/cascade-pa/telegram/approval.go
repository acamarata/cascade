// Purpose (this file): the Telegram inline-button signed approval flow
//   (§5.24, R-21.210, R-21.230): validateCallbackQuery (the real Bot API
//   callback_query decode-and-validate path, Art.2 — approval_fuzz_test.go's
//   FuzzParseCallbackQuery is the raw-bytes decoder), this module's own
//   opaque callback_data shape, and the ordered handler NewApprovalHandler
//   registers on HandlerCallbackQuery.
//
// Inputs: a decoded callback_query, the R-21.210 server-side callback
//   nonce W/S-48.T1 stores, and the host-injected cascadepa.ApprovalService.
//
// Outputs: NewApprovalHandler builds a Handler that either dispatches a
//   verified decision (approval.grant on an approve tap, approval.deny on
//   a reject tap) or rejects — fail-closed at every step.
//
// Constraints, and the order (R-21.210, then R-21.230 where it overrides):
//   0. sender authorization (approval_auth.go) — BEFORE anything below;
//   0.4. the callback must carry a message envelope — an absent one (Bot
//      API >= 7.0's inaccessible_message case) refuses before the nonce
//      resolves, rather than degrading to an empty-string chat/message
//      that could match an empty-bound nonce (rework 6);
//   0.5. nonce resolve + ATOMIC consume, MATCHING BEFORE DELETING
//      (callback.go, T0 D1 item 7): a mismatch refuses and leaves the
//      record for a correct retry; only a match consumes it;
//   1-2. GetPending + RemoteApprovabilityMatrix.CanBridge, evaluated
//      SERVER-SIDE (deps.Approvals.ShowPending) — no classifier of its
//      own (R-16.60c);
//   3. redemption: approval.grant/deny, called with request_id ALONE —
//      the signed token is loaded and submitted on the host side
//      (R-21.230). WHICH verb is called comes from the CONSUMED nonce
//      record's own AllowedVerdict, never from the wire.
//   CALLBACK DATA IS "<request_id>|<nonce>" — R-21.210's WIRE CONTRACT
//   VERBATIM, at most 64 bytes (Telegram's own ceiling; parseApprovalData
//   refuses anything longer). T0 D1 superseded the original four-field
//   pipe format (verdict word + action digest also on the wire): an
//   adversarial CR found both load-bearing nowhere — the digest was
//   checked against a value the same untrusted payload supplied, and the
//   verdict was redundant with the nonce's own AllowedVerdict. A future
//   producer mints one nonce per button (one AllowedVerdict each), so the
//   button's own meaning never has to travel.
//   ERRINVALIDSIGNATURE REVEALS NOTHING (R-21.230): mapGrantRefusal maps a
//   signature failure to the SAME fixed generic reply an unresolvable
//   request id gets. ErrTokenExpired/ErrTokenReplayed keep distinct
//   replies, matched by MESSAGE TEXT over the real strings both refusal
//   layers emit — never taxonomy Kind, which differs between the verifier
//   layer (rpc_grant.go) and the redemption layer (approval_queue_errors.go)
//   for the same word "expired" (lesson_errors_is_compares_kind_only.md; an
//   RPC round trip reconstructs a fresh error, so identity never survives it
//   — internal/client/codec.go). A missing attestation source (verbs.go's
//   real KindElevationRequired text, FLAG-2 fix) gets its own reply too.
// SPORT: plugins/cascade-pa/telegram validateCallbackQuery/ADDED,
//   NewApprovalHandler/ADDED (P1-E23-W5-S48-T4); callback_data shrunk to
//   "<request_id>|<nonce>", the 64-byte length gate, the nil-Message
//   refusal and mapGrantRefusal's real-string match/CHANGED (rework, T0 D1);
//   mapGrantRefusal's KindElevationRequired case/ADDED (narrow fix, FLAG-2).

package telegram

import (
	"context"
	"strconv"
	"strings"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxCallbackDataBytes is the Bot API's own ceiling on one callback_data
// payload; parseApprovalData refuses anything longer (rework fix 3).
const maxCallbackDataBytes = 64

// The subject-facing replies this file's flow produces. Every one is fixed
// text: interpolating a live value into any of these would make the bridge
// a verification oracle (R-21.230).
const (
	// replyApprovalRefused is the ONE fixed generic reply for an
	// unresolvable request id AND a signature failure alike (R-21.230):
	// it must be byte-identical in both cases.
	replyApprovalRefused   = "this approval could not be verified"
	replyApprovalExpired   = "this approval has expired"
	replyApprovalReplayed  = "this approval was already decided"
	replyApprovalForbidden = "this action cannot be decided over the bridge"
	replyApprovalGranted   = "approved"
	replyApprovalDenied    = "denied"
)

// validateCallbackQuery checks the fields this module's flow depends on.
// The poll loop's own decode (wire.go) never rejects a callback_query
// missing these — Update.CallbackQuery is a presence-only pointer field —
// so this handler's own entry point (NewApprovalHandler) re-checks them
// before anything else runs. FuzzParseCallbackQuery (approval_fuzz_test.go)
// decodes raw bytes and runs this same check — the real Bot API
// decode-and-validate path, Art.2.
func validateCallbackQuery(cq *CallbackQuery) error {
	if cq.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "cascade-pa/telegram: callback_query has no id")
	}
	if cq.From.ID == 0 {
		return cascade.New(cascade.KindInvalidInput, "cascade-pa/telegram: callback_query has no from.id")
	}
	return nil
}

// approvalClaim is this module's own decoded callback_data: R-21.210's
// wire contract verbatim. No verdict field — it is read back from the
// CONSUMED nonce record, never claimed by the wire (see file header).
type approvalClaim struct {
	requestID string
	nonce     string
}

// parseApprovalData decodes the "<request_id>|<nonce>" shape this file's
// header documents. Every field is required: an absent one is exactly the
// "guessed request_id"/"forwarded button" shape R-21.210 requires refused
// — Consume's own field comparison decides those, this only extracts.
// Anything over Telegram's 64-byte callback_data ceiling refuses outright.
func parseApprovalData(data string) (approvalClaim, error) {
	if len(data) > maxCallbackDataBytes {
		return approvalClaim{}, cascade.New(cascade.KindInvalidInput,
			"cascade-pa/telegram: approval callback data exceeds Telegram's 64-byte callback_data limit")
	}
	parts := strings.Split(data, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return approvalClaim{}, cascade.New(cascade.KindInvalidInput,
			"cascade-pa/telegram: malformed approval callback data")
	}
	return approvalClaim{requestID: parts[0], nonce: parts[1]}, nil
}

// ApprovalHandlerDeps is everything NewApprovalHandler needs, all of it
// injected — mirroring NewTelegramModule's own explicit-dependency shape.
type ApprovalHandlerDeps struct {
	// Subject is this bridge instance's id (the BridgeInstance nonce
	// field and the BindingStore key).
	Subject string
	// Binding answers STEP 0 (approval_auth.go).
	Binding *cascadepa.BindingStore
	// Callbacks is the W/S-48.T1 server-side one-use nonce store.
	Callbacks *cascadepa.CallbackNonceStore
	// Approvals is the host-injected §5.24 redemption surface.
	Approvals cascadepa.ApprovalService
	// Events receives STEP 0 rejections. Optional: a nil sink does not
	// change the rejection, only whether it is recorded (mirrors
	// QuarantineSink's own "a publish failure never changes a decision").
	Events ApprovalEventSink
	// Clock times STEP 0's event and gates nonce expiry.
	Clock cascadepa.PairClock
	// Answer sends the guarded reply (module.Answer in production).
	Answer func(ctx context.Context, callbackID, chatKind, text string)
}

// NewApprovalHandler builds the Handler this ticket registers on
// HandlerCallbackQuery. deps.Answer is called exactly once on every path.
func NewApprovalHandler(deps ApprovalHandlerDeps) Handler {
	return func(ctx context.Context, msg InboundMessage) error {
		cq := msg.Update.CallbackQuery
		if cq == nil {
			return nil
		}
		if err := validateCallbackQuery(cq); err != nil {
			return err
		}
		return handleApprovalCallback(ctx, deps, cq)
	}
}

// resolveApprovalNonce runs STEPS 0.4-0.5: refuse a callback with no
// message envelope (rework fix 6), then consume the nonce, matching
// BEFORE deleting (callback.go, T0 D1 item 7). Split out of
// handleApprovalCallback for Art.10.3's 50-line cap.
func resolveApprovalNonce(deps ApprovalHandlerDeps, cq *CallbackQuery,
	claim approvalClaim, senderID string) (cascadepa.CallbackNonce, error) {
	if cq.Message == nil {
		return cascadepa.CallbackNonce{}, cascade.New(cascade.KindInvalidInput,
			"cascade-pa/telegram: the approval callback carries no message envelope")
	}
	chatID, messageID := liveChatMessage(cq)
	nonceClaim := cascadepa.CallbackClaim{
		Nonce: claim.nonce, RequestID: claim.requestID,
		BridgeInstance: deps.Subject, PairedSubjectID: senderID,
		ChatID: chatID, MessageID: messageID,
	}
	if deps.Callbacks == nil {
		return cascadepa.CallbackNonce{}, cascade.New(cascade.KindUnavailable,
			"cascade-pa/telegram: no callback nonce store is wired")
	}
	nonce, ok := deps.Callbacks.Consume(nonceClaim, deps.Clock.Now())
	if !ok {
		return cascadepa.CallbackNonce{}, cascade.New(cascade.KindPermissionDenied,
			"cascade-pa/telegram: the approval callback did not verify")
	}
	return nonce, nil
}

// handleApprovalCallback runs the full R-21.210/R-21.230 order. Every
// branch answers deps.Answer exactly once before returning.
func handleApprovalCallback(ctx context.Context, deps ApprovalHandlerDeps, cq *CallbackQuery) error {
	chatKind := chatKindOf(cq.Message)
	senderID := strconv.FormatInt(cq.From.ID, 10)

	// STEP 0: sender authorization.
	if !authorizeApprovalSender(ctx, deps.Binding, deps.Subject, senderID) {
		emitApprovalUnauthorized(ctx, deps.Events, deps.Clock, deps.Subject, senderID, chatKind)
		deps.Answer(ctx, cq.ID, chatKind, replyApprovalUnauthorized)
		return ErrApprovalSenderUnauthorized
	}

	claim, err := parseApprovalData(cq.Data)
	if err != nil {
		deps.Answer(ctx, cq.ID, chatKind, replyApprovalRefused)
		return err
	}

	// STEPS 0.4-0.5: message-envelope presence, then nonce resolve +
	// atomic consume.
	nonce, err := resolveApprovalNonce(deps, cq, claim, senderID)
	if err != nil {
		deps.Answer(ctx, cq.ID, chatKind, replyApprovalRefused)
		return err
	}

	// STEP 1: server-side lookup. An unresolvable request id gets the SAME
	// fixed generic reply a forged signature does (R-21.230's
	// byte-identical requirement) — the bridge is never an oracle for
	// "does this request exist".
	approvals := cascadepa.ApprovalServiceOrRefuseAll(deps.Approvals)
	pending, err := approvals.ShowPending(ctx, claim.requestID)
	if err != nil {
		deps.Answer(ctx, cq.ID, chatKind, replyApprovalRefused)
		return cascade.New(cascade.KindNotFound, "cascade-pa/telegram: the approval could not be resolved")
	}

	// STEP 2: the host's own CanBridge verdict (R-16.60c) — its own,
	// distinct actionable reply: this is a real, named class of rejection,
	// not a verification oracle.
	if !pending.Bridgeable {
		deps.Answer(ctx, cq.ID, chatKind, replyApprovalForbidden)
		return cascade.New(cascade.KindPermissionDenied,
			"cascade-pa/telegram: this action cannot be decided over the bridge")
	}

	// STEP 3: redemption. The verdict comes from the CONSUMED record —
	// never from the wire, which carries none (T0 rework fix 1).
	return redeemApproval(ctx, deps, approvals, cq.ID, chatKind, claim.requestID, nonce.AllowedVerdict)
}

// redeemApproval calls approval.grant/deny with the request id alone —
// never the nonce, a digest or a token.
func redeemApproval(ctx context.Context, deps ApprovalHandlerDeps, approvals cascadepa.ApprovalService,
	callbackID, chatKind, requestID string, verdict bool) error {
	if verdict {
		if err := approvals.Grant(ctx, requestID); err != nil {
			deps.Answer(ctx, callbackID, chatKind, mapGrantRefusal(err))
			return err
		}
		deps.Answer(ctx, callbackID, chatKind, replyApprovalGranted)
		return nil
	}
	if err := approvals.Deny(ctx, requestID); err != nil {
		deps.Answer(ctx, callbackID, chatKind, replyApprovalRefused)
		return err
	}
	deps.Answer(ctx, callbackID, chatKind, replyApprovalDenied)
	return nil
}

// liveChatMessage reads the chat/message ids off the LIVE envelope — never
// off callback_data — so a forwarded button (same nonce, different
// chat/message) fails the nonce comparison on its own.
func liveChatMessage(cq *CallbackQuery) (chatID, messageID string) {
	if cq.Message == nil {
		return "", ""
	}
	return strconv.FormatInt(cq.Message.Chat.ID, 10), strconv.FormatInt(cq.Message.MessageID, 10)
}

// mapGrantRefusal maps an approval.grant refusal onto one of R-21.230's
// replies, by MESSAGE SUBSTRING alone (never Kind, and never a Kind-plus-
// substring conjunction — see file header): the verifier layer
// (rpc_grant.go's grantRefusal(ErrExpired)) answers KindPermissionDenied
// "...no longer valid"; the redemption layer (approval_queue_errors.go's
// ErrTokenExpired) answers KindPolicyDenied "...expired...". A missing
// attestation source gets its own reply too (FLAG-2); everything else
// unclassified falls through to the ONE fixed generic reply.
func mapGrantRefusal(err error) string {
	if err == nil {
		return replyApprovalRefused
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no longer valid") || strings.Contains(msg, "expired"):
		return replyApprovalExpired
	case strings.Contains(msg, "already been redeemed") || strings.Contains(msg, "already being redeemed"):
		return replyApprovalReplayed
	case strings.Contains(msg, "is an elevated verb and no attestation source is enrolled"):
		return replyApprovalForbidden
	default:
		return replyApprovalRefused
	}
}
