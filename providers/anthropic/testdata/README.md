# providers/anthropic testdata provenance

Per 12-QUALITY-CONSTITUTION.md Art.2.2, every fixture the no-network unit
lane replays must carry a stated provenance: what produced it, and when.

## Fixtures

All fixtures in this directory and in `fuzz/FuzzAnthropicWireDecode/` were
transcribed by hand from Anthropic's public Messages API reference
documentation (the `/v1/messages`, `/v1/messages/count_tokens`, and
streaming-events pages at docs.anthropic.com), on 2026-09-06. They are NOT
captured from a live call: this build environment has no outbound network
access (12-QUALITY-CONSTITUTION.md Art.7.2's default unit lane forbids it
by construction, and this task ran with no egress at all), so a genuine
`curl`/SDK capture against `api.anthropic.com` could not be performed as
part of this ticket.

This is recorded honestly as a known gap against Art.2's "real counterpart"
standard: the JSON shapes below reflect the vendor's documented contract to
the best of this transcription, not a byte-for-byte capture of an actual
response. Wire shapes used:

- Chat response envelope: `id`, `type`, `role`, `content` (array of
  `{type, text}` blocks), `model`, `stop_reason`, `usage`
  (`input_tokens`/`output_tokens`).
- Error envelope: `{"type":"error","error":{"type":"...","message":"..."}}`.
- Streaming SSE event names: `message_start`, `content_block_start`,
  `content_block_delta` (`delta.text`), `content_block_stop`,
  `message_delta` (`usage`), `message_stop`, `error`, `ping`.
- `count_tokens` response: `{"input_tokens": N}`.

## Follow-up

When this driver is wired behind a live credential (S-20.T1's intake,
outside this ticket's scope), the tagged `integration` lane in
`integration_test.go` should be run once against the real API and its
actual response bytes captured here to replace this transcription,
updating this file's provenance line to name the live capture instead.

## Fuzz seed corpus

`fuzz/FuzzAnthropicWireDecode/seed_response.json` holds one seed: a `raw`
field containing a full six-event SSE exchange (message_start through
message_stop) built from the same transcribed shapes above. The fuzz test
in `anthropic_test.go` loads this file and calls `f.Add` with its bytes,
alongside a handful of inline malformed/truncated seeds, before fuzzing
`scanSSEEvents` + `decodeSSEEvent`.
