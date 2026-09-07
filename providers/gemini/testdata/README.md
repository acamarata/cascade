# providers/gemini testdata provenance

Per 12-QUALITY-CONSTITUTION.md Art.2.2, every fixture the no-network unit
lane replays must carry a stated provenance: what produced it, and when.

## Fixtures

All fixtures in this directory and in `fuzz/FuzzGeminiWireDecode/` were
transcribed by hand from Google's public Gemini API reference documentation
(the `generateContent`, `streamGenerateContent`, `embedContent` /
`batchEmbedContents`, and `countTokens` reference pages at
ai.google.dev/api and the Generative Language API's published error
model), on 2026-09-06. They are NOT captured from a live call: this build
environment has no outbound network access (12-QUALITY-CONSTITUTION.md
Art.7.2's default unit lane forbids it by construction, and this task ran
with no egress at all), so a genuine capture against
`generativelanguage.googleapis.com` could not be performed as part of this
ticket.

This is recorded honestly as a known gap against Art.2's "real counterpart"
standard: the JSON shapes below reflect the vendor's documented contract to
the best of this transcription, not a byte-for-byte capture of an actual
response. Wire shapes used:

- generateContent response envelope: `candidates` (array of
  `{content: {parts: [{text}], role}, finishReason, index}`) plus
  `usageMetadata` (`promptTokenCount`/`candidatesTokenCount`/
  `totalTokenCount`).
- Error envelope: `{"error":{"code":N,"message":"...","status":"..."}}`
  (the standard `google.rpc.Status`-shaped error every Google API
  surfaces), including the documented invalid-API-key shape (HTTP 400,
  message containing "API key not valid").
- Streaming (`streamGenerateContent?alt=sse`): blank-line-delimited
  `data:` payloads, each a full or partial `GenerateContentResponse` (no
  `event:` line, unlike Anthropic's SSE dialect); the API declares no
  explicit terminal event, so an ended stream after a clean partial
  candidate is this driver's own terminal signal.
- `batchEmbedContents` response: `{"embeddings":[{"values":[...]},...]}`,
  one entry per request, in order.
- `countTokens` response: `{"totalTokens": N}`.

## Follow-up

When this driver is wired behind a live credential (S-20.T1's intake,
outside this ticket's scope), the tagged `integration` lane in
`integration_test.go` should be run once against the real API and its
actual response bytes captured here to replace this transcription,
updating this file's provenance line to name the live capture instead.

## Fuzz seed corpus

`fuzz/FuzzGeminiWireDecode/seed_response.json` holds one seed: a full
two-chunk SSE exchange built from the same transcribed shapes above. The
fuzz test in `gemini_test.go` loads this file (Go's native
`go test fuzz v1` corpus format) and calls `f.Add` with its bytes,
alongside a handful of inline malformed/truncated seeds, before fuzzing
`scanGeminiSSE` + `decodeGeminiChunk`.
