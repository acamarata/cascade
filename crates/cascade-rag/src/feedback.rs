//! Feedback adapter: ingest citation-quality signals for future LoRA/projection training.
//!
//! # Purpose
//!
//! Records `FeedbackSignal` events (thumbs-up/down, citation quality scores,
//! query→context relevance) that are produced by the citation pipeline
//! (rag-08 / mem-01) and stored in a `rag_feedback` SQLite table for later
//! batch training use.
//!
//! The actual LoRA fine-tuning or projection-head training is explicitly
//! deferred — this module only handles the ingest/storage side.
//!
//! # Schema
//!
//! `rag_feedback` table:
//! ```sql
//! CREATE TABLE IF NOT EXISTS rag_feedback (
//!     id          INTEGER PRIMARY KEY AUTOINCREMENT,
//!     created_at  INTEGER NOT NULL,   -- Unix timestamp (seconds)
//!     query_text  TEXT    NOT NULL,
//!     chunk_id    INTEGER,            -- references rag_chunks(id), nullable
//!     signal_kind TEXT    NOT NULL,   -- "thumbs_up" | "thumbs_down" | "citation_score" | "relevance"
//!     score       REAL,              -- 0.0–1.0 for graded signals; NULL for binary
//!     metadata    TEXT               -- JSON blob for extensibility
//! );
//! ```
//!
//! # Inputs
//!
//! - `conn`: open SQLite connection; schema is created if absent (idempotent).
//! - `signal`: [`FeedbackSignal`] event to record.
//!
//! # Outputs
//!
//! `Ok(i64)` — the row ID of the inserted signal.
//!
//! # Constraints
//!
//! - Schema creation is idempotent (`CREATE TABLE IF NOT EXISTS`).
//! - `chunk_id` is nullable — feedback can apply to the whole query response.
//! - The actual LoRA/projection training is `// TODO(rag-09): batch training pass`.
//!
//! # Ticket
//!
//! rag-09
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::feedback
//! SPORT: MASTER-TABLES.md → rag_feedback

use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use std::time::{SystemTime, UNIX_EPOCH};

use cascade_types::error::{CascadeError, Result};

// ── Schema ────────────────────────────────────────────────────────────────────

const CREATE_FEEDBACK_TABLE: &str = "
CREATE TABLE IF NOT EXISTS rag_feedback (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  INTEGER NOT NULL,
    query_text  TEXT    NOT NULL,
    chunk_id    INTEGER,
    signal_kind TEXT    NOT NULL,
    score       REAL,
    metadata    TEXT
);
CREATE INDEX IF NOT EXISTS idx_rag_feedback_query ON rag_feedback(query_text);
CREATE INDEX IF NOT EXISTS idx_rag_feedback_chunk ON rag_feedback(chunk_id);
CREATE INDEX IF NOT EXISTS idx_rag_feedback_kind  ON rag_feedback(signal_kind);
";

// ── Types ─────────────────────────────────────────────────────────────────────

/// The kind of feedback signal being recorded.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SignalKind {
    /// User explicitly approved this result (👍).
    ThumbsUp,
    /// User explicitly rejected this result (👎).
    ThumbsDown,
    /// Graded citation quality score (0.0 = irrelevant, 1.0 = perfect citation).
    CitationScore,
    /// Graded query→context relevance score from a downstream evaluator.
    Relevance,
}

impl SignalKind {
    fn as_str(&self) -> &'static str {
        match self {
            Self::ThumbsUp => "thumbs_up",
            Self::ThumbsDown => "thumbs_down",
            Self::CitationScore => "citation_score",
            Self::Relevance => "relevance",
        }
    }
}

impl std::fmt::Display for SignalKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// A single feedback signal from a user or automated evaluator.
///
/// # Fields
///
/// - `query_text`: the original user query this signal applies to.
/// - `chunk_id`: the specific chunk being rated; `None` for query-level signals.
/// - `kind`: categorical signal type.
/// - `score`: graded value 0.0–1.0 for `CitationScore` / `Relevance`; `None`
///   for binary `ThumbsUp` / `ThumbsDown` signals.
/// - `metadata`: optional JSON string for extensibility (session ID, model
///   version, strategy used, etc.).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FeedbackSignal {
    /// The user query this signal is for.
    pub query_text: String,
    /// The chunk being rated (optional; `None` for query-level feedback).
    pub chunk_id: Option<i64>,
    /// Signal kind.
    pub kind: SignalKind,
    /// Graded score for quantitative signals; `None` for binary signals.
    pub score: Option<f64>,
    /// Optional JSON metadata (session_id, model_version, strategy, etc.).
    pub metadata: Option<String>,
}

impl FeedbackSignal {
    /// Construct a thumbs-up signal for a specific chunk.
    pub fn thumbs_up(query: impl Into<String>, chunk_id: i64) -> Self {
        Self {
            query_text: query.into(),
            chunk_id: Some(chunk_id),
            kind: SignalKind::ThumbsUp,
            score: None,
            metadata: None,
        }
    }

    /// Construct a thumbs-down signal for a specific chunk.
    pub fn thumbs_down(query: impl Into<String>, chunk_id: i64) -> Self {
        Self {
            query_text: query.into(),
            chunk_id: Some(chunk_id),
            kind: SignalKind::ThumbsDown,
            score: None,
            metadata: None,
        }
    }

    /// Construct a graded citation-quality signal (score clamped to 0.0–1.0).
    pub fn citation_score(query: impl Into<String>, chunk_id: i64, score: f64) -> Self {
        Self {
            query_text: query.into(),
            chunk_id: Some(chunk_id),
            kind: SignalKind::CitationScore,
            score: Some(score.clamp(0.0, 1.0)),
            metadata: None,
        }
    }

    /// Construct a query-level relevance signal (no chunk; score clamped 0.0–1.0).
    pub fn relevance(query: impl Into<String>, score: f64) -> Self {
        Self {
            query_text: query.into(),
            chunk_id: None,
            kind: SignalKind::Relevance,
            score: Some(score.clamp(0.0, 1.0)),
            metadata: None,
        }
    }

    /// Attach a JSON metadata blob to this signal.
    pub fn with_metadata(mut self, meta: impl Into<String>) -> Self {
        self.metadata = Some(meta.into());
        self
    }
}

// ── Schema init ───────────────────────────────────────────────────────────────

/// Ensure the `rag_feedback` schema exists on `conn`.
///
/// Idempotent — safe to call on every connection open.
pub fn ensure_feedback_schema(conn: &Connection) -> Result<()> {
    conn.execute_batch(CREATE_FEEDBACK_TABLE)
        .map_err(|e| CascadeError::Other(format!("rag_feedback schema: {e}")))
}

// ── Ingest ────────────────────────────────────────────────────────────────────

/// Record a [`FeedbackSignal`] in the `rag_feedback` table.
///
/// Creates the schema if absent (idempotent).
///
/// # Inputs
///
/// - `conn`: open SQLite connection.
/// - `signal`: the feedback event to persist.
///
/// # Outputs
///
/// `Ok(i64)` — the row ID (`last_insert_rowid`) of the inserted record.
///
/// # Errors
///
/// `CascadeError::Other` wrapping any SQLite error.
pub fn ingest_feedback(conn: &Connection, signal: &FeedbackSignal) -> Result<i64> {
    ensure_feedback_schema(conn)?;

    let created_at = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);

    conn.execute(
        "INSERT INTO rag_feedback \
         (created_at, query_text, chunk_id, signal_kind, score, metadata) \
         VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
        params![
            created_at,
            signal.query_text,
            signal.chunk_id,
            signal.kind.as_str(),
            signal.score,
            signal.metadata,
        ],
    )
    .map_err(|e| CascadeError::Other(format!("rag_feedback insert: {e}")))?;

    Ok(conn.last_insert_rowid())
}

/// List all recorded signals for a given query (most recent first).
///
/// Useful for batch export and debugging.
///
/// # Inputs
///
/// - `conn`: connection with `rag_feedback` table.
/// - `query_text`: exact query string to filter by.
///
/// # Outputs
///
/// `Ok(Vec<(i64, FeedbackSignal)>)` — row ID plus signal for each match.
pub fn signals_for_query(
    conn: &Connection,
    query_text: &str,
) -> Result<Vec<(i64, FeedbackSignal)>> {
    ensure_feedback_schema(conn)?;

    let mut stmt = conn
        .prepare(
            "SELECT id, query_text, chunk_id, signal_kind, score, metadata \
             FROM rag_feedback WHERE query_text = ?1 ORDER BY id DESC",
        )
        .map_err(|e| CascadeError::Other(format!("signals_for_query prepare: {e}")))?;

    let rows = stmt
        .query_map(params![query_text], |row| {
            let kind_str: String = row.get(3)?;
            Ok((
                row.get::<_, i64>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, Option<i64>>(2)?,
                kind_str,
                row.get::<_, Option<f64>>(4)?,
                row.get::<_, Option<String>>(5)?,
            ))
        })
        .map_err(|e| CascadeError::Other(format!("signals_for_query query: {e}")))?;

    let mut results = Vec::new();
    for r in rows {
        let (id, query_text, chunk_id, kind_str, score, metadata) =
            r.map_err(|e| CascadeError::Other(e.to_string()))?;

        let kind = match kind_str.as_str() {
            "thumbs_up" => SignalKind::ThumbsUp,
            "thumbs_down" => SignalKind::ThumbsDown,
            "citation_score" => SignalKind::CitationScore,
            "relevance" => SignalKind::Relevance,
            other => {
                return Err(CascadeError::Other(format!(
                    "unknown signal_kind in DB: {other}"
                )))
            }
        };

        results.push((
            id,
            FeedbackSignal {
                query_text,
                chunk_id,
                kind,
                score,
                metadata,
            },
        ));
    }

    Ok(results)
}

// TODO(rag-09): batch training pass — read signals, compute relevance labels,
// fine-tune projection head or LoRA adapter on (query, chunk, label) triples.

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    fn fresh_conn() -> Connection {
        Connection::open_in_memory().unwrap()
    }

    #[test]
    fn ingest_thumbs_up_stores_row() {
        let conn = fresh_conn();
        let sig = FeedbackSignal::thumbs_up("who calls foo", 42);
        let id = ingest_feedback(&conn, &sig).unwrap();
        assert!(id > 0);
    }

    #[test]
    fn ingest_thumbs_down_stores_row() {
        let conn = fresh_conn();
        let sig = FeedbackSignal::thumbs_down("explain RRF", 7);
        let id = ingest_feedback(&conn, &sig).unwrap();
        assert!(id > 0);
    }

    #[test]
    fn ingest_citation_score_stores_row() {
        let conn = fresh_conn();
        let sig = FeedbackSignal::citation_score("dense retrieval", 3, 0.85);
        let id = ingest_feedback(&conn, &sig).unwrap();
        assert!(id > 0);
    }

    #[test]
    fn ingest_relevance_signal_stores_row() {
        let conn = fresh_conn();
        let sig = FeedbackSignal::relevance("chunking strategy", 0.72);
        let id = ingest_feedback(&conn, &sig).unwrap();
        assert!(id > 0);
    }

    #[test]
    fn citation_score_clamped_above_1() {
        let sig = FeedbackSignal::citation_score("q", 1, 2.5);
        assert_eq!(sig.score, Some(1.0));
    }

    #[test]
    fn citation_score_clamped_below_0() {
        let sig = FeedbackSignal::citation_score("q", 1, -0.5);
        assert_eq!(sig.score, Some(0.0));
    }

    #[test]
    fn relevance_clamped_above_1() {
        let sig = FeedbackSignal::relevance("q", 5.0);
        assert_eq!(sig.score, Some(1.0));
    }

    #[test]
    fn signals_for_query_returns_inserted_signals() {
        let conn = fresh_conn();
        let query = "explain HyDE";

        ingest_feedback(&conn, &FeedbackSignal::thumbs_up(query, 1)).unwrap();
        ingest_feedback(&conn, &FeedbackSignal::citation_score(query, 2, 0.9)).unwrap();
        ingest_feedback(&conn, &FeedbackSignal::thumbs_down("other query", 3)).unwrap();

        let sigs = signals_for_query(&conn, query).unwrap();
        assert_eq!(sigs.len(), 2);
        // Most recent first
        assert_eq!(sigs[0].1.kind, SignalKind::CitationScore);
        assert_eq!(sigs[1].1.kind, SignalKind::ThumbsUp);
    }

    #[test]
    fn signals_for_unknown_query_returns_empty() {
        let conn = fresh_conn();
        ensure_feedback_schema(&conn).unwrap();
        let sigs = signals_for_query(&conn, "nonexistent").unwrap();
        assert!(sigs.is_empty());
    }

    #[test]
    fn ingest_with_metadata_stores_json() {
        let conn = fresh_conn();
        let sig = FeedbackSignal::citation_score("q", 1, 0.5)
            .with_metadata(r#"{"session":"abc","strategy":"multi-query"}"#);
        ingest_feedback(&conn, &sig).unwrap();

        let sigs = signals_for_query(&conn, "q").unwrap();
        assert_eq!(sigs.len(), 1);
        assert!(sigs[0].1.metadata.as_deref().unwrap().contains("session"));
    }

    #[test]
    fn schema_creation_is_idempotent() {
        let conn = fresh_conn();
        ensure_feedback_schema(&conn).unwrap();
        ensure_feedback_schema(&conn).unwrap(); // second call must not error
    }

    #[test]
    fn chunk_id_is_none_for_relevance_signal() {
        let sig = FeedbackSignal::relevance("q", 0.5);
        assert!(sig.chunk_id.is_none());
    }

    #[test]
    fn multiple_ingests_get_distinct_ids() {
        let conn = fresh_conn();
        let id1 = ingest_feedback(&conn, &FeedbackSignal::thumbs_up("q", 1)).unwrap();
        let id2 = ingest_feedback(&conn, &FeedbackSignal::thumbs_up("q", 2)).unwrap();
        assert_ne!(id1, id2);
    }
}
