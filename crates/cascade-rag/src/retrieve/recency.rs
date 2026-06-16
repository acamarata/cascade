//! Recency retrieval channel.
//!
//! Ranks chunks by how recently their source document was modified (using
//! `rag_sources.mtime` when available, falling back to `rag_sources.indexed_at`
//! when `mtime` is `NULL`).  Most-recently-modified documents rank first.
//!
//! ## Purpose
//!
//! When two or more documents are otherwise equally relevant (tied RRF scores),
//! users typically prefer the newer one.  This channel injects a soft recency
//! preference into the fusion without overriding relevance signals from BM25 or
//! dense retrieval — the weight assigned to recency in [`RrfConfig`] should be
//! small (default 0.5) so it nudges ties without dominating.
//!
//! ## Score normalisation
//!
//! Raw timestamps are converted to a `[0.0, 1.0]` relevance score via:
//!
//! ```text
//! score(t) = t / t_max    (within the candidate set)
//! ```
//!
//! where `t_max` is the most recent timestamp in the result set.  A score of
//! `1.0` means "most recently updated"; `0.0` is either the oldest or a chunk
//! with no timestamp data.
//!
//! ## Graceful degradation
//!
//! If `rag_sources` has no rows, or all timestamps are `NULL`, the retriever
//! returns an empty list — the fusion proceeds with the remaining channels.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::RecencyRetriever

use async_trait::async_trait;
use rusqlite::{Connection, Result as SqlResult};
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
};

use crate::index::RagIndex;

// ── Low-level query helper ────────────────────────────────────────────────────

/// Return chunk IDs ordered by document recency (most recent first).
///
/// Joins `rag_chunks` with `rag_sources` to fetch `mtime` (preferred) or
/// `indexed_at` as the recency timestamp.  Normalises timestamps to
/// `[0.0, 1.0]` within the returned set.
///
/// # Inputs
///
/// - `conn` — open `rusqlite::Connection` with migrations applied.
/// - `k`    — maximum number of results.
///
/// # Outputs
///
/// `Ok(Vec<(chunk_id, normalised_score)>)` sorted descending by recency.
/// Returns `Ok(vec![])` on any SQL error rather than propagating.
pub fn query_by_recency(conn: &Connection, k: usize) -> SqlResult<Vec<(i64, f64)>> {
    // Use COALESCE(mtime, indexed_at) as the effective timestamp.
    // Fetch one representative chunk per source (lowest chunk_index).
    let mut stmt = match conn.prepare(
        "SELECT c.id, COALESCE(s.mtime, s.indexed_at, 0) AS ts \
         FROM rag_chunks AS c \
         JOIN rag_sources AS s ON s.id = c.source_id \
         WHERE c.chunk_index = 0 \
         ORDER BY ts DESC \
         LIMIT ?1",
    ) {
        Ok(s) => s,
        Err(_) => return Ok(vec![]),
    };

    let rows: SqlResult<Vec<(i64, i64)>> = stmt
        .query_map(rusqlite::params![k as i64], |row| {
            Ok((row.get::<_, i64>(0)?, row.get::<_, i64>(1)?))
        })
        .and_then(|m| m.collect());

    let pairs = match rows {
        Ok(r) => r,
        Err(_) => return Ok(vec![]),
    };

    if pairs.is_empty() {
        return Ok(vec![]);
    }

    // Normalise: t / t_max (t_max is the first element after ORDER BY ts DESC).
    let t_max = pairs[0].1;
    if t_max <= 0 {
        // All timestamps are zero or missing — return uniform scores.
        return Ok(pairs
            .into_iter()
            .map(|(id, _)| (id, 1.0_f64))
            .collect());
    }

    let normalised = pairs
        .into_iter()
        .map(|(id, ts)| {
            let score = (ts as f64 / t_max as f64).clamp(0.0, 1.0);
            (id, score)
        })
        .collect();

    Ok(normalised)
}

// ── High-level Retriever ──────────────────────────────────────────────────────

/// Retriever that ranks chunks by source document recency.
///
/// Intended as a lightweight tie-breaker in multi-channel RRF fusion.  The
/// recency channel weight should be kept below the body FTS and dense weights
/// so it softens ties rather than overriding relevance.
///
/// # Example
///
/// ```rust,no_run
/// # use std::sync::Arc;
/// # use cascade_rag::index::RagIndex;
/// # use cascade_rag::retrieve::recency::RecencyRetriever;
/// # use cascade_types::Retriever;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = Arc::new(RagIndex::open("/tmp/cascade.db").await?);
/// let retriever = RecencyRetriever::new(idx);
/// let hits = retriever.retrieve("any query", &Default::default()).await?;
/// # Ok(())
/// # }
/// ```
pub struct RecencyRetriever {
    index: Arc<RagIndex>,
}

impl RecencyRetriever {
    /// Construct from a shared index handle.
    pub fn new(index: Arc<RagIndex>) -> Self {
        Self { index }
    }
}

#[async_trait]
impl Retriever for RecencyRetriever {
    /// Return chunks ordered by source document recency.
    ///
    /// The `query` parameter is intentionally ignored — recency is query-
    /// independent by design.  The top-`opts.k` most-recent chunks are returned
    /// regardless of semantic relevance.
    #[instrument(skip(self, _query), fields(k = opts.k))]
    async fn retrieve(&self, _query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        let hits = self.index.recency_query(opts.k).await.unwrap_or_default();

        let results = hits
            .into_iter()
            .enumerate()
            .map(|(rank, (chunk_id_str, score))| RetrievalHit {
                chunk_id: chunk_id_str,
                text: String::new(),
                file_path: None,
                start_line: None,
                end_line: None,
                score: score as f32,
                rank,
                tier: None,
            })
            .collect();

        Ok(results)
    }

    fn name(&self) -> &str {
        "recency"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
pub mod tests {
    use super::*;
    use crate::db::run_migrations;
    use rusqlite::Connection;

    /// Open a migrated in-memory DB.
    pub fn migrated_conn() -> Connection {
        let conn = Connection::open_in_memory().expect("in-memory DB");
        run_migrations(&conn).expect("migrations");
        conn
    }

    /// Insert a source row with an explicit mtime.
    pub fn insert_source_with_mtime(conn: &Connection, path: &str, mtime: i64) -> i64 {
        conn.execute(
            "INSERT INTO rag_sources (file_path, mtime, indexed_at, schema_version) \
             VALUES (?1, ?2, ?2, 1)",
            rusqlite::params![path, mtime],
        )
        .unwrap();
        conn.last_insert_rowid()
    }

    /// Insert a chunk for a source.
    pub fn insert_chunk(conn: &Connection, source_id: i64, idx: i64, text: &str) -> i64 {
        conn.execute(
            "INSERT INTO rag_chunks (source_id, chunk_index, chunk_text, schema_version) \
             VALUES (?1, ?2, ?3, 1)",
            rusqlite::params![source_id, idx, text],
        )
        .unwrap();
        conn.last_insert_rowid()
    }

    // ── retrieve::recency::newer_ranks_higher ─────────────────────────────────

    /// The chunk from the more-recently-modified source ranks first.
    #[test]
    fn newer_document_ranks_first() {
        let conn = migrated_conn();

        let s_old = insert_source_with_mtime(&conn, "/old.md", 1_000);
        let s_new = insert_source_with_mtime(&conn, "/new.md", 9_000);

        let c_old = insert_chunk(&conn, s_old, 0, "old content");
        let c_new = insert_chunk(&conn, s_new, 0, "new content");

        let results = query_by_recency(&conn, 10).unwrap();
        assert!(!results.is_empty(), "must return results");

        let ids: Vec<i64> = results.iter().map(|(id, _)| *id).collect();
        let pos_new = ids.iter().position(|id| *id == c_new).unwrap();
        let pos_old = ids.iter().position(|id| *id == c_old).unwrap();
        assert!(
            pos_new < pos_old,
            "newer document (c_new) must rank before older (c_old)"
        );
    }

    // ── retrieve::recency::score_normalisation ────────────────────────────────

    /// The most-recent document has score 1.0; older documents have lower scores.
    #[test]
    fn most_recent_scores_one() {
        let conn = migrated_conn();

        let s_new = insert_source_with_mtime(&conn, "/newest.md", 10_000);
        let s_old = insert_source_with_mtime(&conn, "/oldest.md", 5_000);

        insert_chunk(&conn, s_new, 0, "newest");
        insert_chunk(&conn, s_old, 0, "oldest");

        let results = query_by_recency(&conn, 10).unwrap();

        // Find the chunk for s_new.
        let newest_score = results[0].1;
        assert!(
            (newest_score - 1.0).abs() < 1e-9,
            "most-recent chunk must score 1.0, got {newest_score}"
        );

        // Oldest chunk must have score < 1.0.
        let oldest_score = results[1].1;
        assert!(
            oldest_score < 1.0,
            "older chunk must score < 1.0, got {oldest_score}"
        );
    }

    // ── retrieve::recency::empty_table_returns_empty ──────────────────────────

    /// An empty index returns an empty list without error.
    #[test]
    fn empty_index_returns_empty() {
        let conn = migrated_conn();
        let results = query_by_recency(&conn, 10).unwrap();
        assert!(results.is_empty(), "empty index must produce empty results");
    }

    // ── retrieve::recency::top_k_respected ───────────────────────────────────

    /// Only `k` results are returned even when more are available.
    #[test]
    fn top_k_respected() {
        let conn = migrated_conn();

        for i in 0..5 {
            let s = insert_source_with_mtime(&conn, &format!("/doc{i}.md"), i as i64 * 1000 + 1);
            insert_chunk(&conn, s, 0, &format!("content {i}"));
        }

        let results = query_by_recency(&conn, 3).unwrap();
        assert_eq!(results.len(), 3, "must respect k=3 limit");
    }

    // ── retrieve::recency::null_mtime_fallback ────────────────────────────────

    /// When `mtime` is NULL, `indexed_at` is used as the fallback timestamp.
    #[test]
    fn null_mtime_falls_back_to_indexed_at() {
        let conn = migrated_conn();

        // Insert source with NULL mtime but non-zero indexed_at.
        conn.execute(
            "INSERT INTO rag_sources (file_path, mtime, indexed_at, schema_version) \
             VALUES ('/no-mtime.md', NULL, 7777, 1)",
            [],
        )
        .unwrap();
        let s = conn.last_insert_rowid();
        let c = insert_chunk(&conn, s, 0, "no mtime content");

        let results = query_by_recency(&conn, 10).unwrap();
        let ids: Vec<i64> = results.iter().map(|(id, _)| *id).collect();
        assert!(ids.contains(&c), "chunk from source with NULL mtime must appear via indexed_at fallback");
    }
}
