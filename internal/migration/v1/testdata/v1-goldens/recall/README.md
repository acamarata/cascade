# v1 recall fixture provenance

The three files in `corpus/` (`c1.md`, `c2.md`, `c3.md`) are byte-for-byte
copies of three real chunk rows read directly from a real, self-hosted v1
Cascade installation's own retrieval index: the `rag_chunks` table of
`../cascade-v1/.cascade/index/cascade.db` (380 real indexed sources, 4233
real indexed chunks — this repository's own v1 predecessor indexing its
own working tree while in daily use). They were harvested on 2026-09-13 by
a direct `sqlite3` read of that database (no v1 binary was built or run;
the schema — `rag_chunks`, `rag_sources`, `rag_fts5` — is v1's own, read
as data). Each chunk was selected for containing no personal names, no
absolute machine paths, and no ATX heading line, so re-chunking it through
v2's `MarkdownChunker` (which splits only on ATX headings) reproduces it as
a single chunk, byte-for-byte.

`queries.json` holds one query per chunk, built by the same known-item
method `cascade-v1/crates/cascade-rag/tests/retrieval_eval.rs` documents
and uses: each query is a small set of distinctive terms drawn directly
from its target chunk's own real text, so the ground truth is verifiable
by inspection rather than hand-labelled. `expected_content_hashes` is the
BLAKE3-256 hex digest of each chunk's exact bytes, computed independently
of both the v1 and the v2 pipeline (both are documented to hash chunk
content with plain BLAKE3-256: `cascade-v1/crates/cascade-db/src/hash.rs`
and this tree's `internal/retrieval/id.go`), so a passing
`TestParityChecker_GoldenCoverage` proves the rebuilt v2 index answers each
query with the same real content v1 indexed, not merely with whatever v2
happens to produce.

## Why not the full 4233-chunk v1 index

`cascade-v1/crates/cascade-rag/tests/retrieval_eval.rs` itself documents
why a chunk-id-based golden set cannot be built from that whole index
verbatim: "Ground truth is the SOURCE PATH, not a chunk id: chunk ids are
assigned at ingest and would change on every rebuild, rotting the fixture
immediately." The three chunks here sidestep that instability by using
each chunk's own content as its own one-file corpus source, so ChunkID
(content-addressed, chunker-boundary-independent for a single-chunk file)
is stable across both pipelines by construction, not by coincidence. This
ticket's journal (`P1-E26-W10-S53-T2.md`) records the full reasoning and
the resource constraints that ruled out compiling and running the real v1
Rust workspace to harvest a larger set.

## Divergence ledger

`divergence-ledger.yaml` is currently empty: all three golden queries
report exact 100% top-k coverage. See its own header comment for the
tripwire pattern R-14.82 requires of any future divergence.
