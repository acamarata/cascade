# internal/testdata/fuzz/FuzzParseRequest — FuzzParseRequest seed corpus

Seed corpus for `internal/rpc.FuzzParseRequest` (`internal/rpc/fuzz_test.go`),
per 06-FORGE-SPEC.md §5 rule 7 ("any ticket adding a parser/decoder MUST
include a `FuzzXxx` target in checks; corpora live at
`internal/testdata/fuzz/...`, never repo root") and
P1-E04-W1-S06-T3's `files_scope`, which names this exact path.

`fuzz_test.go` reads every `*.json` file in this directory at
`FuzzParseRequest`'s setup time and seeds each one via `f.Add`, alongside a
handful of literal strings covering cases a valid JSON file cannot express
(a genuinely empty body, non-JSON garbage).

## Provenance (Article 2 — external contract)

External contract: **JSON-RPC 2.0** (jsonrpc.org). Tool/version/date of
provenance: `https://www.jsonrpc.org/specification`, the JSON-RPC 2.0
specification (a stable, dated-2013 "Living Standard" that has not revised
since — no version number beyond "2.0" is published by the spec itself),
captured 2026-09-03 for this ticket.

- `valid-positional-params.json` — the spec's own "rpc call with positional
  parameters" example verbatim (§examples), reused as a request fixture and
  again inline in `jsonrpc_test.go`'s `specFixture` constant (this ticket's
  AC: "at least one test exercises a spec-sourced wire fixture").
- `valid-named-params-string-id.json` — the spec's "rpc call with named
  parameters" example verbatim, with a string id (the spec permits id to be
  a string, a number, or null).
- `valid-notification.json` — the spec's notification example (no `id`
  member at all) verbatim.
- `valid-null-id.json` — an explicit JSON `null` id, distinct from an
  omitted id (a notification) per this ticket's `Request.IsNotification`
  semantics.
- `valid-client-version.json` — a well-formed request carrying this
  ticket's own `client_version` extension field (not part of the JSON-RPC
  2.0 spec itself; version.go's `SkewCheck` reads it).
- `adversarial-empty.json` — zero bytes: the shortest possible truncation.
- `adversarial-not-json.json` — plain text that is not JSON at all.
- `adversarial-batch-array.json` — the spec's own batch-call example
  verbatim; this ticket deliberately does NOT implement batch requests
  (see `version.go`'s and `jsonrpc.go`'s SPEC-COVERAGE doc comments), so
  `Parse` must reject this with a clear `codeInvalidRequest`, never
  silently misparse or panic on the top-level array.
- `adversarial-wrong-version.json` — a syntactically valid request object
  whose `"jsonrpc"` field is `"1.0"`, not `"2.0"`.
- `adversarial-missing-method.json` — a well-formed object missing the
  required `"method"` member.
- `adversarial-truncated.json` — a JSON object body cut off mid-value.

`FuzzParseRequest`'s property is: `Parse` never panics on any input,
well-formed or not — malformed input must return a non-nil `*ErrorObject`,
nothing else (never a nil, nil result, and never a Request whose ID/Params
raw bytes fail to re-parse as JSON).
