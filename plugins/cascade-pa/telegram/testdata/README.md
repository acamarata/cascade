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
