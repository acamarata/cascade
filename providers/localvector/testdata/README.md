# providers/localvector/testdata — recall@k fixture provenance

`recall_fixture.json` is the ground-truth corpus `vector_test.go`'s
`TestRecallAtK` asserts exact recall against (P1-E02-W1-S03-T4).

## Generation method

- **Seed:** `20260902` (`math/rand.NewSource(20260902)`), Go's default
  (non-cryptographic) PRNG — stable across runs on the same Go version, per
  the standard library's documented `math/rand` sequence guarantee for a
  fixed source.
- **Generated:** 2026-09-02.
- **Corpus:** 60 vectors (`v00`..`v59`), dimension 8, each component drawn
  uniform `[-1, 1]` via `float32(rng.Float64()*2 - 1)`.
- **Queries:** 6 held-out vectors (`q0`..`q5`, not members of the corpus),
  same generation method, drawn from the same RNG stream immediately after
  the corpus.
- **Ground truth:** for each query, every corpus vector's cosine similarity
  against it was computed by a standalone generator function that is a
  byte-for-byte copy of `providers/localvector/query.go`'s own
  `cosineSimilarity` (float64 accumulation over the float32 inputs, cast
  back to float32 at the end) — the fixture's expected ranking is
  guaranteed to match what `FlatVectorStore.Query` itself computes, not an
  independently-rounded approximation of it. Each query's top 10 corpus IDs
  by descending score (ties, none present — see below — broken by
  ascending ID, matching `query.go`'s `sortMatches`) are recorded as
  `expected_top10`.
- **No ties at the recall boundary:** the generator asserts (and would
  panic and refuse to write the fixture if not true) that no two corpus
  vectors tie in score at the rank-5/rank-6 or rank-10/rank-11 boundary for
  any query, so `recall@5` and `recall@10` are unambiguous — a real
  boundary tie would make "the correct top-k set" not well-defined without
  also fixing a tie-break rule the test would then be asserting on the
  fixture's construction rather than on `FlatVectorStore`'s ranking.
- **Generator:** a throwaway `go run` script, not checked into this
  package (it produces only this static, frozen JSON artifact; it is not
  part of any build or test path, so it carries no Art.1 stub-gate
  obligations and isn't part of `files_scope`).

## Assertions made against this fixture

`TestRecallAtK` (`vector_test.go`) loads `recall_fixture.json`, `Upsert`s
every corpus vector into one namespace of a fresh `FlatVectorStore`
(backed by `storetest.NewMemStore`), then for every query:

- `recall@5`: the ID set of the top-5 IDs `Query` returns (`TopK: 5`)
  equals `expected_top10[:5]`'s ID set, asserted `== 1.0` — brute force is
  an exact scan, so any miss is a driver bug, never a tolerance.
- `recall@10`: the ID set of the top-10 IDs `Query` returns (`TopK: 10`)
  equals `expected_top10`'s ID set, asserted `== 1.0`.
- Because ties are absent at both boundaries (see above) and
  `FlatVectorStore.Query` uses the identical scoring formula and tie-break
  rule as the generator, both assertions also hold on exact ranked ORDER,
  not just set membership — the test checks both.
