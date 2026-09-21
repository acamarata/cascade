// Purpose: the Telegram Bot API wire shapes and the one decoder every
//   inbound byte crosses (Art.2 external contract —
//   core.telegram.org/bots/api #update #message #callbackquery #getupdates
//   #sendmessage #answercallbackquery), plus the three request bodies this
//   module is allowed to send.
//
// Inputs: raw getUpdates/sendMessage response bytes.
// Outputs: decoded Updates, or a typed refusal. decodeEnvelope maps
//   Telegram's own error_code onto the terminal/transient distinction the
//   poll loop needs: 401 (revoked token) and 409 (another poller) are
//   TERMINAL, everything else is transient and backed off.
//
// Constraints: NO getFile. There is no getFile method constant, no request
//   body for it and no URL builder that could reach it (R-21.227's "never
//   downloaded"). The media fields below are presence markers only: the
//   decoder records that a media field was there and the dispatch gate
//   refuses the update; no byte of the media itself is ever read.
//
// SPORT: plugins/cascade-pa/telegram Update/ADDED, Message/ADDED,
//   CallbackQuery/ADDED, decodeEnvelope/ADDED (P1-E23-W5-S48-T1).

package telegram

import (
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The three Bot API methods this module may call. The list is exhaustive
// and enforced at the transport (apicall.go's methodAllowed), which is what
// lets a test assert "no getFile call exists" and have that assertion be
// able to FAIL.
const (
	MethodGetUpdates          = "getUpdates"
	MethodSendMessage         = "sendMessage"
	MethodAnswerCallbackQuery = "answerCallbackQuery"
)

// User is the Bot API's User object (fields this module reads).
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// Chat is the Bot API's Chat object (fields this module reads).
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is the Bot API's Message object, narrowed to the fields this
// module reads or refuses on. It also decodes a Bot API >=7.0
// InaccessibleMessage (same shape with date 0 and no text), which is what a
// callback_query carries when the original message is no longer reachable.
type Message struct {
	MessageID int64             `json:"message_id"`
	From      *User             `json:"from,omitempty"`
	Chat      Chat              `json:"chat"`
	Date      int64             `json:"date"`
	Text      string            `json:"text,omitempty"`
	Photo     []json.RawMessage `json:"photo,omitempty"`
	Document  json.RawMessage   `json:"document,omitempty"`
	Voice     json.RawMessage   `json:"voice,omitempty"`
	Video     json.RawMessage   `json:"video,omitempty"`
	Sticker   json.RawMessage   `json:"sticker,omitempty"`
	Location  json.RawMessage   `json:"location,omitempty"`
}

// HasRefusedMedia reports whether m carries any of the media kinds
// R-21.227 requires refused without ever calling getFile.
func (m Message) HasRefusedMedia() bool {
	return len(m.Photo) > 0 || len(m.Document) > 0 || len(m.Voice) > 0 ||
		len(m.Video) > 0 || len(m.Sticker) > 0 || len(m.Location) > 0
}

// CallbackQuery is the Bot API's CallbackQuery object.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Update is one decoded getUpdates result entry. Exactly one of Message or
// CallbackQuery is populated for the two kinds this module dispatches in P1
// (§R-21.227 TEXT AND CALLBACK ONLY); every other kind (edited_message,
// channel_post, ...) decodes with both nil and is skipped by dispatch —
// fail-closed, and not a media refusal case.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// getUpdatesParams is getUpdates' request body. Timeout is seconds
// (Telegram's long-poll convention); AllowedUpdates restricts the server to
// the two kinds this module dispatches, so a media-only update usually
// never crosses the wire at all — the in-process HasRefusedMedia check is
// the enforced backstop for a server or fixture that sends one anyway.
type getUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

// sendMessageParams is sendMessage's request body.
type sendMessageParams struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// answerCallbackQueryParams is answerCallbackQuery's request body — the
// only way to answer a bare callback, which carries no chat to reply into.
type answerCallbackQueryParams struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

// apiEnvelope is the Bot API's {"ok":bool,"result":...} wrapper plus the
// error_code every non-ok reply carries.
type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
}

// The two TERMINAL API refusals. Both carry a FIXED message with no
// server-supplied text interpolated: Telegram's description field is
// attacker-influenceable in principle and has no business in a local error
// string, and the token appears in neither.
var (
	// errTokenRejected is HTTP 401: the bot token was revoked or is wrong.
	// Retrying cannot fix it, so the loop stops instead of polling forever.
	errTokenRejected = cascade.New(cascade.KindPolicyDenied,
		"cascade-pa/telegram: the Bot API rejected this token (401); polling stopped")
	// errPollConflict is HTTP 409: another process is long-polling the same
	// bot. Two pollers split updates unpredictably, so this one stops.
	errPollConflict = cascade.New(cascade.KindConflict,
		"cascade-pa/telegram: another process is already polling this bot (409); polling stopped")
)

// decodeEnvelope unwraps raw into out. It never panics on adversarial
// input and never reflects the server's own description text back into an
// error message.
func decodeEnvelope(raw []byte, out any) error {
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "cascade-pa/telegram: malformed API response")
	}
	if !env.OK {
		return apiRefusal(env.ErrorCode)
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "cascade-pa/telegram: malformed result payload")
	}
	return nil
}

// apiRefusal maps an error_code onto this package's typed refusal.
func apiRefusal(code int) error {
	switch code {
	case 401:
		return errTokenRejected
	case 409:
		return errPollConflict
	default:
		return cascade.Newf(cascade.KindUnavailable,
			"cascade-pa/telegram: the Bot API refused the call (error_code %d)", code)
	}
}

// terminalPollError reports whether err is one of the two refusals that
// must STOP the poll loop rather than be backed off.
//
// It matches on the sentinel's message text as well as its kind, because
// pkg/cascade's Is compares the KIND ONLY: errors.Is(err, errTokenRejected)
// is true for any KindPolicyDenied error in the tree, including an egress
// refusal that has nothing to do with the token. Requiring the message too
// is what makes this predicate mean what it says.
func terminalPollError(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range []*cascade.Error{errTokenRejected, errPollConflict} {
		kind, ok := cascade.KindOf(sentinel)
		if ok && cascade.HasKind(err, kind) && strings.Contains(err.Error(), sentinel.Error()) {
			return true
		}
	}
	return false
}
