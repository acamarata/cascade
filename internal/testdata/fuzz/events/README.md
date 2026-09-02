# internal/testdata/fuzz/events — FuzzEventDecode seed corpus

Seed corpus for `internal/events.FuzzEventDecode` (`internal/events/fuzz_test.go`),
per 06-FORGE-SPEC.md §5.7 (fuzz corpora live under `internal/testdata/fuzz/`,
never beside the owning package) and P1-E03-W1-S04-T3's `files_scope`, which
names this exact path.

`fuzz_test.go` reads every `*.bin` file in this directory at
`FuzzEventDecode`'s setup time and seeds each one via `f.Add`, so the fuzzer
starts every run already exercising both well-formed envelopes and known
adversarial edge cases, not an empty corpus.

## Provenance (Article 2)

All six files here are hand-authored for this ticket (tool: this ticket's
own `internal/events` envelope format; version: the `encodeEvent` wire
format defined in `internal/events/types.go`; date: 2026-09-02) — there is
no external/real-world artifact to harvest a seed from, since
`internal/events`'s envelope format is new in this ticket.

- `valid-plugin-registered.bin` — a realistic, well-formed envelope for a
  `plugin.registered` event (the Kind this ticket wires end to end for the
  R-14.134 obligation), produced by the package's own `encodeEvent`.
- `valid-empty-fields.bin` — a well-formed envelope with every field at its
  zero value (Seq 0, empty Kind/Source, nil Payload, Unix-epoch Timestamp) —
  the minimal valid case.
- `valid-binary-payload.bin` — a well-formed envelope whose Payload contains
  non-UTF8 bytes, proving the format round-trips arbitrary binary content,
  not just text.
- `adversarial-empty.bin` — zero bytes: the shortest possible truncation.
- `adversarial-truncated-header.bin` — 3 bytes, shorter than the fixed
  16-byte Seq+Timestamp header.
- `adversarial-oversized-length.bin` — a well-formed 16-byte header
  followed by a length prefix (`0x7fffffff`) far larger than any bytes that
  follow it, exercising `readLenPrefixed`'s bounds check.

`FuzzEventDecode`'s property is: `decodeEvent` never panics on any input,
well-formed or not — malformed input must return a `cascade.KindIntegrity`
error, nothing else.
