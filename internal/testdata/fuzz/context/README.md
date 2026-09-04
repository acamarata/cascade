# FuzzMergeTiers seed corpus

Hand-authored seeds for `FuzzMergeTiers`
(`internal/context/fuzz_merge_test.go`), which drives the level-2-heading
section splitter and the higher-tier-wins precedence pass in
`internal/context/merge.go`.

Location per 06-FORGE-SPEC.md §5.7 (curated corpora live under
`internal/testdata/fuzz/<Target>/`, never at the repo root); the parent
`../README.md` explains how this differs from Go's own
`testdata/fuzz/<FuzzName>/` failure corpus.

Provenance: hand-authored for this ticket (P1-E05-W2-S08-T2), not harvested
from an external artifact. The real-world corpus this target is also
exercised against lives at
`internal/context/testdata/v1-goldens/merge/` and is harvested from v1 —
see that directory's README for its provenance.

| Seed | The case it covers |
|---|---|
| `seed-basic-sections.md` | the ordinary shape: a preamble followed by two level-2 sections |
| `seed-fenced-quoted-headings.md` | `## ` lines inside ``` and `~~~` fences, which must stay content rather than cut a section in half |
| `seed-degenerate-headings.md` | a heading repeated within one record, a bare `##` with no text, a `###` level-3 heading, and a heading with trailing whitespace |
| `seed-empty-section-body.md` | a heading defined with an empty body — the case that must still win its heading and suppress lower tiers |
| `seed-invalid-utf8-and-nul.bin` | content that is not valid UTF-8 and contains a NUL byte — the fail-closed rejection path |

Every seed is fed to the target in more than one tier slot, so each one is
exercised both as the winning (higher) tier and as the losing (lower) one.
