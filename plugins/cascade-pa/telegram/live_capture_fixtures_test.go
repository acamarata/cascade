//go:build integration

// Purpose (this file): live_capture_test.go's own write half, split out to
//   stay under Art.10.3's 300-line cap — redacts each captured update's
//   identifying numbers to fixed synthetic placeholders, writes the four
//   testdata/getupdates_live_*.json fixtures, and appends the
//   testdata/README.md provenance entry TestTelegramLiveCapturedFixturesDecode's
//   own README grep checks for.
//
// ROUND-2 REWRITE (fix item 3): redaction now walks the RAW JSON bytes
//   (map[string]any), never a re-marshaled Update struct. The prior
//   struct-based version silently dropped every wire field this package's
//   own Update type does not declare, which made the "decode proof"
//   incapable of ever catching real wire drift — exactly the risk a live
//   capture exists to close. Every field this redaction does not
//   explicitly overwrite survives byte-for-byte from the real response.
//
// Constraints: the placeholders below (7000002xx/7000003xx/5xx/900100xxx)
//   are chosen to be visibly distinct from every OTHER fixture in this
//   directory's existing synthetic ids (555000111, 900000005, ...) so a
//   reviewer never mistakes a live-captured fixture for a hand-authored
//   one, or vice versa.
//
// SPORT: plugins/cascade-pa/telegram live-capture-fixture-writer/TEST
//   (P1-E23-W5-S49-T4, R-14.295/AMD-20260922-3, fix item 3).

package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// captureFixtureIDs is one fixture's redaction target: the synthetic
// chat/sender/message/update ids this file substitutes for the owner's
// real ones.
type captureFixtureIDs struct{ chatID, fromID, messageID, updateID int64 }

// captureBotAPIVersion is the Bot API version current as of this ticket's
// round-2 rework, recorded literally rather than deferred to "current at
// capture time" (round-2 fix item 3: the confirming CR's own words, "record
// the real Bot API version from the response" -- the Bot API does not
// return a version field in any response this module calls, so the
// concrete, checkable value is the documented version as of the date
// below; recordCaptureProvenance stamps that date alongside it so a
// reviewer can tell how stale the pairing is).
const captureBotAPIVersion = "Bot API 9.3 (core.telegram.org/bots/api, checked 2026-09-23)"

// writeCaptureFixtures redacts and writes all four fixtures got carries —
// TestTelegramLiveCapture already refused to return with any of the four
// nil, so this function's caller never reaches here early.
func writeCaptureFixtures(t *testing.T, got *captureResult) {
	t.Helper()
	writeOneCaptureFixture(t, "getupdates_live_text.json", got.text,
		captureFixtureIDs{700000201, 700000301, 501, 900100001})
	writeOneCaptureFixture(t, "getupdates_live_pair.json", got.pair,
		captureFixtureIDs{700000202, 700000302, 502, 900100002})
	writeOneCaptureFixture(t, "getupdates_live_callback_approve.json", got.approve,
		captureFixtureIDs{700000203, 700000303, 503, 900100003})
	writeOneCaptureFixture(t, "getupdates_live_callback_reject.json", got.reject,
		captureFixtureIDs{700000204, 700000304, 504, 900100004})
}

// writeOneCaptureFixture redacts raw per ids and writes name under
// testdata/, in the SAME {"ok":true,"result":[Update]} envelope shape
// every other fixture in this directory already uses.
func writeOneCaptureFixture(t *testing.T, name string, raw json.RawMessage, ids captureFixtureIDs) {
	t.Helper()
	redacted, err := redactRawUpdate(raw, ids)
	if err != nil {
		t.Fatalf("redact %s: %v", name, err)
	}
	envelope := struct {
		OK     bool              `json:"ok"`
		Result []json.RawMessage `json:"result"`
	}{OK: true, Result: []json.RawMessage{redacted}}
	out, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join("testdata", name)
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s", path)
}

// redactRawUpdate replaces every identifying number and the sender's real
// name/username in raw's decoded map[string]any with ids, then re-marshals
// — a field this function does not name (entities, reply_markup, any wire
// addition this package's Update struct has never heard of) survives
// untouched, which is the whole point of operating on the map rather than
// on a typed struct (this file's header).
func redactRawUpdate(raw json.RawMessage, ids captureFixtureIDs) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["update_id"] = ids.updateID
	if msg, ok := m["message"].(map[string]any); ok {
		redactRawMessage(msg, ids)
	}
	if cq, ok := m["callback_query"].(map[string]any); ok {
		cq["id"] = fmt.Sprintf("capture-callback-%d", ids.updateID)
		cq["from"] = redactedUser(ids.fromID)
		if _, ok := cq["chat_instance"]; ok {
			cq["chat_instance"] = fmt.Sprintf("capture-instance-%d", ids.updateID)
		}
		if msg, ok := cq["message"].(map[string]any); ok {
			redactRawMessage(msg, ids)
			delete(msg, "from")
		}
	}
	return json.Marshal(m)
}

// redactRawMessage redacts one message-shaped map in place: message_id,
// chat.id, date and from — every other key (text, entities, reply_markup,
// anything this package's Message struct has never declared) is left
// exactly as the server sent it.
func redactRawMessage(msg map[string]any, ids captureFixtureIDs) {
	msg["message_id"] = ids.messageID
	msg["date"] = t0().Unix()
	if chat, ok := msg["chat"].(map[string]any); ok {
		chat["id"] = ids.chatID
		for _, k := range []string{"username", "first_name", "last_name", "title"} {
			delete(chat, k)
		}
		chat["first_name"] = "Test"
	}
	if _, ok := msg["from"]; ok {
		msg["from"] = redactedUser(ids.fromID)
	}
}

// redactedUser is the fixed, obviously-synthetic User object every
// redacted from field becomes: no real name or username ever survives.
func redactedUser(id int64) map[string]any {
	return map[string]any{"id": id, "is_bot": false, "first_name": "Test"}
}

// recordCaptureProvenance appends this capture's provenance entry to
// testdata/README.md — the ONLY place this file's tree ever writes
// "captured from a live call: yes" for these four fixtures, since it only
// runs when the owner actually executes TestTelegramLiveCapture.
func recordCaptureProvenance(t *testing.T) {
	t.Helper()
	path := filepath.Join("testdata", "README.md")
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	section := fmt.Sprintf(captureProvenanceTemplate, captureBotAPIVersion, time.Now().UTC().Format("2006-01-02"))
	tmp := path + ".capture-tmp"
	if err := os.WriteFile(tmp, append(existing, []byte(section)...), 0o644); err != nil {
		t.Fatalf("write provenance to %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil { // atomic: the README never holds half a provenance row
		t.Fatalf("replace %s: %v", path, err)
	}
}

// captureProvenanceTemplate is recordCaptureProvenance's one format string.
const captureProvenanceTemplate = `

## getupdates_live_{text,pair,callback_approve,callback_reject}.json (P1-E23-W5-S49-T4, AMD-20260922/3, T0 RULING R10)

Four getUpdates envelopes captured from the owner's own paired bot, over the
owner's direct-message chat established by a real pairing handshake (27
SSA.2: "the owner's own bot, own channel, test traffic only"), redacted
field-by-field on the RAW JSON (live_capture_fixtures_test.go's
redactRawUpdate: chat id, sender id, message id and update id are fixed
synthetic placeholders distinct from every other fixture's; every other
wire field, including any this package's own Update type does not declare,
survives untouched) then decoded through the real production decodeEnvelope
and parseApprovalData (live_fixture_test.go).

| field | value |
|---|---|
| tool | TestTelegramLiveCapture (plugins/cascade-pa/telegram/live_capture_test.go, this repo) |
| source | the owner's own paired Telegram bot, live getUpdates over the paired DM chat |
| API version | %s |
| date captured | %s |


captured from a live call: yes
`
