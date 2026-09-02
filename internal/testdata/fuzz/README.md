# Shared fuzz corpus home

This directory is the single, shared home for hand-seeded fuzz corpus
entries across the whole module (06-FORGE-SPEC.md §5.7: "corpora live at
`internal/testdata/fuzz/...` (never repo root)" — clean root per
12-QUALITY-CONSTITUTION.md Art.10.1). It is distinct from Go's own
per-package `testdata/fuzz/<FuzzName>/` corpus directories that
`go test -fuzz` creates automatically next to each `FuzzXxx` target —
those stay where Go puts them, inside their owning package. This directory
holds corpus seeds curated by hand or harvested from real-world inputs
(Article 2 real-counterpart fixtures), organized as:

```
internal/testdata/fuzz/<FuzzTargetName>/<seed-file>
```

No `FuzzXxx` target exists yet in the tree (P1-E01-W1-S01-T3 ships the CI
lane and this location only). Targets arrive with the parser/decoder
tickets that owe one per 06-FORGE-SPEC.md §5 rule 7 — each such ticket
adds its `FuzzXxx` function under its own package AND, if it curates seed
inputs beyond `f.Add(...)` literals, a subdirectory here named for the
target. `.github/workflows/fuzz-nightly.yml` runs `go test -fuzz=. -fuzztime=Ns`
against every `FuzzXxx` target that exists in the tree each night; with
zero targets it is green-empty by design, not a stub (Art.1 — this is
infrastructure whose consumers arrive later, per §D-38's identical pattern
for the bench-budget harness).

Provenance (Article 2): when a seed file here is harvested from a real
external artifact rather than hand-authored, record its tool/version/date
in this file at the time it is added.
