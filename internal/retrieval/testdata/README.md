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
