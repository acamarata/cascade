# providers/ollama testdata provenance

Per 12-QUALITY-CONSTITUTION.md Art.2.2, every fixture the no-network unit
lane replays must carry a stated provenance: what produced it, and when.

## Fixtures

All fixtures in this directory and in `fuzz/FuzzOllamaWireDecode/` were
transcribed by hand from Ollama's public API reference documentation (the
`/api/chat`, `/api/embed`, and `/api/tags` pages at
github.com/ollama/ollama/blob/main/docs/api.md), on 2026-09-06. They are
NOT captured from a live call: this build environment has no outbound
network access and no local Ollama installation (12-QUALITY-CONSTITUTION.md
Art.7.2's default unit lane forbids network access by construction, and
this task ran with no egress and no `ollama` binary available), so a
genuine `curl` capture against a running `ollama serve` instance could not
be performed as part of this ticket.

This is recorded honestly as a known gap against Art.2's "real counterpart"
standard: the JSON shapes below reflect the vendor's documented contract to
the best of this transcription, not a byte-for-byte capture of an actual
response. Wire shapes used:

- Chat response envelope (`/api/chat`, `stream: false`): `model`,
  `created_at`, `message` (`{role, content}`), `done`, `done_reason`,
  `prompt_eval_count`, `eval_count`.
- Chat streaming body (`/api/chat`, `stream: true`, the default): a
  sequence of newline-delimited JSON objects, each the same shape as the
  non-streaming response but with `done: false` and a partial
  `message.content` until the final line, which carries `done: true`,
  `done_reason`, and the two eval-count usage fields.
- Error envelope: `{"error": "..."}`, returned with a non-2xx HTTP status
  (400 for a malformed request, 404 for an unknown/unpulled model).
- Embed request/response (`/api/embed`): request `{"model": "...",
  "input": [...]}` accepts a batch; response `{"embeddings": [[...],
  ...], "prompt_eval_count": N}`.
- Tags response (`/api/tags`): `{"models": [{"name": "...", ...}]}`.

Ollama exposes no server-side token-count endpoint, so this driver's
Count verb is not backed by a fixture here - it returns a typed
KindUnsupported error directly (ollama.go).

## Follow-up

When this driver is wired behind a live local Ollama instance (S-20.T1's
intake, outside this ticket's scope, or the tagged `integration` lane in
integration_test.go run manually with OLLAMA_BASE_URL set against a real
`ollama serve`), its actual response bytes should be captured here to
replace this transcription, updating this file's provenance line to name
the live capture instead.

## Fuzz seed corpus

`fuzz/FuzzOllamaWireDecode/seed_response.json` holds one seed: a `raw`
byte slice containing one complete NDJSON streaming line (a
`content_block`-style delta chunk) built from the same transcribed shapes
above. The fuzz test in `ollama_test.go` loads this file via Go's native
corpus format and calls `f.Add` with its bytes, alongside a handful of
inline malformed/truncated/error-shaped seeds, before fuzzing
`decodeStreamChunk`.
