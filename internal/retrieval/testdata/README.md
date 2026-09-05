# internal/retrieval/testdata — fixture provenance (Art.2 §2)

## v1-goldens/ (golden chunk-boundary fixtures)

Each file under `v1-goldens/` is a self-contained golden: `input` holds a
real file's content copied verbatim (never authored for this test), and
`chunks` holds this ticket's `MarkdownChunker`/`CodeChunker` output over
that exact input, checked into git as the regression baseline `chunk_test.go`
asserts against in non-update mode. Per this ticket's SPEC-SALVAGE note
("harvest chunk boundary examples as golden inputs only" — v1 is Rust and
has no directly reusable Go golden format), what is harvested from v1 is
the INPUT file, never v1's own expected output: there is no v1 chunker
whose Go-shaped boundary/ID output could be reused directly, so `chunks`
is this ticket's own implementation's output over a real-world input,
hand-verified against that input's actual heading/declaration structure
before being checked in (each file's chunk count matches a manual count of
the input's real ATX headings or top-level declarations — see below).

- **`md_chunk_basic.json`** — input: this repo's own top-level `README.md`
  (tool: n/a, real source file copied verbatim; date: 2026-09-03). Two ATX
  headings (`# Cascade`, `## Building from source`) yield two chunks;
  verified by eye against the real file's two-heading structure.
- **`go_chunk_basic.json`** — input: this repo's own `pkg/cascade/kinds.go`
  (tool: n/a, real source file copied verbatim; date: 2026-09-03). Six
  top-level declarations (`type Kind`, the frozen `const` block, `var
  kindNames`, `func String`, `func Valid`, `func AllKinds`) yield six
  chunks via `go/ast`; verified by eye against the real file's six
  declarations.
- **`other_lang_chunk.json`** — input: `acamarata/cascade-v1` (archived,
  read-only per `.claude/planning/p1/ARCHIVE-MAP.md`) at
  `crates/cascade-rag/src/ingest/chunker.rs`, harvested from the local
  archive clone `../cascade-v1/` (tool: n/a, real source file copied
  verbatim from the archive clone; date: 2026-09-03). This is v1's own
  chunker-selection helper, taken as input ONLY (Rust) — no v1 logic or
  source is reused by `internal/retrieval`, which reimplements
  markdown/code chunking from scratch per SPEC-SALVAGE. Three top-level
  Rust items (`pub fn chunker_for_path`, `pub(super) fn hex_blake3`,
  `pub(super) fn file_mtime`) plus a leading file-doc-comment/imports
  preamble yield four chunks via the regex fallback's `rust` pattern;
  verified by eye against the real file's three functions.

## v1-goldens/fts5_queries.json (full-text ranking golden)

`fts5_queries.json` is a different shape from the chunk goldens above and
is checked by `TestGoldenFTS5`. It is a KNOWN-ITEM retrieval fixture: each
query is a distinctive passage lifted out of one document, so the correct
answer is the document the passage came from, and the recorded expectation
is that document's SOURCE PATH rather than any engine's ranked output.
Nothing in it is this implementation's own output, so the golden cannot
pass by agreeing with itself.

- **queries** (15) come from `acamarata/cascade-v1` (archived, read-only
  per `.claude/planning/p1/ARCHIVE-MAP.md`) at
  `crates/cascade-rag/tests/fixtures/eval/queries.jsonl`, the dataset that
  repo's own `scripts/gen-eval-corpus.py` generated from its 89 public
  documents (dataset `cascade-docs-known-item` v1.0.0, generated from
  commit `6c7ec3e35b0f994d0a488b2c35a678fbe59f4c42`). Only the queries
  whose ground-truth document is one of the five documents below were
  taken. Harvested from the local archive clone `../cascade-v1/` on
  2026-09-05.
- **documents** (5) are that same repo's own files, copied verbatim:
  `docs/target-dir-hygiene.md`, `docs/coverage-baseline-2026-08-23.md`,
  `.github/wiki/Scheduler.md`, `.github/wiki/Plugin-Development.md`,
  `.github/wiki/RAG-Pipeline.md`. Input only, per SPEC-SALVAGE: no v1
  logic or source is reused, and v1's Rust FTS5 ranker is not consulted
  (it has no reusable Go golden form, the same reason recorded for the
  chunk goldens above).

### Engine provenance for this fixture (Art.2 §2)

- **Storage engine**: SQLite through `providers/sqlite` (the pure-Go
  `modernc.org/sqlite` driver, version as pinned in `go.mod`), opened by
  the test on a real database file under `t.TempDir()`, never a mock
  store.
- **Tokenizer**: exact-token, Unicode-lowercased ASCII alphanumeric runs,
  bounded at 64 characters (`internal/retrieval.tokenize`). This is
  NARROWER than the `porter unicode61` tokenizer the ticket contract names:
  Porter stemming is deliberately not implemented, because a hand-written
  stemmer would be this package's own approximation of SQLite's asserted
  against itself. Matching is therefore never wider than a stemmed index
  would be. See `fts5_schema.go` for the recorded contract deviation on the
  storage layout, and this ticket's journal for both sides quoted.
- **Ranking**: BM25 with k1 = 1.2, b = 0.75, inverse document frequency in
  the non-negative "plus one" form, ties broken by ascending chunk id.
- **Captured**: 2026-09-05.

## fuzz corpus

`FuzzChunk`'s seed corpus does NOT live in this directory. Per
`internal/testdata/fuzz/README.md`'s repo-wide convention (06-FORGE-SPEC.md
§5 rule 7: fuzz corpora live at the single shared `internal/testdata/fuzz/`
home, never beside the owning package), the corpus lives at
`internal/testdata/fuzz/FuzzChunk/` — see that directory's own README.md
for the seed-by-seed provenance record. This ticket's contract names
`internal/retrieval/testdata/fuzz/FuzzChunk/` as the seed path; the tree's
actual, already-established convention (every other `FuzzXxx` target in
this module: `internal/events`, `internal/rpc`, `internal/mcp`,
`internal/events/scheduler`) puts every corpus under the shared location
instead, so `FuzzChunk` follows the tree rather than the contract text
here.
