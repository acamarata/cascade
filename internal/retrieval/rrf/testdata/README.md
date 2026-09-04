# internal/retrieval/rrf/testdata: fixture provenance (Art.2 §2)

## v1-goldens/ (RRF fusion goldens)

Each file is one fusion case harvested from the archived v1 implementation
of Reciprocal Rank Fusion, which is the real counterpart for this package.
Nothing here was produced by running the Go code under test: the input
lists and the expected fused ordering both come from v1's own assertions,
so a golden cannot silently record whatever this implementation happens to
do.

- **Tool:** `cascade-v1`, crate `cascade-rag`, module
  `retrieve::rrf` (`merge.rs` for the algorithm, `tests.rs` for the
  asserted vectors). Read-only per `.claude/planning/p1/ARCHIVE-MAP.md`;
  no v1 source is reused, only its stated inputs and expected outputs.
- **Version tag:** `archive/p9-integration`.
- **Date harvested:** 2026-08-31.

### What each field means

| Field | Meaning |
| --- | --- |
| `k` | The RRF smoothing constant the case was asserted at. |
| `lists[].strategy` | v1's own leg label for that list, kept verbatim. |
| `lists[].weight` | v1's per-list weight multiplier. |
| `lists[].hits` | Chunk ids in rank order, best first. |
| `expected[]` | The fused order v1 asserts, with the fused score before normalization and the legs that contributed. |

`expected` is ordered: the file's order IS the asserted ranking.

### The one transformation applied

v1 identified chunks by 64-bit integer and broke score ties by ascending
integer id. v2 chunk ids are strings (content-addressed digests), and ties
break by ascending string id. Sorting `"100"` and `"50"` as strings would
reverse v1's asserted order for the tie case, so every harvested id is
written zero-padded to four digits (`50` becomes `"0050"`). Padding makes
the string order agree with v1's integer order, which keeps the asserted
ranking intact instead of quietly changing it to suit the new id type.
Real v2 ids are fixed-width hex digests, where the same agreement holds
without padding.

Nothing else was changed. The scores are v1's own expressions evaluated to
`float64`: for example `known_vector_fusion` asserts `1/61 + 1/62` for each
of its two chunks, and that is the value recorded.

### Cases

| File | What v1 asserts |
| --- | --- |
| `known_vector_fusion.json` | Two legs holding mirrored ranks fuse to exactly equal scores; the tie breaks on the id. |
| `source_weights.json` | Doubling a leg's weight doubles its contribution, and the heavier leg's hit outranks the lighter leg's. |
| `tie_break_determinism.json` | Two mirrored two-hit legs tie exactly; the lower id ranks first. |
| `one_empty_one_non_empty.json` | An empty leg contributes nothing and does not disturb the other leg's order. |
| `single_list_passthrough.json` | A lone leg degenerates to its own rank order. |
| `provenance_sources_hit.json` | A chunk found by both legs sums both contributions; a chunk found by one records only that leg. |
| `three_list_fusion.json` | The N-leg path: a chunk in all three legs outranks chunks in fewer. |

### What is not harvested

v1 applied normalization per channel, before ranking, and left it disabled
by default. This ticket normalizes the fused set instead, after fusion, so
there is no v1 expected output for the normalized score to be compared
against. The goldens therefore carry the fused score before normalization,
which is the value the ranking is decided by, and the normalization of that
set is asserted separately against its own stated rules in `fuse_test.go`.
