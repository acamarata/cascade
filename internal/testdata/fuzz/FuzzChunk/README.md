# internal/testdata/fuzz/FuzzChunk — FuzzChunk seed corpus

Seed corpus for `internal/retrieval.FuzzChunk` (`internal/retrieval/fuzz_test.go`),
per 06-FORGE-SPEC.md §5 rule 7 ("any ticket adding a parser/decoder MUST
include a `FuzzXxx` target in checks; corpora live at
`internal/testdata/fuzz/...`, never repo root") and this module's
established convention of putting every `FuzzXxx` corpus under this
shared home rather than beside its owning package (see the sibling
`FuzzMCPFrame`, `FuzzParseRequest`, `events`, `scheduler` directories).

`fuzz_test.go` reads every file in this directory except `README.md` at
`FuzzChunk`'s setup time and seeds each one's raw bytes via `f.Add`,
alongside a handful of literal adversarial inputs a file cannot express as
cleanly (nil, a single NUL byte, a 64KB run of unbroken `#` characters).

## Provenance (Article 2)

Unlike this ticket's `testdata/v1-goldens/` fixtures (real-file inputs
with expected chunk boundaries), a fuzz seed corpus has no "real
counterpart" to harvest from — its purpose is exercising specific
adversarial shapes, so every seed here is hand-authored to a stated
purpose rather than copied from an external source:

- `seed_markdown_basic.md` — a small well-formed markdown document (one
  H1, one H2, inline code, a link) so the fuzzer's mutations start from a
  structurally valid seed rather than only adversarial ones.
- `seed_go_basic.go` — a small well-formed Go source file (one func with
  a doc comment, one struct) for the same reason on the go/ast path.
- `seed_nested_fences.md` — an ATX-heading-like line ("# also not a
  heading") nested inside triple-backtick fences, itself inside a
  quadruple-backtick fence. This package's chunker treats ATX detection
  line-by-line with no fence awareness (markdown.go's package doc records
  this as a stated non-goal, not a gap); this seed is the adversarial
  input that property exists to describe, so the fuzzer explores content
  shaped like it.
- `seed_huge_line.txt` — 60,000 bytes of `x` with no newline at all,
  proving the line scanner (`splitLines`, markdown.go) does not choke on
  an arbitrarily long single line — the shape a hostile or corrupt input
  with no line breaks would take.
- `seed_invalid_utf8_and_mixed_eol.bin` — mixed LF/CR/CRLF line endings
  followed by a byte sequence (`\xc3\x28`) that is not valid UTF-8 (0xC3
  starts a two-byte sequence but 0x28 is not a valid continuation byte),
  exercising both `canonicalizeLineEndings` (id.go) and
  `validateContent`'s (chunk.go) binary-content rejection path in the
  same input.

Every seed is run through every language `fuzz_test.go`'s `fuzzLangs`
drives (`markdown`, `go`, `python`, `rust`, `javascript`, and an
unrecognized language name to exercise the regex fallback's default
pattern), so a single seed file here exercises every chunker path, not
just the one its own extension would suggest.
