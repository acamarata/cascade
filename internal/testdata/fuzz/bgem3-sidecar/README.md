# internal/testdata/fuzz/bgem3-sidecar — FuzzBgeM3SidecarDecode seed corpus

Seed corpus for `providers/embeddings/bgem3.FuzzBgeM3SidecarDecode`
(`providers/embeddings/bgem3/protocol_test.go`), per 06-FORGE-SPEC.md §5
rule 7 ("any ticket adding a parser/decoder MUST include a `FuzzXxx` target
in checks; corpora live at `internal/testdata/fuzz/...`, never repo root")
and P1-E06-W2-S12-T2's `files_scope`, which names this exact path.

`protocol_test.go` reads every `*.bin` file in this directory at the
target's setup time and seeds each one via `f.Add`. Go's own automatic
corpus loading only covers the target package's own
`testdata/fuzz/<FuzzName>/` directory, and this corpus deliberately lives
outside the package (the plan's shared corpus root), so the explicit
`f.Add` load is what makes these seeds effective at all.

## What the target is

`decodeResponseFrame` — the client's response decoder. Everything it reads
comes from a separate process on the other end of a socket, so every byte
is untrusted input. The target asserts three properties on every input:
the decoder never panics, never allocates ahead of the bytes that actually
arrived, and never returns a decoded response together with a nil error
unless the payload really was a decodable JSON object.

## Provenance

**First-party protocol, not an Article-2 external contract.** The wire
format is specified in `providers/embeddings/bgem3/SPEC.md` and
implemented on the far side by this project's own post-P1 sidecar
artifact; there is no external specification to cite. Every seed below was
authored by hand from SPEC.md's framing and payload sections on
2026-09-04, generated as raw bytes (the framing is binary, so these are
`.bin`, not `.json`).

## The seeds

Well-formed frames, so the fuzzer starts from inputs that reach the JSON
decoder rather than dying at the header:

- `valid-success.bin` — a conforming success response, two 8-dimension
  vectors.
- `valid-error.bin` — a conforming failure response carrying an
  `invalid_input` code.

Valid-but-unexpected shapes, the class that decodes without error and must
still be refused downstream rather than crashing here:

- `valid-unknown-members.bin` — a future MINOR version's response with
  members this version does not define, and `vectors: null`.
- `shape-scalar-payload.bin`, `shape-array-payload.bin` — valid JSON that
  is not an object.
- `shape-json-null.bin` — the literal `null`, which `encoding/json`
  accepts into a struct as a no-op, leaving a zero-valued response that
  the client's version check is what rejects.
- `shape-wrong-types.bin` — every member present with the wrong JSON type.
- `negative-dimensions.bin` — a structurally valid response claiming a
  negative vector width.
- `both-vectors-and-error.bin` — the two mutually exclusive members
  together, which SPEC.md forbids and the client refuses.

Truncation and framing attacks:

- `empty.bin` — zero bytes, the shortest possible truncation.
- `truncated-header.bin` — two of the four header bytes.
- `truncated-payload.bin` — a header announcing 64 bytes followed by 7.
- `zero-length.bin` — a header declaring a zero-length payload.
- `oversized-length.bin` — a header declaring `0xFFFFFFFF` bytes.
- `oversized-just-over-cap.bin` — one byte over the 16 MiB cap, the exact
  boundary the cap check must catch.
- `at-cap-but-truncated.bin` — a header declaring exactly the cap with two
  bytes behind it: the case that would cost a 16 MiB allocation if the
  decoder sized its buffer from the declared length instead of from what
  arrived.
- `trailing-bytes.bin` — a complete frame followed by bytes the decoder
  must not read.

Encoding and garbage:

- `garbage.bin` — bytes 0x00–0x3F, no structure at all.
- `invalid-utf8.bin` — a framed payload containing an invalid UTF-8
  sequence inside a JSON string.
- `deep-nesting.bin` — 2000 nested arrays, the recursion-depth probe.
- `huge-number.bin` — a vector component of `1e400`, which overflows
  `float32` and must be a decode error rather than an infinity reaching a
  vector index.
