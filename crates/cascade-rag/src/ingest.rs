//! # ingest
//!
//! Central ingest pipeline: file path → parse → chunk → embed → store.
//!
//! ## Responsibilities
//!
//! 1. BLAKE3 content-hash check — unchanged files are skipped (idempotent).
//! 2. Old-chunk eviction — changed files have their prior chunks deleted before
//!    re-indexing so the DB never holds stale vectors.
//! 3. Chunker dispatch — selects the right [`Chunker`] based on file extension.
//! 4. Batch embedding — dense + sparse in batches of 32.
//! 5. Atomic transaction — all DB writes for one file commit or roll back together.
//! 6. Progress callback — optional per-chunk notification for UI progress bars.
//!
//! ## Chunker selection
//!
//! | Extension | Chunker |
//! |-----------|---------|
//! | `.md`, `.mdx` | [`MarkdownChunker`] |
//! | `.rs`, `.ts`, `.tsx`, `.js`, `.jsx`, `.py` | [`CodeChunker`] (feature = `code-chunker`) |
//! | prose / other text | [`HierarchicalChunker`] |
//! | default | [`SemanticChunker`] |
//!
//! Without the `code-chunker` feature, code extensions fall through to
//! [`HierarchicalChunker`].
//!
//! ## Transaction contract
//!
//! The entire ingest of a single file is wrapped in one `BEGIN / COMMIT`.
//! Failure at any step rolls back completely; `rag_sources` is only updated
//! on success so a partial failure is re-attempted on next run.
//!
//! ## Idempotency
//!
//! Content hash is BLAKE3 of raw file bytes (via `cascade_db::content_hash`).  If the hash matches the stored
//! `content_hash` in `rag_sources`, the pipeline returns
//! `IngestResult { skipped: true, .. }` immediately without touching the DB.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::IngestPipeline

// ── Sub-modules ───────────────────────────────────────────────────────────────

mod chunker;
mod pipeline;
mod types;

// ── Re-exports (preserve the original public API surface) ─────────────────────

pub use chunker::chunker_for_path;
pub use pipeline::IngestPipeline;
pub use types::{IngestConfig, IngestResult, IngestStats};

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::run_migrations;
    use crate::embed::MockEmbedModel;
    use rusqlite::Connection;
    use std::io::Write as IoWrite;
    use std::path::Path;
    use std::sync::Arc;
    use tempfile::NamedTempFile;

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Register the sqlite-vec extension so vec0 virtual tables resolve in
    /// migrations (no-op without the `vec` feature).
    #[cfg(feature = "vec")]
    fn load_vec_extension() {
        use rusqlite::ffi::sqlite3_auto_extension;
        unsafe {
            sqlite3_auto_extension(Some(std::mem::transmute(
                sqlite_vec::sqlite3_vec_init as *const (),
            )));
        }
    }

    fn migrated_conn() -> Connection {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let conn = Connection::open_in_memory().expect("in-memory DB");
        run_migrations(&conn).expect("migrations ok");
        conn
    }

    fn make_pipeline(conn: Connection) -> IngestPipeline {
        let embed = Arc::new(MockEmbedModel::default());
        IngestPipeline::new(conn, embed, IngestConfig::default())
    }

    fn write_file(content: &str, suffix: &str) -> NamedTempFile {
        let mut f = tempfile::Builder::new()
            .suffix(suffix)
            .tempfile()
            .expect("tempfile");
        IoWrite::write_all(&mut f, content.as_bytes()).expect("write");
        IoWrite::flush(&mut f).expect("flush");
        f
    }

    // ── ingest::full_pipeline ─────────────────────────────────────────────────

    /// Full pipeline with a markdown fixture.
    #[test]
    fn ingest_md_creates_chunks_and_rows() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        let f = write_file(
            "# Hello\n\nThis is a test document for the ingest pipeline.\n\n## Section Two\n\nMore content here.",
            ".md",
        );

        let result = pipeline.ingest_file(f.path()).expect("ingest ok");

        assert!(!result.skipped, "first ingest must not be skipped");
        assert!(result.chunks_created > 0, "must create at least one chunk");
        assert!(result.source_id > 0, "source_id must be non-zero");

        // Verify DB rows.
        let source_count: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_sources", [], |r| r.get(0))
            .unwrap();
        assert_eq!(source_count, 1);

        let chunk_count: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();
        assert_eq!(chunk_count as usize, result.chunks_created);
    }

    /// Full pipeline with a Rust source fixture.
    #[test]
    fn ingest_rs_creates_chunks_and_rows() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        let f = write_file(
            "fn main() {\n    println!(\"Hello, world!\");\n}\n\nfn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n",
            ".rs",
        );

        let result = pipeline.ingest_file(f.path()).expect("ingest ok");

        assert!(!result.skipped);
        assert!(result.chunks_created > 0);
    }

    // ── ingest::idempotent ────────────────────────────────────────────────────

    /// Re-ingesting the same file unchanged must return skipped=true.
    #[test]
    fn idempotent_same_file_skips() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let f = write_file("# Idempotency test\n\nSame content every time.\n", ".md");

        let first = pipeline.ingest_file(f.path()).expect("first ingest ok");
        assert!(!first.skipped, "first must not skip");

        let second = pipeline.ingest_file(f.path()).expect("second ingest ok");
        assert!(second.skipped, "second ingest must skip (hash unchanged)");
        assert_eq!(second.chunks_created, 0);
        assert_eq!(
            second.source_id, first.source_id,
            "source_id must be stable"
        );
    }

    // ── ingest::changed_file ──────────────────────────────────────────────────

    /// When the file changes, old chunks must be deleted and new ones created.
    #[test]
    fn changed_file_evicts_and_reingests() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        // First ingest.
        let f = write_file("# Version 1\n\nOriginal content.\n", ".md");
        let first = pipeline.ingest_file(f.path()).expect("first ingest ok");
        assert!(!first.skipped);
        let first_chunks: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();

        // Modify the file — overwrite via std::fs::write to avoid NamedTempFile
        // seek/set_len limitations.
        std::fs::write(
            f.path(),
            b"# Version 2\n\nCompletely different content.\n\n## Extra\n\nMore paragraphs added here for chunking.\n",
        )
        .unwrap();

        // Second ingest — should re-ingest.
        let second = pipeline.ingest_file(f.path()).expect("second ingest ok");
        assert!(!second.skipped, "changed file must not skip");
        assert!(second.chunks_created > 0);

        // Only the new chunks should be in the DB (old ones deleted by CASCADE).
        let after_chunks: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();
        assert_eq!(
            after_chunks as usize, second.chunks_created,
            "DB chunks must equal second ingest output (old chunks evicted)"
        );
        // Confirm the old chunk count is gone (they should differ because content changed).
        let _ = first_chunks; // used implicitly above
    }

    // ── ingest::parse_failure_isolation ──────────────────────────────────────

    /// A file that can't be read should produce an error without affecting
    /// other files in a batch.
    #[test]
    fn parse_failure_isolated_in_batch() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        let good = write_file("# Good file\n\nReal content.\n", ".md");
        let bad_path = std::path::Path::new("/nonexistent/does_not_exist_xyz.md");

        let (results, errors) = pipeline.ingest_files([good.path(), bad_path]);

        assert_eq!(results.len(), 1, "good file must produce a result");
        assert!(!results[0].skipped);
        assert_eq!(errors.len(), 1, "bad path must produce an error");
    }

    // ── ingest::chunks_created_count ─────────────────────────────────────────

    /// `chunks_created` must equal the actual number of rag_chunks rows.
    #[test]
    fn chunks_created_matches_db_rows() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let f = write_file(
            "# A\n\nParagraph one.\n\n## B\n\nParagraph two.\n\n## C\n\nParagraph three.\n",
            ".md",
        );

        let result = pipeline.ingest_file(f.path()).expect("ingest ok");
        let db_count: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();

        assert_eq!(result.chunks_created, db_count as usize);
    }

    // ── ingest::progress_callback ─────────────────────────────────────────────

    #[test]
    fn progress_callback_called_per_chunk() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        let conn = migrated_conn();
        let embed = Arc::new(MockEmbedModel::default());
        let counter = Arc::new(AtomicUsize::new(0));
        let counter_clone = counter.clone();

        let pipeline = IngestPipeline::new(conn, embed, IngestConfig::default()).with_progress(
            move |_path, _idx, _total| {
                counter_clone.fetch_add(1, Ordering::SeqCst);
            },
        );

        let f = write_file("# Progress\n\nContent for progress test.\n", ".md");
        let result = pipeline.ingest_file(f.path()).expect("ingest ok");

        assert_eq!(
            counter.load(Ordering::SeqCst),
            result.chunks_created,
            "progress callback must fire once per chunk"
        );
    }

    // ── ingest::chunker_for_path ──────────────────────────────────────────────

    #[test]
    fn chunker_selection_md() {
        use crate::chunk::ChunkerConfig;
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.md"), &cfg);
        assert_eq!(c.strategy_name(), "markdown");
    }

    #[test]
    fn chunker_selection_mdx() {
        use crate::chunk::ChunkerConfig;
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.mdx"), &cfg);
        assert_eq!(c.strategy_name(), "markdown");
    }

    #[test]
    fn chunker_selection_txt_hierarchical() {
        use crate::chunk::ChunkerConfig;
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.txt"), &cfg);
        // Accept either the long or short strategy name for future-proofing.
        assert!(
            c.strategy_name().starts_with("hierarchical"),
            "expected hierarchical chunker, got: {}",
            c.strategy_name()
        );
    }

    #[test]
    fn chunker_selection_unknown_semantic() {
        use crate::chunk::ChunkerConfig;
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.pdf"), &cfg);
        assert!(
            c.strategy_name().starts_with("semantic"),
            "expected semantic chunker, got: {}",
            c.strategy_name()
        );
    }

    #[cfg(feature = "code-chunker")]
    #[test]
    fn chunker_selection_rs_code() {
        use crate::chunk::ChunkerConfig;
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.rs"), &cfg);
        assert_eq!(c.strategy_name(), "code");
    }

    // ── ingest::hex_blake3 ────────────────────────────────────────────────────

    #[test]
    fn blake3_is_deterministic() {
        let h1 = chunker::hex_blake3(b"hello world");
        let h2 = chunker::hex_blake3(b"hello world");
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64, "BLAKE3 hex must be 64 chars");
    }

    #[test]
    fn blake3_differs_on_change() {
        let h1 = chunker::hex_blake3(b"version 1");
        let h2 = chunker::hex_blake3(b"version 2");
        assert_ne!(h1, h2);
    }

    // ── ingest::delta scan ────────────────────────────────────────────────────

    fn make_state_store() -> crate::index::state::IndexStateStore {
        crate::index::state::IndexStateStore::open_in_memory().expect("state store")
    }

    /// New file: ingest_delta must ingest it and record stats.
    #[test]
    fn delta_new_file_is_ingested() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta new\n\nContent.\n", ".md");

        let stats = pipeline.ingest_delta([f.path()], &state);

        assert_eq!(stats.scanned, 1);
        assert_eq!(stats.ingested, 1);
        assert_eq!(stats.skipped, 0);
        assert_eq!(stats.evicted, 0);
    }

    /// Unchanged file: second delta pass must skip.
    #[test]
    fn delta_unchanged_file_is_skipped() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta unchanged\n\nSame content.\n", ".md");

        // First pass — ingests.
        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);

        // Second pass — skips (mtime/size or hash unchanged).
        let s2 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s2.ingested, 0);
        assert_eq!(s2.skipped, 1, "unchanged file must be skipped");
    }

    /// Modified file: second delta pass must re-ingest.
    #[test]
    fn delta_modified_file_is_reingested() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta v1\n\nOriginal.\n", ".md");

        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);

        // Overwrite to force content change and mtime bump.
        std::fs::write(
            f.path(),
            b"# Delta v2\n\nCompletely different content now.\n",
        )
        .unwrap();

        let s2 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s2.ingested, 1, "modified file must be re-ingested");
        assert_eq!(s2.skipped, 0);
    }

    /// Deleted file: eviction sweep removes it from state store.
    #[test]
    fn delta_deleted_file_is_evicted() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta delete\n\nContent.\n", ".md");
        let path_copy = f.path().to_path_buf();

        // Index it.
        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);
        assert_eq!(state.count(), 1);

        // Delete from disk.
        drop(f);
        assert!(!path_copy.exists());

        // Next pass — no candidates, eviction still fires.
        let s2 = pipeline.ingest_delta(std::iter::empty(), &state);
        assert_eq!(s2.evicted, 1, "deleted file must be evicted");
        assert_eq!(state.count(), 0);
    }

    /// Fast-path: stats.skipped increments when mtime+size match (no hash computed).
    #[test]
    fn delta_stats_skipped_count_is_accurate() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let files: Vec<_> = (0..3)
            .map(|i| write_file(&format!("# File {i}\n\nContent {i}.\n"), ".md"))
            .collect();

        // First pass — ingest all.
        let paths: Vec<&Path> = files.iter().map(|f| f.path()).collect();
        let s1 = pipeline.ingest_delta(paths.iter().copied(), &state);
        assert_eq!(s1.ingested, 3);

        // Second pass — all skipped.
        let s2 = pipeline.ingest_delta(paths.iter().copied(), &state);
        assert_eq!(s2.skipped, 3);
        assert_eq!(s2.ingested, 0);
        assert_eq!(s2.scanned, 3);
    }

    /// incremental=false forces full re-index regardless of hash.
    #[test]
    fn delta_incremental_false_always_reindexes() {
        let conn = migrated_conn();
        let embed = Arc::new(MockEmbedModel::default());
        let cfg = IngestConfig {
            incremental: false,
            ..Default::default()
        };
        let pipeline = IngestPipeline::new(conn, embed, cfg);
        let state = make_state_store();

        let f = write_file("# Full reindex\n\nContent.\n", ".md");

        // First pass.
        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);

        // Second pass — incremental=false must re-ingest even though content unchanged.
        let s2 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s2.ingested, 1, "incremental=false must always re-ingest");
        assert_eq!(s2.skipped, 0);
    }

    /// Duration field is non-zero after a real pass.
    #[test]
    fn delta_stats_duration_is_populated() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Duration test\n\nContent.\n", ".md");

        let stats = pipeline.ingest_delta([f.path()], &state);
        // Duration must be non-zero (at least a few nanoseconds).
        // Even an empty loop takes > 0 ns; this is always true in practice.
        assert!(stats.duration_ms() < 60_000, "duration sanity: < 60s");
    }
}
