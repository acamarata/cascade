# Acceptance fixtures — topic auto-filing (P1-E21-W5-S46-T5)

## Status: no fixture files live in this directory

This directory exists to satisfy `files_scope.add` for the Epic U
acceptance ticket (P1-E21-W5-S46-T5). It deliberately holds no `*.json`
records. Two different fixture sources are used by
`acceptance_autofile_test.go`, and neither one is a file under this path:

| Source | Where | What it proves | Real or synthetic |
|---|---|---|---|
| The real S-45.T1 owner corpus | `../corpus/` | Art.2's binding claim: at least one acceptance test asserts against the real owner-scenario labeled fixture corpus. | Real, but **not yet delivered** — see `../corpus/README.md`. `acceptanceRealOwnerCorpus` loads this path with the real `LoadCorpus` and `t.Skip`s with a named citation while it is empty, rather than fabricating a pass. |
| `syntheticCorpus()` | `segmenter_harness_test.go` (P1-E21-W5-S45-T2), in-memory, no file on disk | The auto-filing MECHANISM: `Segment` → `AutoThreader.Route` → a real sqlite `ThreadStore` produces exactly 4 discrete topic threads over the closed `{alpha, beta, gamma, delta}` vocabulary, and the boundary-F1/assignment-accuracy floors are cleared. | **Openly synthetic**, already reviewed and shipped by S-45.T2. Reused here unmodified rather than re-invented, per this repo's reuse-before-new-unit rule. |

## Why no new fixture file was added here

Adding a third, acceptance-specific corpus file under this directory would
be exactly the "self-authored dialect" Art.2 forbids as a substitute for
the real owner corpus: it would look like fixture provenance without being
either the real corpus or the pre-existing, already-reviewed synthetic
fixture. Provenance for the two sources actually used is above; there is
nothing else to document here.

## When the owner corpus lands

Once `../corpus/` holds real records, `acceptanceRealOwnerCorpus` (in
`acceptance_autofile_test.go`) runs its scoring branch instead of skipping
— no test code change is required, since the skip path only fires on an
empty or missing directory. Update `../corpus/README.md`'s own provenance
fields (source tool, version, capture date, sanitization method, label
schema) at that time; this file's job is done once that happens.
