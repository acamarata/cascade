# FuzzParseRetryAfter seed corpus

Provenance: hand-authored seeds covering the two documented Retry-After
shapes (RFC 9110 Sec.10.2.3) plus adversarial inputs, added with this
ticket (P1-E40-W9-S78-T1). Not captured from a live provider response --
`ParseRetryAfter` is a pure string parser exercised directly, so no
recorded fixture applies here (Art.2's "recorded fixture" requirement
governs the gemini driver's HTTP-status mapping tests, not this parser).

Seeds cover:

- empty string (refused)
- delta-seconds: `"0"`, `"120"`
- a negative delta-seconds value (refused, never a negative duration)
- an unrecognised value (refused)
- HTTP-date (RFC 1123, GMT) at a fixed past instant and a future instant
- an out-of-range integer literal (must not overflow/panic)
- raw non-UTF8-safe control bytes

Go's fuzzer will add further corpus entries under this directory as it
discovers new interesting inputs; those are committed separately as found.
