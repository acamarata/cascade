# internal/testdata/fuzz/FuzzFrontmatterParse

Seed corpus for `internal/memory.FuzzFrontmatterParse`
(`internal/memory/file_store_fs_test.go`), per 06-FORGE-SPEC.md §5 rule 7
and this module's convention that every fuzz corpus lives under this shared
home rather than beside its owning package.

The target reads every file in this directory except this README at setup
time and seeds each one's raw bytes with `f.Add`, alongside the four
checked-in format fixtures from `internal/memory/testdata/v1-goldens/` and
three literals a file cannot express (nil, a bare opening fence, an empty
frontmatter block).

The property being fuzzed is stronger than "does not panic". Every input
must either be refused with a taxonomy error and the zero record, or decode
to a record that survives its own encode-decode round trip. There is no
third outcome, and a partially populated record returned alongside an error
fails the test.

## Provenance (Article 2)

`seed001` is a byte-for-byte copy of `entry_user.md` from
`internal/memory/testdata/v1-goldens/`, so the fuzzer's mutations start
from a structurally valid record rather than only from broken ones. Its own
provenance is recorded in that directory's README.

Every other seed is derived from `seed001` by one deliberate corruption,
hand-specified to reach a distinct refusal path. A fuzz corpus has no real
external counterpart to harvest, so each seed states the shape it exists to
explore:

| Seed | Shape |
|---|---|
| `seed002_truncated_header.bin` | The first third of a valid record: the shape an interrupted write by an external tool would leave, with the frontmatter cut mid-key. |
| `seed003_no_closing_fence.bin` | Header and body present, closing fence removed, so the parser runs off the end looking for a fence that never comes. |
| `seed004_wrong_types.bin` | A quoted string where a number belongs and a bare number where a quoted string belongs, in one input. |
| `seed005_future_format.bin` | A format version larger than a 32-bit integer, which must be refused as unsupported rather than overflowing. |
| `seed006_fence_only.bin` | An empty frontmatter block: well-formed fences, no keys at all. |
| `seed007_invalid_utf8_and_crlf.bin` | Every line ending converted to CRLF and a byte sequence that is not valid UTF-8 (`0xC3 0x28`: a two-byte lead followed by an invalid continuation) placed in the body, exercising the line-ending tolerance and the quoting path in the same input. |
| `seed008_duplicate_and_unknown_keys.bin` | A key repeated and an unrecognized key added, the two ways a writer and this reader can disagree about the key set. |
| `seed009_deep_nesting_and_long_line.bin` | A 40KB single frontmatter line consisting of 20,000 escaped quote sequences, exercising the string decoder against an input far longer than any real record's. |
