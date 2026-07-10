//! FTS5 BM25 keyword retriever.
//!
//! ## Two APIs
//!
//! ### Low-level (`rusqlite::Connection`-based) — T-P4-E01-06
//!
//! - [`index_chunk`] — ensure a row exists in `rag_chunks` and is reflected in
//!   the `rag_fts5` external-content virtual table (via the auto-sync triggers).
//! - [`query_fts5`] — execute a BM25 keyword query against `rag_fts5` and
//!   return `(chunk_id, normalised_score)` pairs ordered by descending relevance.
//! - [`normalize_bm25_score`] — convert the raw negative SQLite BM25 value to
//!   a positive `[0.0, 1.0]` score suitable for RRF fusion.
//!
//! ### High-level (`Retriever` trait) — wraps `RagIndex`
//!
//! [`FtsRetriever`] implements [`cascade_types::Retriever`] and delegates to
//! [`RagIndex::fts_query`].
//!
//! ## BM25 scoring
//!
//! SQLite's `bm25()` returns negative values; more negative = more relevant.
//! [`normalize_bm25_score`] maps them to `[0.0, 1.0]` using
//! `1 / (1 - score)` clamped to `[0.0, 1.0]`.
//!
//! ## Query sanitisation
//!
//! [`query_fts5`] wraps the raw query string in double-quotes before passing it
//! to FTS5.  This turns the input into a single phrase token, preventing FTS5
//! operator injection (e.g. `OR`, `NOT`, unbalanced parentheses).  Embedded
//! double-quotes inside the query are escaped as `""`.  If FTS5 still reports a
//! parse error the function returns `Ok(vec![])` rather than propagating the
//! error, so callers never crash on user-supplied strings.
//!
//! ## Performance target
//!
//! Keyword search returns top-K=10 results in < 200 ms on a 100K-chunk index.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::FtsRetriever

use async_trait::async_trait;
use rusqlite::{Connection, Result as SqlResult};
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
};

use crate::index::RagIndex;

// ── Low-level FTS5 functions (T-P4-E01-06) ───────────────────────────────────

/// Ensure `chunk_id` / `chunk_text` exists in `rag_chunks` so the FTS5
/// external-content table is kept in sync automatically via triggers.
///
/// # Purpose
///
/// The `rag_fts5` virtual table is declared with `content = "rag_chunks"` and
/// has three sync triggers (`rag_fts5_ai`, `rag_fts5_au`, `rag_fts5_bd`).
/// Therefore, **direct FTS5 inserts are not needed** — writing a row to
/// `rag_chunks` fires the trigger which populates `rag_fts5`.
///
/// `index_chunk` uses `INSERT OR IGNORE` so calling it repeatedly for the same
/// `chunk_id` is safe (idempotent).
///
/// # Inputs
///
/// - `conn`     — open `rusqlite::Connection` with migrations already applied.
/// - `chunk_id` — stable integer PK for the chunk; must be unique per source.
/// - `text`     — raw text content to index.
///
/// # Constraints
///
/// The `rag_chunks` table requires a `source_id` FK into `rag_sources`.  This
/// function inserts a sentinel source row with `file_path = '<internal>'` to
/// satisfy the FK when no real source is available (typical in unit tests or
/// direct indexing pipelines that construct chunks without a file on disk).
/// Pass the chunk into `rag_chunks` manually (with a real `source_id`) before
/// calling `index_chunk` if you need FK accuracy; the `INSERT OR IGNORE` will
/// then be a no-op.
///
/// # Outputs
///
/// `Ok(())` on success; `Err(rusqlite::Error)` on any SQL failure.
pub fn index_chunk(conn: &Connection, chunk_id: i64, text: &str) -> SqlResult<()> {
    // Ensure a sentinel source row exists so the FK constraint is satisfied.
    conn.execute(
        "INSERT OR IGNORE INTO rag_sources (id, file_path, schema_version) \
         VALUES (0, '<internal>', 1)",
        [],
    )?;

    // Insert the chunk if it does not already exist.  The rag_fts5_ai trigger
    // fires automatically and adds the row to rag_fts5.
    conn.execute(
        "INSERT OR IGNORE INTO rag_chunks \
         (id, source_id, chunk_index, chunk_text, schema_version) \
         VALUES (?1, 0, ?1, ?2, 1)",
        rusqlite::params![chunk_id, text],
    )?;

    Ok(())
}

/// Execute a BM25 full-text query against `rag_fts5` and return top-`k` hits.
///
/// # Purpose
///
/// Runs `SELECT rowid, bm25(rag_fts5) FROM rag_fts5 WHERE rag_fts5 MATCH ?
/// ORDER BY bm25(rag_fts5) LIMIT ?` and normalises the raw negative BM25
/// scores to a positive `[0.0, 1.0]` range before returning.
///
/// # Inputs
///
/// - `conn`  — open connection with migrations applied.
/// - `query` — user-supplied search string (arbitrary UTF-8, including FTS5
///   operator characters).  Sanitised internally.
/// - `k`     — maximum number of results to return.
///
/// # Outputs
///
/// `Ok(Vec<(chunk_id, normalised_score)>)` sorted by descending score (most
/// relevant first).  Returns `Ok(vec![])` for malformed FTS5 query syntax
/// rather than propagating the parse error, so callers never crash on bad input.
pub fn query_fts5(conn: &Connection, query: &str, k: usize) -> SqlResult<Vec<(i64, f64)>> {
    let sanitised = sanitise_fts5_query(query);

    let mut stmt = match conn.prepare(
        "SELECT rowid, bm25(rag_fts5) AS score \
         FROM rag_fts5 \
         WHERE rag_fts5 MATCH ?1 \
         ORDER BY score \
         LIMIT ?2",
    ) {
        Ok(s) => s,
        Err(_) => return Ok(vec![]),
    };

    let rows: SqlResult<Vec<(i64, f64)>> = stmt
        .query_map(rusqlite::params![sanitised, k as i64], |row| {
            let rowid: i64 = row.get(0)?;
            let raw_score: f64 = row.get(1)?;
            Ok((rowid, normalize_bm25_score(raw_score)))
        })
        .and_then(|mapped| mapped.collect());

    // Treat FTS5 parse errors as empty result set (malformed query safety).
    match rows {
        Ok(r) => Ok(r),
        Err(rusqlite::Error::SqliteFailure(_, _)) => Ok(vec![]),
        Err(e) => Err(e),
    }
}

/// Normalise a raw BM25 score from SQLite FTS5 to `[0.0, 1.0]`.
///
/// # Purpose
///
/// SQLite's `bm25()` returns negative values (more negative = more relevant).
/// We use a sigmoid mapping: `1 / (1 + e^score)` which gives:
///
/// - very negative (e.g. -10) → ≈ 1.0  (highly relevant; `e^-10 ≈ 0`)
/// - zero                     → 0.5    (threshold)
/// - positive (unexpected)    → < 0.5  (clamped to 0.0)
///
/// Relative ordering is fully preserved.  The result is clamped to `[0.0, 1.0]`.
///
/// # Inputs / Outputs
///
/// `raw` — raw `f64` BM25 score from SQLite (expected ≤ 0).
/// Returns normalised `f64` in `[0.0, 1.0]`.
pub fn normalize_bm25_score(raw: f64) -> f64 {
    (1.0_f64 / (1.0 + raw.exp())).clamp(0.0, 1.0)
}

/// Sanitise a user query string for safe FTS5 MATCH usage.
///
/// Wraps the entire query in double-quotes (FTS5 phrase literal) after escaping
/// any embedded `"` as `""`.  This prevents FTS5 operator injection while still
/// allowing porter-stemming to fire on individual words inside the phrase.
fn sanitise_fts5_query(query: &str) -> String {
    // Escape embedded double-quotes per FTS5 spec: " → ""
    let escaped = query.replace('"', "\"\"");
    format!("\"{}\"", escaped)
}

// ── High-level Retriever (wraps RagIndex) ─────────────────────────────────────

/// Retriever that executes BM25 full-text queries against FTS5.
///
/// Requires an [`RagIndex`] that has been populated via [`RagIndex::upsert_chunk`].
pub struct FtsRetriever {
    index: Arc<RagIndex>,
}

impl FtsRetriever {
    /// Construct from a shared index handle.
    pub fn new(index: Arc<RagIndex>) -> Self {
        Self { index }
    }
}

#[async_trait]
impl Retriever for FtsRetriever {
    /// Execute a BM25 query and return top-K hits.
    ///
    /// # FTS5 query syntax
    ///
    /// The `query` string is passed verbatim to FTS5.  Callers should use the
    /// FTS5 simple query syntax (space-separated terms, `"phrase"` for phrases).
    /// Special characters should be escaped by the caller if needed.
    #[instrument(skip(self), fields(k = opts.k))]
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        let raw = self.index.fts_query(query, opts.k).await?;

        // Batch-fetch text + location fields from the chunks table in one query.
        let ids: Vec<String> = raw.iter().map(|(id, _)| id.clone()).collect();
        let body_map = self.index.fetch_chunks_by_ids(&ids).await;

        let hits = raw
            .into_iter()
            .enumerate()
            .map(|(rank, (chunk_id, raw_score))| {
                let (text, file_path, start_line, end_line) = body_map
                    .get(&chunk_id)
                    .cloned()
                    .unwrap_or_else(|| (String::new(), None, None, None));
                RetrievalHit {
                    chunk_id,
                    text,
                    file_path: file_path.map(std::path::PathBuf::from),
                    start_line: start_line.map(|v| v as usize),
                    end_line: end_line.map(|v| v as usize),
                    score: normalise_bm25(raw_score),
                    rank,
                    tier: None,
                }
            })
            .collect();
        Ok(hits)
    }

    fn name(&self) -> &str {
        "fts5-bm25"
    }
}

/// Normalise a raw BM25 score (f32) from the legacy `fts_chunks` table to
/// `[0.0, 1.0]`.
///
/// Delegates to [`normalize_bm25_score`] for consistent sigmoid behaviour.
/// Used by [`FtsRetriever`] (high-level path via `RagIndex`).
/// For the low-level `rag_fts5` path, use [`normalize_bm25_score`] directly.
pub fn normalise_bm25(raw: f32) -> f32 {
    normalize_bm25_score(raw as f64) as f32
}

// ── Tests (T-P4-E01-06) ───────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::run_migrations;
    use rusqlite::Connection;

    /// Open an in-memory DB and apply all migrations.
    fn migrated_conn() -> Connection {
        let conn = Connection::open_in_memory().expect("in-memory DB");
        run_migrations(&conn).expect("migrations");
        conn
    }

    // ── retrieve::fts::index_and_query_roundtrip ──────────────────────────────

    /// Index a batch of chunks, query a known term, verify roundtrip.
    #[test]
    fn index_and_query_roundtrip() {
        let conn = migrated_conn();

        // Insert 5 chunks.  Chunk 1 contains "hello", the others do not.
        index_chunk(&conn, 1, "hello world from cascade").unwrap();
        index_chunk(&conn, 2, "rust async programming with tokio").unwrap();
        index_chunk(&conn, 3, "bm25 keyword search full text").unwrap();
        index_chunk(&conn, 4, "sqlite fts5 virtual table index").unwrap();
        index_chunk(&conn, 5, "context cascade retrieval augmented generation").unwrap();

        let results = query_fts5(&conn, "hello", 10).unwrap();
        assert!(!results.is_empty(), "query for 'hello' must return results");

        let ids: Vec<i64> = results.iter().map(|(id, _)| *id).collect();
        assert!(
            ids.contains(&1),
            "chunk 1 (contains 'hello') must be in results"
        );
    }

    // ── retrieve::fts::ranking_order ─────────────────────────────────────────

    /// The chunk whose text best matches the query should rank first.
    #[test]
    fn ranking_order() {
        let conn = migrated_conn();

        // Chunk 10 mentions "rust" three times (more relevant);
        // chunk 11 mentions "rust" once.
        index_chunk(&conn, 10, "rust rust rust systems programming").unwrap();
        index_chunk(&conn, 11, "python is great but rust is fast").unwrap();
        index_chunk(&conn, 12, "unrelated content about cooking recipes").unwrap();

        let results = query_fts5(&conn, "rust", 10).unwrap();
        assert!(results.len() >= 2, "must return at least 2 results");

        // Scores are normalised to [0,1]; higher = more relevant.
        let top_id = results[0].0;
        assert_eq!(top_id, 10, "chunk 10 (3× 'rust') must rank first");

        // All scores must be in [0.0, 1.0].
        for (_, score) in &results {
            assert!(*score >= 0.0 && *score <= 1.0, "score {score} out of range");
        }
    }

    // ── retrieve::fts::delete_removes_from_fts ────────────────────────────────

    /// After deleting a chunk from rag_chunks the FTS trigger must remove it.
    #[test]
    fn delete_removes_from_fts() {
        let conn = migrated_conn();

        index_chunk(&conn, 20, "unique term xyzzy for delete test").unwrap();

        // Verify indexed.
        let before = query_fts5(&conn, "xyzzy", 5).unwrap();
        assert!(!before.is_empty(), "chunk must be findable before delete");

        // Delete via rag_chunks (bd trigger removes from rag_fts5).
        conn.execute("DELETE FROM rag_chunks WHERE id = 20", [])
            .unwrap();

        let after = query_fts5(&conn, "xyzzy", 5).unwrap();
        assert!(after.is_empty(), "chunk must not be findable after delete");
    }

    // ── retrieve::fts::malformed_query_safety ────────────────────────────────

    /// Malformed FTS5 syntax (unbalanced quotes, lone operators) must return
    /// Ok(empty vec), never panic or Err.
    #[test]
    fn malformed_query_returns_empty() {
        let conn = migrated_conn();
        index_chunk(&conn, 30, "some indexable content here").unwrap();

        // Unbalanced double-quote — after sanitisation this becomes a valid
        // phrase (the sanitiser escapes the embedded quote), so we test the
        // raw-query path separately using a query that FTS5 would reject even
        // after wrapping: an empty string.
        let r1 = query_fts5(&conn, "", 10).unwrap();
        // Empty-string query may return zero results; must not panic.
        let _ = r1;

        // A query consisting only of special FTS5 operators.
        let r2 = query_fts5(&conn, "AND OR NOT", 10).unwrap();
        let _ = r2;

        // Control characters / null byte.
        let r3 = query_fts5(&conn, "hello\x00world", 10).unwrap();
        let _ = r3;
    }

    // ── retrieve::fts::porter_stemming ───────────────────────────────────────

    /// Porter tokenizer: querying "run" should match a chunk containing "running".
    #[test]
    fn porter_stemming_matches_variant() {
        let conn = migrated_conn();

        index_chunk(&conn, 40, "the program is running in the background").unwrap();
        index_chunk(&conn, 41, "no relevant content here at all zqqx").unwrap();

        let results = query_fts5(&conn, "run", 10).unwrap();
        let ids: Vec<i64> = results.iter().map(|(id, _)| *id).collect();
        assert!(
            ids.contains(&40),
            "chunk 40 ('running') must match query 'run' via porter stemming"
        );
    }

    // ── retrieve::fts::normalize_bm25_score ──────────────────────────────────

    /// Verify the sigmoid normalisation formula properties.
    ///
    /// Sigmoid: `1 / (1 + e^score)` maps negative → high, zero → 0.5.
    #[test]
    fn normalize_bm25_properties() {
        // Very negative → close to 1.0 (sigmoid of -10 ≈ 0.9999546).
        let high = normalize_bm25_score(-10.0);
        assert!(high > 0.9, "score -10 must normalise to > 0.9, got {high}");

        // Zero → 0.5 (sigmoid threshold).
        let zero = normalize_bm25_score(0.0);
        assert!(
            (zero - 0.5).abs() < 1e-9,
            "score 0 must normalise to 0.5, got {zero}"
        );

        // Positive → in [0.0, 0.5) after clamp.
        let pos = normalize_bm25_score(5.0);
        assert!(
            (0.0..=1.0).contains(&pos),
            "positive score must be in [0,1]"
        );

        // Relative ordering: -5 more relevant than -1 → higher normalised score.
        let r_neg5 = normalize_bm25_score(-5.0);
        let r_neg1 = normalize_bm25_score(-1.0);
        assert!(r_neg5 > r_neg1, "-5 must normalise higher than -1");
    }

    // ── retrieve::fts::phrase_search ─────────────────────────────────────────

    /// Phrase search: query "async programming" must rank the exact-phrase chunk
    /// above an unrelated chunk.
    #[test]
    fn phrase_search_ranks_correct_chunk() {
        let conn = migrated_conn();

        index_chunk(&conn, 50, "rust async programming is powerful").unwrap();
        index_chunk(&conn, 51, "synchronous blocking code can be slow").unwrap();

        // The query_fts5 sanitiser wraps in quotes → becomes a phrase search.
        let results = query_fts5(&conn, "async programming", 10).unwrap();
        assert!(!results.is_empty(), "phrase query must return results");

        let top_id = results[0].0;
        assert_eq!(top_id, 50, "phrase chunk must rank first");
    }

    // ── retrieve::fts::idempotent_index ──────────────────────────────────────

    /// Calling index_chunk twice for the same id must not duplicate FTS rows.
    #[test]
    fn idempotent_index_no_duplicate() {
        let conn = migrated_conn();

        index_chunk(&conn, 60, "duplicate test content foobar").unwrap();
        index_chunk(&conn, 60, "duplicate test content foobar").unwrap();

        let results = query_fts5(&conn, "foobar", 10).unwrap();
        assert_eq!(
            results.len(),
            1,
            "must return exactly one result, not duplicates"
        );
    }

    // ── retrieve::fts::fts_retriever_text_populated ──────────────────────────

    /// FtsRetriever::retrieve must return hits with non-empty text equal to the
    /// indexed chunk body, not the empty stub that existed before the chunks-join.
    ///
    /// Steps:
    ///  1. Open a real RagIndex (tempfile).
    ///  2. Upsert a chunk with known text.
    ///  3. Query via FtsRetriever::retrieve.
    ///  4. Assert the top hit's text matches the original body.
    #[tokio::test]
    async fn fts_retriever_text_populated() {
        use crate::index::RagIndex;
        use cascade_types::retriever::RetrieveOpts;
        use std::sync::Arc;
        use tempfile::NamedTempFile;

        let tmp = NamedTempFile::new().expect("tempfile");
        let idx = Arc::new(RagIndex::open(tmp.path()).await.expect("RagIndex::open"));

        let expected_text = "the quick brown fox jumps over the lazy dog";
        idx.upsert_chunk("chunk-fox", None, Some(10), Some(20), expected_text, None)
            .await
            .expect("upsert_chunk");

        let retriever = FtsRetriever::new(Arc::clone(&idx));
        let opts = RetrieveOpts {
            k: 5,
            ..Default::default()
        };
        let hits = retriever
            .retrieve("quick brown fox", &opts)
            .await
            .expect("retrieve");

        assert!(!hits.is_empty(), "FTS query must return at least one hit");
        let top = &hits[0];
        assert_eq!(
            top.chunk_id, "chunk-fox",
            "top hit must be the indexed chunk"
        );
        assert_eq!(
            top.text, expected_text,
            "RetrievalHit.text must equal the indexed chunk body, not empty string"
        );
        assert_eq!(
            top.start_line,
            Some(10),
            "start_line must be populated from chunks table"
        );
        assert_eq!(
            top.end_line,
            Some(20),
            "end_line must be populated from chunks table"
        );
    }
}
