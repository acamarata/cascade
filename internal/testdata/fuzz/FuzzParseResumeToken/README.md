# internal/testdata/fuzz/FuzzParseResumeToken — seed corpus

Seed corpus for `internal/rpc.FuzzParseResumeToken` (`internal/rpc/fuzz_sse_test.go`),
per 06-FORGE-SPEC.md §5 rule 7 ("any ticket adding a parser/decoder MUST
include a `FuzzXxx` target") and P1-E04-W1-S06-T4's `files_scope`, which
names this exact path.

`fuzz_sse_test.go` reads every `*.txt` file in this directory at
`FuzzParseResumeToken`'s setup time and seeds each one's raw bytes via
`f.Add`. Unlike `FuzzParseRequest`'s JSON corpus, a resume token is not a
structured format — it is an opaque string a client copies verbatim from
an SSE event's `id:` line into its next request's `Last-Event-ID` header —
so seeds here are plain text/binary, not JSON.

## Provenance (Article 2 — external contract)

External contract: **W3C Server-Sent Events (Living Standard)**,
`https://html.spec.whatwg.org/multipage/server-sent-events.html`, the
section defining the `Last-Event-ID` request header and its SHOULD
semantics (a client MUST send back exactly the last-seen `id:` value
verbatim; a server encountering a value it does not recognize SHOULD NOT
treat that as an error). Captured 2026-09-03 for this ticket. R-14.13
(15-T0-RULINGS-R14.md) ratifies the concrete wire choice built on top of
that spec text: `Last-Event-ID` is an opaque base64url wrapping of
`internal/events/cursor.go`'s 8-byte big-endian Seq cursor.

- `empty.txt` — a zero-byte file: the absent-header case (Go's
  `http.Header.Get` also returns `""` for a header that was never sent,
  which this same code path must handle identically).
- `valid-cursor.txt` — `base64.RawURLEncoding` of `binary.BigEndian.PutUint64(_, 42)`:
  a real, valid resume token as `formatResumeToken`/an SSE client would
  produce it.
- `garbage.txt` — arbitrary non-base64 bytes, including raw `0x00`/`0xff`
  and shell-adversarial characters.
- `max-length.txt` — 4096 bytes of the same non-cursor character: proves
  no unbounded-length‑driven panic or slowdown.

`FuzzParseResumeToken`'s property: `parseResumeToken` never panics on any
input, and never returns an error — every input decodes to either a valid
`(seq, true)` (only for a well-formed base64url wrapping of exactly 8
bytes) or `(0, false)` ("open at tail"), per the SHOULD semantics above.
