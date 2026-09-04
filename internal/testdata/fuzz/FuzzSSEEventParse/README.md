# internal/testdata/fuzz/FuzzSSEEventParse — FuzzSSEEventParse seed corpus

Seed corpus for `internal/client.FuzzSSEEventParse`
(`internal/client/fuzz_test.go`), per 06-FORGE-SPEC.md §5 rule 7 and
P1-E04-W1-S07-T3's files_scope, which names this exact path.

`fuzz_test.go` reads every `*.txt` file in this directory at
`FuzzSSEEventParse`'s setup time and seeds each one via `f.Add` (a resume
token / SSE stream is opaque text, not JSON, hence `.txt` rather than
`.json` — the same convention `internal/rpc`'s
`FuzzParseResumeToken` corpus already uses for the sibling Last-Event-ID
parser).

## Provenance (Article 2 — external contract)

External contract: **Server-Sent Events** (W3C/WHATWG "HTML Living
Standard", §9.2 "Event stream interpretation" — the same living standard
`internal/rpc/sse.go`'s own package doc cites for its heartbeat/resume
semantics). Tool/version/date of provenance:
`https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation`,
captured 2026-09-03 for this ticket.

- `valid-single-event.txt` — one complete `id`/`data` block exactly as
  `internal/rpc/sse.go`'s `writeSSEEvent` emits it, terminated by a blank
  line.
- `valid-heartbeat-comment.txt` — a comment line (leading `:`), the exact
  shape `internal/rpc/sse.go`'s heartbeat writes (`": keep-alive"`); the
  spec's own example of a comment used for exactly this purpose.
- `valid-multiline-data.txt` — two `data:` lines in one block, which the
  spec requires be joined with `"\n"` on dispatch.
- `adversarial-empty.txt` — zero bytes.
- `adversarial-bare-field-no-colon.txt` — a field name with no `:` at
  all, the spec's own "line consists of a field name, no colon" case.
- `adversarial-empty-value.txt` — a field with a colon but no value.
- `adversarial-truncated-no-blank-line.txt` — a block that starts a
  `data:` field and is cut off before the terminating blank line, so no
  event is ever dispatched from it.
- `adversarial-many-colons.txt` — a comment line composed entirely of
  colons, exercising the "first colon splits field from value" rule
  against a line that has several.

`FuzzSSEEventParse`'s property is: `sseAccumulator.feed` never panics on
any single line, and never emits more accumulated bytes than the sum of
what it was actually fed.
