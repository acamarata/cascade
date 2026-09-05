# Rewriter golden fixtures

Seven cases pinning the exact bytes `secrets.Rewriter.Rewrite` produces for a
turn. Each file is read by `internal/secrets/goldenfixture_test.go`.

## Format

A small YAML subset, read by the loader in that test file rather than by a
YAML dependency (the module has none, and a golden loader is not a reason to
add one):

| Field | Meaning |
| --- | --- |
| `name` | fixture name, matching the file name |
| `provenance` | `detector` or `authored`; see below |
| `input` | the original turn, as a Go-quoted string |
| `expected_output` | the exact bytes the rewriter must produce |
| `canaries` | substrings of `input` that must not survive anywhere |
| `hits` | the detection hits handed to the rewriter |

`input` and `expected_output` are Go-quoted on one line so a fixture pins
bytes exactly. An invisible trailing newline is the usual way a golden stops
proving anything, and a quoted string makes it visible.

## Provenance (Art.2)

Nothing here is generated from the rewriter's own output. The two halves of
each fixture come from two independent places:

- **`hits`, for `provenance: detector` fixtures**, are the output of the real
  `secrets.Detector` (`NewDetector(DefaultRegistry(), DefaultDetectionConfig())`,
  `ScanCertain`) over `input`, captured on 2026-09-05 from the detector shipped
  by ticket P1-E08-W2-S15-T3. The fixture test re-derives them on every run and
  fails if the recorded hits and the live detector have drifted apart, so these
  are a live counterpart check rather than a transcription.
- **`hits`, for `provenance: authored` fixtures**, are written by hand to reach
  a case the detector resolves before a rewriter ever sees it. `overlap_span`
  is the only one: the detector resolves overlapping candidates internally, so
  the overlap rule in the rewriter can only be exercised with a hand-built
  pair. Its hits are not checked against the detector.
- **`expected_output`** is written by hand from `input` and `hits` by applying
  the grammar in `internal/secrets/tags.go`. It is never captured from a
  rewriter run.

## Credential material

Every credential-shaped string in these files is a synthetic canary, invented
for this corpus and matching no real account: each contains the literal
`Canary` and a run of repeated digits. They exist to be detected and removed.
The `canaries` field lists them, and the fixture test asserts that no canary
appears in the rewritten output, in any `Replacement`, in the formatted result,
or in any error string, in raw, hex or base64 form.

## Cases

| File | What it pins |
| --- | --- |
| `single_span.yaml` | one API-key hit inside a sentence |
| `multi_span.yaml` | three hits of different types in one block |
| `overlap_span.yaml` | two overlapping candidates; the larger span wins |
| `empty_input.yaml` | a zero-length turn rewrites to zero length, no error |
| `non_ascii.yaml` | Japanese and Arabic text around an embedded hit |
| `multiline.yaml` | multi-paragraph content with hits on lines 2 and 4 |
| `idempotent.yaml` | already-tagged text passes through byte-identical |
