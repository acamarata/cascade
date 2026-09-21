package telegram

// Purpose (this file): the decode boundary's tests — the real-API-shaped
//   fixtures decoded through the PRODUCTION types (never a test-local struct),
//   the envelope's terminal/transient mapping, and the fail-closed handling of
//   every Update kind this module does not dispatch.
//
// SPORT: plugins/cascade-pa/telegram wire-tests/TEST (P1-E23-W5-S48-T1).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// decodeFixture decodes one getUpdates fixture through the production
// envelope decoder and the production Update type.
func decodeFixture(t *testing.T, name string) []Update {
	t.Helper()
	var out []Update
	if err := decodeEnvelope(mustReadTestdata(t, name), &out); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return out
}

func TestFixture_TextMessageDecodes(t *testing.T) {
	got := decodeFixture(t, "getupdates_text.json")
	if len(got) != 1 {
		t.Fatalf("decoded %d updates, want 1", len(got))
	}
	u := got[0]
	if u.Message == nil || u.Message.From == nil {
		t.Fatalf("decoded %+v, want a message with a from", u)
	}
	if u.Message.From.ID != 555000111 || u.Message.Chat.ID != 555000111 {
		t.Fatalf("ids = from %d chat %d", u.Message.From.ID, u.Message.Chat.ID)
	}
	if u.Message.HasRefusedMedia() {
		t.Fatal("a plain text message reported media")
	}
}

func TestFixture_PairCommandDecodes(t *testing.T) {
	got := decodeFixture(t, "getupdates_pair_cmd.json")
	if len(got) != 1 || got[0].Message == nil {
		t.Fatalf("decoded %+v", got)
	}
	code, ok := isPairCommand(got[0].Message.Text)
	if !ok || code != "7ZQK3M9F" {
		t.Fatalf("isPairCommand(%q) = (%q, %v)", got[0].Message.Text, code, ok)
	}
}

// TestFixture_CallbackQueryDecodes closes the Art.2 gap the review named: the
// callback path was previously driven only from hand-built Go structs, so no
// JSON ever validated those field names.
func TestFixture_CallbackQueryDecodes(t *testing.T) {
	got := decodeFixture(t, "getupdates_callback_query.json")
	if len(got) != 1 {
		t.Fatalf("decoded %d updates, want 1", len(got))
	}
	cq := got[0].CallbackQuery
	if cq == nil {
		t.Fatalf("decoded %+v, want a callback_query", got[0])
	}
	if cq.ID != "4382bfdwdsb323b2d9" || cq.From.ID != 555000111 || cq.Data != "approve:req-7f3a" {
		t.Fatalf("callback = %+v", cq)
	}
	if cq.Message == nil || cq.Message.MessageID != 44 {
		t.Fatalf("callback message = %+v", cq.Message)
	}
	if got[0].Message != nil {
		t.Fatal("a callback_query update also decoded a top-level message")
	}
}

// TestFixture_InaccessibleMessageDecodes covers Bot API >=7.0: a callback whose
// original message is no longer reachable arrives as an InaccessibleMessage
// (date 0, no text). It must decode, not error, and must carry no media.
func TestFixture_InaccessibleMessageDecodes(t *testing.T) {
	got := decodeFixture(t, "getupdates_callback_inaccessible.json")
	if len(got) != 1 || got[0].CallbackQuery == nil {
		t.Fatalf("decoded %+v", got)
	}
	msg := got[0].CallbackQuery.Message
	if msg == nil {
		t.Fatal("the inaccessible message decoded as nil; the callback lost its chat context")
	}
	if msg.Date != 0 {
		t.Fatalf("date = %d, want 0 for an InaccessibleMessage", msg.Date)
	}
	if msg.Text != "" || msg.HasRefusedMedia() {
		t.Fatalf("an inaccessible message carried content: %+v", msg)
	}
}

// TestUpdate_UndispatchedKindsDecodeToNothing is the fail-closed proof for
// every Update kind P1 does not handle: edited_message and channel_post decode
// with both pointers nil, so dispatch's switch falls through.
func TestUpdate_UndispatchedKindsDecodeToNothing(t *testing.T) {
	for _, raw := range []string{
		`{"update_id":1,"edited_message":{"message_id":1,"chat":{"id":1},"text":"edited"}}`,
		`{"update_id":2,"channel_post":{"message_id":2,"chat":{"id":2},"text":"posted"}}`,
		`{"update_id":3,"my_chat_member":{"chat":{"id":3}}}`,
	} {
		var u Update
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatalf("decoding %s: %v", raw, err)
		}
		if u.Message != nil || u.CallbackQuery != nil {
			t.Fatalf("%s decoded into a dispatchable shape: %+v", raw, u)
		}
	}
}

func TestDecodeEnvelope_TerminalAndTransientRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      string
		terminal bool
		kind     cascade.Kind
	}{
		{"401", `{"ok":false,"error_code":401,"description":"Unauthorized"}`, true, cascade.KindPolicyDenied},
		{"409", `{"ok":false,"error_code":409,"description":"Conflict"}`, true, cascade.KindConflict},
		{"429", `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, false, cascade.KindUnavailable},
		{"500", `{"ok":false,"error_code":500,"description":"Internal"}`, false, cascade.KindUnavailable},
		{"no code", `{"ok":false}`, false, cascade.KindUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out []Update
			err := decodeEnvelope([]byte(tc.raw), &out)
			if err == nil {
				t.Fatal("ok:false decoded without an error")
			}
			if !cascade.HasKind(err, tc.kind) {
				t.Fatalf("error %v does not carry kind %v", err, tc.kind)
			}
			if got := terminalPollError(err); got != tc.terminal {
				t.Fatalf("terminalPollError = %v, want %v for %v", got, tc.terminal, err)
			}
		})
	}
}

// TestDecodeEnvelope_NeverReflectsTheServerDescription: Telegram's description
// is remote text and has no business in a local error string.
func TestDecodeEnvelope_NeverReflectsTheServerDescription(t *testing.T) {
	const planted = "PLANTED-REMOTE-TEXT-9f2a"
	var out []Update
	err := decodeEnvelope([]byte(`{"ok":false,"error_code":500,"description":"`+planted+`"}`), &out)
	if err == nil {
		t.Fatal("ok:false decoded without an error")
	}
	if strings.Contains(err.Error(), planted) {
		t.Fatalf("the server's own description text reached the error: %q", err.Error())
	}
}

func TestDecodeEnvelope_MalformedInputNeverPanics(t *testing.T) {
	for _, raw := range []string{`{not json`, ``, `{`, `[]`, `{"ok":true,"result":"not-a-list"}`} {
		var out []Update
		if err := decodeEnvelope([]byte(raw), &out); err == nil && raw != `` {
			t.Fatalf("decodeEnvelope(%q) returned nil; want an error", raw)
		}
	}
}

func TestDecodeEnvelope_NilOutAndEmptyResultAreFine(t *testing.T) {
	if err := decodeEnvelope([]byte(`{"ok":true}`), nil); err != nil {
		t.Fatalf("nil out: %v", err)
	}
	var out []Update
	if err := decodeEnvelope([]byte(`{"ok":true}`), &out); err != nil {
		t.Fatalf("empty result: %v", err)
	}
}

// TestTerminalPollError_IsNotKindOnly is the errors.Is trap this tree has hit
// before: pkg/cascade compares the KIND only, so any KindPolicyDenied error
// would satisfy errors.Is(err, errTokenRejected). The predicate must require
// the message too, or an egress refusal would stop the poll loop as if the
// token had been revoked.
func TestTerminalPollError_IsNotKindOnly(t *testing.T) {
	impostor := cascade.New(cascade.KindPolicyDenied, "egress: class refused tier restricted")
	if terminalPollError(impostor) {
		t.Fatal("an unrelated KindPolicyDenied error was treated as a revoked token")
	}
	conflict := cascade.New(cascade.KindConflict, "cascade-pa/telegram: module already started")
	if terminalPollError(conflict) {
		t.Fatal("an unrelated KindConflict error was treated as a polling conflict")
	}
	if terminalPollError(nil) {
		t.Fatal("nil was treated as terminal")
	}
	if !terminalPollError(errTokenRejected) || !terminalPollError(errPollConflict) {
		t.Fatal("a real terminal refusal was not recognised")
	}
}

func TestMessage_HasRefusedMedia(t *testing.T) {
	if (Message{Text: "hello"}).HasRefusedMedia() {
		t.Fatal("plain text reported media")
	}
	for name, m := range map[string]Message{
		"photo":    {Photo: []json.RawMessage{[]byte(`{}`)}},
		"document": {Document: []byte(`{}`)},
		"voice":    {Voice: []byte(`{}`)},
		"video":    {Video: []byte(`{}`)},
		"sticker":  {Sticker: []byte(`{}`)},
		"location": {Location: []byte(`{}`)},
	} {
		if !m.HasRefusedMedia() {
			t.Fatalf("a %s message did not report media", name)
		}
	}
}
