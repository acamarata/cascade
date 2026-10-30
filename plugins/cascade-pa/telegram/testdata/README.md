# plugins/cascade-pa/telegram test fixture provenance

## The four `getupdates_*.json` fixtures

| file | shape |
|---|---|
| `getupdates_text.json` | one `message` update with `from`/`chat`/`text` |
| `getupdates_pair_cmd.json` | the same shape, carrying `/pair <code>` |
| `getupdates_callback_query.json` | one `callback_query` update with `from`, its originating `message`, `chat_instance` and `data` |
| `getupdates_callback_inaccessible.json` | a Bot API >= 7.0 `callback_query` whose `message` is an `InaccessibleMessage` (`date: 0`, no `text`) |

Each is one `getUpdates` response envelope, `{"ok":true,"result":[Update,...]}` —
the shape documented at <https://core.telegram.org/bots/api#update>,
<https://core.telegram.org/bots/api#message>,
<https://core.telegram.org/bots/api#callbackquery>,
<https://core.telegram.org/bots/api#inaccessiblemessage> and
<https://core.telegram.org/bots/api#getupdates>.

All four decode through the PRODUCTION types (`decodeEnvelope` into `[]Update`,
`wire_test.go`); no test declares a local struct for them, so a field name that
drifted from the API would fail a test rather than pass one quietly.

## Provenance, stated plainly

| field | value |
|---|---|
| tool | hand-authored against the Bot API reference documentation |
| source | <https://core.telegram.org/bots/api> |
| API version | Bot API 7.x (the `InaccessibleMessage` fixture requires >= 7.0) |
| date authored | 2026-09-20 |
| captured from a live call | **no** |

**These fixtures are NOT captured from a live call to api.telegram.org.** This
build environment has no bot token and no outbound network access, and creating
a Telegram bot account to capture a real response is outside a single ticket's
authorised scope (creating accounts and handling credentials is not an action
this lane may take).

**The risk this carries.** Art.2 asks for a fixture captured from the real API
precisely because a hand-authored one can diverge from what the server actually
sends: an optional field the docs describe loosely, an integer that is sometimes
a string, a null-versus-omitted difference. These fixtures prove the DECODER
accepts bytes of the documented shape; they do not prove the documented shape is
what production traffic looks like. A capture against a throwaway bot (mirroring
`cascade mcp serve --stdio --capture`'s pattern for the MCP fixtures) would
close that gap and should replace this note when it lands.

**Identifiers.** Every id here (`555000111`, `555000222`, `900000001`–`900000005`,
`4382bfdwdsb323b2d9`, `cascade_test_user`, `cascade_test_bot`) is a synthetic
placeholder chosen to be obviously fake — never a real Telegram user, chat, bot
or callback identifier. No fixture contains a token.

## fuzz/FuzzTelegramUpdate/*

`seed-text-message` and `seed-pair-command` are the first two files above with
their envelopes stripped (the fuzz target decodes a single `Update` object, not
the `getUpdates` envelope — see `fuzz_test.go`). `seed-malformed` is a
deliberately truncated JSON fragment exercising the decoder's never-panics
guarantee on adversarial input.

## `callback_query_approve.json` / `callback_query_reject.json` (P1-E23-W5-S48-T4)

Two bare `callback_query` objects (not wrapped in a `getUpdates` envelope —
this package's own callback_query flow decodes one object directly, the
shape `answerCallbackQuery`'s own inline-button tap delivers), documented
at <https://core.telegram.org/bots/api#callbackquery>. `data` carries this
module's own pipe-delimited `"<request_id>|<nonce>"` shape — R-21.210's
wire contract verbatim (T0 D1, 2026-09-21; approval.go's header comment
explains the ruling — no producer in this tree yet mints a real
inline-button prompt, so this is this ticket's own documented choice, not
a Bot API field), kept under the Bot API's 64-byte `callback_data` ceiling
(53 bytes here).

| field | value |
|---|---|
| tool | hand-authored against the Bot API reference documentation |
| source | <https://core.telegram.org/bots/api#callbackquery> |
| API version | Bot API 7.x |
| date authored | 2026-09-21 |
| captured from a live call | **no** — same credential-boundary reason the four fixtures above give |

Both decode through the PRODUCTION Bot API envelope decoder AND this
package's own callback_query validation (`validateCallbackQuery`,
approval.go) via the test-only `parseCallbackQuery` helper
(`approval_gates_test.go`) — there is no separate exported
`ParseCallbackQuery` symbol, since production never hands a handler raw
per-callback bytes (see approval.go's header). `TestCallbackQueryFixturesDecode`
(approval_fuzz_test.go) drives both fixtures through it, and both seed
`fuzz/FuzzParseCallbackQuery/`. Identifiers (`900000101`, `900000102`,
`555000111`, `4382bfdwdsb323b2d9`, the ULID-shaped request/nonce ids) are
synthetic placeholders, per this file's Identifiers section above.

## `sendmessage_approval_buttons.json` (P1-E23-W5-S48-T4 producer leg, T0 D4)

One `sendMessage` API envelope (`{"ok":true,"result":Message}`), the shape
documented at <https://core.telegram.org/bots/api#sendmessage> and
<https://core.telegram.org/bots/api#message>. `approval_send.go`'s
`sendApprovalButtons` decodes this fixture's shape through the PRODUCTION
`decodeEnvelope` path (`approval_send_test.go`/`approval_send_roundtrip_test.go`
drive it via the package-local `sendCapturingDoer`, mirroring
`egress_test.go`'s `sendmessage_ok.json` precedent for the same call). The
`text` field is this file's own fixed outbound string (`approvalPromptText`,
approval_send.go) — a producer that echoed the real approval's summary in the
message body would be a leak path §5.24 restricts.

| field | value |
|---|---|
| tool | hand-authored against the Bot API reference documentation |
| source | <https://core.telegram.org/bots/api#sendmessage> |
| API version | Bot API 7.x |
| date authored | 2026-09-21 |
| captured from a live call | **no** — same credential-boundary reason the four fixtures above give |

`chat.id` (`555000111`) and `from.id` (`900000005`) reuse the SAME synthetic
placeholders `callback_query_approve.json`/`callback_query_reject.json`
already establish, so the round-trip test
(`TestTelegramApprovalSender_RoundTrip_ConsumerGrantsAndDenies`) sends and
taps against one consistent synthetic identity rather than inventing a
second one. `message_id` (`44`) is a fresh value, distinct from the other
`sendmessage_ok.json` fixture's `43`, so the two are never confused by a test
that reads the wrong one. No fixture here contains a token; see this file's
Identifiers section above.

## `sendmessage_ok.json` (P1-E23-W5-S48-T2)

One `sendMessage` API envelope (`{"ok":true,"result":Message}`), the shape
documented at <https://core.telegram.org/bots/api#sendmessage> and
<https://core.telegram.org/bots/api#message>. `egress_test.go`'s
`recordingPoster` decodes this fixture's shape through the REAL production
`decodeEnvelope` path (see this file's `sendmessage_approval_buttons.json`
section above for the same call's identical fixture-decode precedent).
`result.text` is this module's own `replyText(threadID, turnID)` output
(chat.go) carrying the bridge test rig's canned `turn-1` turn id — it is
NOT a value Telegram's API ever produces, which is exactly why this fixture
cannot be a live capture: capturing one would mean sending this module's
own already-generated reply text back through a real bot to get it echoed
into `result.text`, proving nothing about the API shape a hand-authored
envelope does not already prove.

| field | value |
|---|---|
| tool | hand-authored against the Bot API reference documentation |
| source | <https://core.telegram.org/bots/api#sendmessage> |
| API version | Bot API 7.x |
| date authored | 2026-09-21 |
| captured from a live call | **no** — same credential-boundary reason the four fixtures above give |

`chat.id` (`555000111`) and `from.id` (`900000005`) reuse the SAME synthetic
placeholders `sendmessage_approval_buttons.json` already establishes.
`message_id` (`43`) is distinct from that fixture's `44`, so the two are
never confused by a test that reads the wrong one. No fixture here contains
a token; see this file's Identifiers section above.

## No new fixtures for P1-E23-W5-S48-T3

The bridge secret-refusal ticket (R-21.203/R-21.105) adds no decoder and no
parser: the H/S-16 credential-value detector consumes text this package's
existing `Update` decoder already produces. There is therefore no new
`FuzzXxx` target and no new fixture here — `refuse_test.go` and
`quarantine_test.go` drive the refusal and quarantine gate directly against
an injected `SecretScanner`/`QuarantineSink`, over the same synthetic,
obviously-fake witness strings this file's Identifiers section describes.

## getupdates_live_{text,pair,callback_approve,callback_reject}.json (P1-E23-W5-S49-T4, AMD-20260922/3, T0 RULING R10) — STILL PENDING, NOT YET PRODUCED

Four fixtures are still required from a live capture against the owner's
own paired bot, over the owner's own direct-message chat established by a
real pairing handshake (27 SSA.2; R10 replaced the earlier fixed-channel
targeting this section used to describe), per R-14.295/AMD-20260922-3: a
text message, a `/pair ` message, an Approve callback_query and a Reject
callback_query, each redacted field-by-field on the raw response bytes and
decoded through the real production `decodeEnvelope`/`parseApprovalData`.
They do not exist yet. The provenance row this ticket's acceptance check
greps for is written automatically, once, by the capture run itself (see
`live_capture_fixtures_test.go`'s `recordCaptureProvenance`) and appears
ONLY below this paragraph, never inside it, so this paragraph's own prose
can describe that outcome without pre-empting the check that looks for it.
`TestTelegramLiveCapturedFixturesDecode` (`live_fixture_test.go`, build tag
`tgcapture`) fails until the fixtures land, by design (R6: a missing capture
is a failing acceptance check, never a silent skip). The tag keeps it out of
every untagged and integration run until the capture exists. The capture runs
only in the P1-EXIT-11 owner-present session.

To produce them, the owner maps the vault's own bot-token value onto the
one neutral env-ref below at invocation time (the vault's own variable name
is deliberately never spelled out in this tracked file) and sets the
capture opt-in; the pairing code the run prints is entered from Telegram,
never typed into this command line:

```
CASCADE_ACCEPT_TELEGRAM_BOT_TOKEN=<the vault's bot token> \
CASCADE_ACCEPT_CAPTURE=1 \
go test -tags integration -run '^TestTelegramLiveCapture$' ./plugins/cascade-pa/telegram/
go test -tags tgcapture -run '^TestTelegramLiveCapturedFixturesDecode$' ./plugins/cascade-pa/telegram/
```
