// Purpose (this file): the R-21.210/R-21.230 bridge PRODUCER leg
//   (P1-E23-W5-S48-T4, T0 D4) — the code that first sends an approval
//   reference off-machine. It mints one one-use W/S-48.T1 callback nonce
//   per inline button, sends the Telegram message carrying both buttons'
//   "<request_id>|<nonce>" callback_data (R-21.210's wire contract, the
//   same 64-byte ceiling approval.go's parseApprovalData enforces), and
//   stores each nonce bound to the message Telegram actually created —
//   never to the request the caller asked to send before that.
//
// Inputs: a request id (internal/policy/bridge_leg.go's BridgeRef has only
//   this field; this package cannot import internal/policy at all, Art.10.2
//   — the composition root's adapter, a sibling file in internal/plugins,
//   is what turns a policy.BridgeRef into the plain string Send below
//   takes), the paired-device binding (cascadepa.BridgeState — read
//   directly, since SubjectState.AllowedFrom is already exported and no new
//   BindingStore method is needed), the W/S-48.T1 nonce store, and a
//   BotClient.
//
// Outputs: TelegramApprovalSender, satisfying a plain request-id-string
//   Send; sendApprovalButtons, a new *BotClient method carrying no dist
//   change to client.go/wire.go — see its own comment.
//
// Constraints:
//   - PAIRING FIRST. An unpaired (or never-seen) subject gets no nonce
//     minted and no message sent — SubjectState.Bound() is read BEFORE
//     anything else runs, and Send returns nil: "no one to notify" is not a
//     failure.
//   - THE OUTBOUND TEXT NAMES NOTHING. No action summary, no expiry, no
//     request id in the message BODY (only in callback_data, which
//     Telegram never displays): the same "the bridge is never an oracle"
//     posture approval.go's fixed replies already keep on the inbound side,
//     extended to the outbound side — a producer that echoed the summary
//     here would be a second, independent leak path for exactly the
//     content §5.24 restricts crossing at all.
//   - MESSAGE ID COMES BACK, NEVER GUESSED. CallbackNonce.MessageID must
//     equal what the CONSUMER's liveChatMessage later reads off the real
//     callback (cq.Message.MessageID) — that value only exists after
//     Telegram assigns it, so a nonce's RANDOM VALUE is minted independent
//     of any message (mintNonce needs nothing but entropy) but the
//     CallbackNonce RECORD is stored only after the send response returns
//     the real chat/message ids. No circular dependency: the button's
//     callback_data only ever needs the nonce STRING, not the record.
//   - ONE MESSAGE, TWO NONCES, EACH ITS OWN AllowedVerdict: this file mints
//     a fresh nonce per button rather than reusing the R-21.210 draft's
//     single shared nonce+verdict-on-the-wire shape (superseded, see
//     callback.go's header) — the button's own meaning never has to
//     travel.
//   - A PARTIALLY-PAIRED SUBJECT IS NOT AN ALL-OR-NOTHING SEND: Send
//     iterates every currently allowlisted sender independently: one
//     recipient's malformed row or one failed HTTP call does not stop the
//     others from being notified.
//
// SPORT: plugins/cascade-pa/telegram TelegramApprovalSender/ADDED,
//   sendApprovalButtons/ADDED (P1-E23-W5-S48-T4 producer leg, T0 D4).

package telegram

import (
	"context"
	"strconv"
	"time"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	"github.com/acamarata/cascade/pkg/cascade"
)

// bridgeApprovalNonceTTL bounds a minted callback nonce's own lifetime.
// Declared locally rather than imported: this package cannot import
// internal/policy (Art.10.2), so it cannot read MaxApprovalTTL directly.
// Five minutes mirrors that ceiling by independent declaration; a nonce
// that outlived the approval it points at would only ever fail at
// GetPending, so the two only need to be in the same order, not identical.
const bridgeApprovalNonceTTL = 5 * time.Minute

// The fixed outbound strings this file ever sends: no summary, no expiry,
// no request id in the body — see this file's header.
const (
	approvalPromptText = "cascade: an action is awaiting your approval"
	approveButtonLabel = "Approve"
	denyButtonLabel    = "Deny"
)

// mintNonce returns a fresh, unguessable nonce value. cascade.NewID is
// pkg/cascade, not internal/, so this package's Art.10.2 boundary holds.
func mintNonce() (string, error) {
	id, err := cascade.NewID()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// TelegramApprovalSenderDeps is everything the producer leg needs, all
// injected — mirroring ApprovalHandlerDeps' own explicit-dependency shape.
//
// precedent (module.go): the package name IS the surface this type names.
//
//nolint:revive // deliberate stutter, mirroring TelegramModule's identical
type TelegramApprovalSenderDeps struct {
	// Subject is this bridge instance's id.
	Subject string
	// State answers the pairing question directly off the durable row.
	State cascadepa.BridgeState
	// Callbacks is the W/S-48.T1 server-side one-use nonce store.
	Callbacks *cascadepa.CallbackNonceStore
	// Client sends the real Bot API sendMessage call.
	Client *BotClient
	// Clock times nonce expiry.
	Clock cascadepa.PairClock
}

// TelegramApprovalSender adapts TelegramApprovalSenderDeps to a plain
// request-id-string call, so the internal/plugins composition root's
// policy.BridgeSender adapter (the only place allowed to import both
// internal/policy and this package) has nothing left to translate but a
// string.
//
// precedent (module.go): the package name IS the surface this type names.
//
//nolint:revive // deliberate stutter, mirroring TelegramModule's identical
type TelegramApprovalSender struct {
	deps TelegramApprovalSenderDeps
}

// NewTelegramApprovalSender builds a sender over deps.
func NewTelegramApprovalSender(deps TelegramApprovalSenderDeps) *TelegramApprovalSender {
	return &TelegramApprovalSender{deps: deps}
}

// Send delivers requestID's two-button approval prompt to every currently
// paired sender on deps.Subject. An unpaired (or unknown) subject gets
// nothing minted and nothing sent — nil, not a failure.
func (s *TelegramApprovalSender) Send(ctx context.Context, requestID string) error {
	st, ok, err := s.deps.State.Load(ctx, s.deps.Subject)
	if err != nil {
		return err
	}
	if !ok || !st.Bound() {
		return nil
	}
	var lastErr error
	sent := 0
	for _, senderID := range st.AllowedFrom {
		if serr := s.sendOne(ctx, requestID, senderID); serr != nil {
			lastErr = serr
			continue
		}
		sent++
	}
	if sent == 0 && lastErr != nil {
		return lastErr
	}
	return nil
}

// sendOne sends one recipient's message and stores its two nonces, bound to
// the REAL chat/message ids the send response returns.
func (s *TelegramApprovalSender) sendOne(ctx context.Context, requestID, senderID string) error {
	chatID, err := strconv.ParseInt(senderID, 10, 64)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err,
			"cascade-pa/telegram: paired sender id %q is not a Telegram chat id", senderID)
	}
	approveNonce, denyNonce, err := s.mintPair()
	if err != nil {
		return err
	}
	approveData, denyData := requestID+"|"+approveNonce, requestID+"|"+denyNonce
	if len(approveData) > maxCallbackDataBytes || len(denyData) > maxCallbackDataBytes {
		return cascade.New(cascade.KindInvalidInput,
			"cascade-pa/telegram: approval callback data exceeds Telegram's 64-byte callback_data limit")
	}
	msg, err := s.deps.Client.sendApprovalButtons(ctx, chatID, cascadepa.TierInternal, approvalPromptText, approveData, denyData)
	if err != nil {
		return err
	}
	return s.storePair(requestID, senderID, msg, approveNonce, denyNonce)
}

// mintPair mints the approve/deny nonce values for one recipient message.
func (s *TelegramApprovalSender) mintPair() (approveNonce, denyNonce string, err error) {
	if approveNonce, err = mintNonce(); err != nil {
		return "", "", err
	}
	if denyNonce, err = mintNonce(); err != nil {
		return "", "", err
	}
	return approveNonce, denyNonce, nil
}

// storePair records both CallbackNonce rows for one sent message, bound to
// the message Telegram actually created.
func (s *TelegramApprovalSender) storePair(requestID, senderID string, msg Message, approveNonce, denyNonce string) error {
	now := s.deps.Clock.Now()
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	messageID := strconv.FormatInt(msg.MessageID, 10)
	base := cascadepa.CallbackNonce{
		RequestID: requestID, BridgeInstance: s.deps.Subject, PairedSubjectID: senderID,
		ChatID: chatID, MessageID: messageID, ExpiresAt: now.Add(bridgeApprovalNonceTTL),
	}
	approve, deny := base, base
	approve.Nonce, approve.AllowedVerdict = approveNonce, true
	deny.Nonce, deny.AllowedVerdict = denyNonce, false
	if err := s.deps.Callbacks.Store(approve); err != nil {
		return err
	}
	return s.deps.Callbacks.Store(deny)
}

// inlineKeyboardButton/inlineKeyboardMarkup/sendApprovalButtonsParams mirror
// wire.go's sendMessageParams shape with reply_markup added — a superset
// request body for the SAME MethodSendMessage apicall.go's Doer allowlist
// already admits. No change to wire.go or apicall.go is needed for two
// extra buttons: Go permits any file in a package to add methods and types
// over another file's declarations.
type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type sendApprovalButtonsParams struct {
	ChatID      int64                `json:"chat_id"`
	Text        string               `json:"text"`
	ReplyMarkup inlineKeyboardMarkup `json:"reply_markup"`
}

// sendApprovalButtons posts text with two inline buttons to chatID, through
// the SAME egress-gated Guard-then-Doer path client.go's SendMessage uses —
// the real firewall, the real doer, the real sendMessage method — carrying
// reply_markup because approval buttons need it. It returns the Message
// Telegram actually created, so the caller binds a nonce to the REAL
// chat/message id rather than the one it merely asked for.
func (c *BotClient) sendApprovalButtons(ctx context.Context, chatID int64,
	tier cascadepa.SensitivityTier, text, approveData, denyData string) (Message, error) {
	safe, err := c.egress.Guard(ctx, tier, []byte(text))
	if err != nil {
		return Message{}, err
	}
	params := sendApprovalButtonsParams{
		ChatID: chatID,
		Text:   string(safe),
		ReplyMarkup: inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{{
			{Text: approveButtonLabel, CallbackData: approveData},
			{Text: denyButtonLabel, CallbackData: denyData},
		}}},
	}
	var sent Message
	if err := c.doer.Do(ctx, MethodSendMessage, params, &sent); err != nil {
		return Message{}, err
	}
	return sent, nil
}

// Client exposes the module's BotClient to producer wiring
// (P1-E23-W5-S48-T4): the composition root needs it to build a
// TelegramApprovalSender, and TelegramModule otherwise keeps it unexported.
func (m *TelegramModule) Client() *BotClient {
	return m.client
}
