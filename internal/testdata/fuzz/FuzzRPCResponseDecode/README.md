# internal/testdata/fuzz/FuzzRPCResponseDecode — FuzzRPCResponseDecode seed corpus

Seed corpus for `internal/client.FuzzRPCResponseDecode`
(`internal/client/fuzz_test.go`), per 06-FORGE-SPEC.md §5 rule 7 ("any
ticket adding a parser/decoder MUST include a `FuzzXxx` target in checks;
corpora live at `internal/testdata/fuzz/...`, never repo root") and
P1-E04-W1-S07-T3's files_scope, which names this exact path.

`fuzz_test.go` reads every `*.json` file in this directory at
`FuzzRPCResponseDecode`'s setup time and seeds each one via `f.Add`,
alongside a handful of literal byte slices covering cases a checked-in
file cannot express as cleanly (a nil slice, deeply nested brackets).

## Provenance (Article 2 — external contract)

External contract: **JSON-RPC 2.0** (jsonrpc.org), the same specification
`internal/rpc`'s own `FuzzParseRequest` corpus documents, applied here to
the response side (`internal/rpc.ResponseEnvelope`) this SDK decodes.
Tool/version/date of provenance: `https://www.jsonrpc.org/specification`,
captured 2026-09-03 for this ticket.

- `valid-result.json` — a well-formed `status.get` success envelope,
  shaped exactly like `internal/daemon.StatusResponse`'s JSON, with the
  `protocol_version`/`server_version` fields `internal/rpc.NewEnvelope`
  always adds.
- `valid-error.json` — a well-formed JSON-RPC 2.0 error envelope carrying
  an application-band code (`internal/rpc`'s `RPCCodeNotFound`, -32001).
- `valid-method-not-found.json` — the exact shape
  `internal/rpc/registry.go`'s `methodNotFoundError` produces: code -32601
  with a `data.kind` member, this SDK's `kindForRPCError` special-cases.
- `valid-notification-null-result.json` — an empty id and a JSON `null`
  result, the shape a notification-style call's response takes.
- `adversarial-empty.json` — zero bytes: the shortest possible truncation.
- `adversarial-not-json.json` — plain text that is not JSON at all.
- `adversarial-truncated.json` — a JSON object body cut off mid-value.
- `adversarial-mismatched-id.json` — a well-formed envelope whose `id`
  does not match any id this SDK would have sent — `Do`'s explicit
  id-correlation check (hard requirement 2's "mismatched-id" test case).
- `adversarial-oversized.json` — several KB of non-JSON base64 noise,
  covering a large-but-not-truncated adversarial body.

`FuzzRPCResponseDecode`'s property is: `decodeEnvelope` never panics on
any input, well-formed or not — malformed input returns a non-nil error,
and a successfully decoded envelope's raw `Result` bytes (if present)
always re-parse as valid JSON.
