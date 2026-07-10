//! Curated-description retrieval channel.
//!
//! Searches the `rag_chunk_meta_fts5` virtual table, which indexes the
//! hand-written frontmatter fields (`description`, `title`, `tags`) for every
//! source document.  These curated fields are a privileged retrieval surface:
//! they are authored by humans, terse, and highly indicative of document intent.
//! A small-fact document whose body is sparse but whose `description` precisely
//! matches the query will rank highly here even when BM25 body search misses.
//!
//! ## Schema dependency
//!
//! Requires migration 0009 (`rag_chunk_meta` + `rag_chunk_meta_fts5`).
//!
//! ## Result semantics
//!
//! The retriever returns one synthetic hit per *source document* (not per
//! chunk).  The `chunk_id` is encoded as `"meta:<source_id>"` so RRF can
//! correlate hits across channels without colliding with real chunk IDs.  The
//! caller (RrfRetriever) maps these back to real chunk IDs via the
//! highest-ranked chunk under that `source_id`.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::CuratedRetriever

use async_trait::async_trait;
use rusqlite::{Connection, Result as SqlResult};
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
};

use crate::index::RagIndex;

// ── Low-level query helpers ───────────────────────────────────────────────────

/// Insert or replace curated metadata for a source document.
///
/// # Purpose
///
/// Populates `rag_chunk_meta` with frontmatter-derived fields.  The associated
/// FTS5 trigger fires automatically to update `rag_chunk_meta_fts5`.
///
/// # Inputs
///
/// - `conn`        — open `rusqlite::Connection` with migrations applied.
/// - `source_id`   — FK into `rag_sources.id`.
/// - `description` — value of frontmatter `description:` key, or `None`.
/// - `title`       — value of frontmatter `title:` key, or first H1, or `None`.
/// - `tags`        — space-joined tag tokens, or `None`.
///
/// # Outputs
///
/// `Ok(())` on success; `Err(rusqlite::Error)` on SQL failure.
pub fn upsert_chunk_meta(
    conn: &Connection,
    source_id: i64,
    description: Option<&str>,
    title: Option<&str>,
    tags: Option<&str>,
) -> SqlResult<()> {
    conn.execute(
        "INSERT OR REPLACE INTO rag_chunk_meta \
         (source_id, description, title, tags, schema_version) \
         VALUES (?1, ?2, ?3, ?4, 1)",
        rusqlite::params![source_id, description, title, tags],
    )?;
    Ok(())
}

/// Query `rag_chunk_meta_fts5` for documents whose curated description/title/tags
/// match `query` using BM25 scoring.
///
/// # Outputs
///
/// `Ok(Vec<(source_id, normalised_score)>)` sorted descending by score.
/// Returns `Ok(vec![])` on FTS parse errors rather than propagating.
pub fn query_curated_fts(conn: &Connection, query: &str, k: usize) -> SqlResult<Vec<(i64, f64)>> {
    let sanitised = sanitise_fts5_query(query);

    let mut stmt = match conn.prepare(
        "SELECT m.source_id, bm25(rag_chunk_meta_fts5) AS score \
         FROM rag_chunk_meta_fts5 \
         JOIN rag_chunk_meta AS m ON m.id = rag_chunk_meta_fts5.rowid \
         WHERE rag_chunk_meta_fts5 MATCH ?1 \
         ORDER BY score ASC \
         LIMIT ?2",
    ) {
        Ok(s) => s,
        Err(_) => return Ok(vec![]),
    };

    let rows: SqlResult<Vec<(i64, f64)>> = stmt
        .query_map(rusqlite::params![sanitised, k as i64], |row| {
            let source_id: i64 = row.get(0)?;
            let raw_score: f64 = row.get(1)?;
            Ok((source_id, normalise_bm25(raw_score)))
        })
        .and_then(|mapped| mapped.collect());

    match rows {
        Ok(r) => Ok(r),
        Err(rusqlite::Error::SqliteFailure(_, _)) => Ok(vec![]),
        Err(e) => Err(e),
    }
}

/// Given a `source_id`, return the `id` of the lowest-indexed chunk in that
/// source.  Used to translate document-level matches to chunk IDs for RRF.
///
/// Returns `None` when the source has no indexed chunks.
pub fn first_chunk_for_source(conn: &Connection, source_id: i64) -> SqlResult<Option<i64>> {
    let result = conn.query_row(
        "SELECT id FROM rag_chunks \
         WHERE source_id = ?1 \
         ORDER BY chunk_index ASC \
         LIMIT 1",
        rusqlite::params![source_id],
        |row| row.get::<_, i64>(0),
    );
    match result {
        Ok(id) => Ok(Some(id)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(e),
    }
}

// ── Normalisation ─────────────────────────────────────────────────────────────

/// Normalise a raw BM25 score (negative) from SQLite to `[0.0, 1.0]`.
///
/// Uses sigmoid: `1 / (1 + e^raw)`.  Very negative raw (high relevance) → ~1.0;
/// raw = 0 → 0.5.  Clamped to `[0.0, 1.0]`.
fn normalise_bm25(raw: f64) -> f64 {
    (1.0_f64 / (1.0 + raw.exp())).clamp(0.0, 1.0)
}

/// Escape and quote a query string for safe FTS5 MATCH use.
fn sanitise_fts5_query(query: &str) -> String {
    let escaped = query.replace('"', "\"\"");
    format!("\"{}\"", escaped)
}

// ── High-level Retriever ──────────────────────────────────────────────────────

/// Retriever that searches curated frontmatter (description / title / tags).
///
/// Returns one hit per source document (translated to the document's first
/// chunk ID).  Missing or empty metadata tables degrade gracefully to an empty
/// result set — no panic, no error.
///
/// # Example
///
/// ```rust,no_run
/// # use std::sync::Arc;
/// # use cascade_rag::index::RagIndex;
/// # use cascade_rag::retrieve::curated::CuratedRetriever;
/// # use cascade_types::Retriever;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = Arc::new(RagIndex::open("/tmp/cascade.db").await?);
/// let retriever = CuratedRetriever::new(idx);
/// let hits = retriever.retrieve("prayer times calculation", &Default::default()).await?;
/// # Ok(())
/// # }
/// ```
pub struct CuratedRetriever {
    index: Arc<RagIndex>,
}

impl CuratedRetriever {
    /// Construct from a shared index handle.
    pub fn new(index: Arc<RagIndex>) -> Self {
        Self { index }
    }
}

#[async_trait]
impl Retriever for CuratedRetriever {
    /// Query the curated-description FTS table and translate source IDs to
    /// chunk IDs via the first chunk in each source.
    #[instrument(skip(self), fields(k = opts.k))]
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        let hits = self
            .index
            .curated_query(query, opts.k)
            .await
            .unwrap_or_default();

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
        "curated-description"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
pub mod tests {
    use super::*;
    use crate::db::run_migrations;
    use rusqlite::Connection;

    /// Open a migrated in-memory DB with a sentinel source row.
    pub fn migrated_conn() -> Connection {
        let conn = Connection::open_in_memory().expect("in-memory DB");
        run_migrations(&conn).expect("migrations");
        conn
    }

    /// Insert a source row and return its id.
    pub fn insert_source(conn: &Connection, path: &str) -> i64 {
        conn.execute(
            "INSERT INTO rag_sources (file_path, schema_version) VALUES (?1, 1)",
            rusqlite::params![path],
        )
        .unwrap();
        conn.last_insert_rowid()
    }

    /// Insert a chunk for a given source and return its id.
    pub fn insert_chunk(conn: &Connection, source_id: i64, idx: i64, text: &str) -> i64 {
        conn.execute(
            "INSERT INTO rag_chunks (source_id, chunk_index, chunk_text, schema_version) \
             VALUES (?1, ?2, ?3, 1)",
            rusqlite::params![source_id, idx, text],
        )
        .unwrap();
        conn.last_insert_rowid()
    }

    // ── retrieve::curated::description_match_ranks ───────────────────────────

    /// A doc whose description matches the query ranks above one whose body
    /// matches only vaguely (simulated by only having meta on the target doc).
    #[test]
    fn description_match_ranks_above_no_description() {
        let conn = migrated_conn();

        let s1 = insert_source(&conn, "/docs/prayer.md");
        let s2 = insert_source(&conn, "/docs/other.md");

        // Only s1 has curated metadata matching "prayer times".
        upsert_chunk_meta(
            &conn,
            s1,
            Some("Islamic prayer times calculation"),
            None,
            None,
        )
        .unwrap();
        // s2 has no curated metadata.

        let results = query_curated_fts(&conn, "prayer times", 10).unwrap();
        let ids: Vec<i64> = results.iter().map(|(id, _)| *id).collect();
        assert!(
            ids.contains(&s1),
            "source with matching description must appear"
        );
        assert!(
            !ids.contains(&s2),
            "source without description must not appear"
        );
    }

    // ── retrieve::curated::title_tag_match ───────────────────────────────────

    /// Matching on title or tags fields also retrieves the document.
    #[test]
    fn title_and_tag_match_retrieves_document() {
        let conn = migrated_conn();

        let s = insert_source(&conn, "/docs/hijri.md");
        upsert_chunk_meta(
            &conn,
            s,
            None,
            Some("Hijri Calendar Reference"),
            Some("hijri calendar islamic"),
        )
        .unwrap();

        let by_title = query_curated_fts(&conn, "hijri calendar", 10).unwrap();
        assert!(
            by_title.iter().any(|(id, _)| *id == s),
            "title match must retrieve document"
        );

        let by_tag = query_curated_fts(&conn, "islamic", 10).unwrap();
        assert!(
            by_tag.iter().any(|(id, _)| *id == s),
            "tag match must retrieve document"
        );
    }

    // ── retrieve::curated::first_chunk_translation ───────────────────────────

    /// `first_chunk_for_source` returns the lowest chunk_index id.
    #[test]
    fn first_chunk_for_source_returns_lowest_index() {
        let conn = migrated_conn();

        let s = insert_source(&conn, "/docs/doc.md");
        let c0 = insert_chunk(&conn, s, 0, "first chunk");
        let _c1 = insert_chunk(&conn, s, 1, "second chunk");

        let result = first_chunk_for_source(&conn, s).unwrap();
        assert_eq!(result, Some(c0), "must return the chunk with chunk_index=0");
    }

    // ── retrieve::curated::no_metadata_returns_empty ─────────────────────────

    /// When no documents have metadata, query returns empty — no panic.
    #[test]
    fn no_metadata_returns_empty() {
        let conn = migrated_conn();
        let results = query_curated_fts(&conn, "anything", 10).unwrap();
        assert!(
            results.is_empty(),
            "empty meta table must produce empty results"
        );
    }

    // ── retrieve::curated::idempotent_upsert ─────────────────────────────────

    /// Calling upsert_chunk_meta twice for the same source_id must not panic
    /// or produce duplicate FTS rows.
    #[test]
    fn idempotent_upsert_no_duplicate() {
        let conn = migrated_conn();
        let s = insert_source(&conn, "/docs/dup.md");

        upsert_chunk_meta(&conn, s, Some("description one"), None, None).unwrap();
        upsert_chunk_meta(&conn, s, Some("description two"), None, None).unwrap();

        let results = query_curated_fts(&conn, "description", 10).unwrap();
        assert_eq!(results.len(), 1, "must have exactly one source in results");
    }
}
