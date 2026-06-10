//! Citation tracking for RAG search results.
//!
//! Every chunk returned by the retrieval pipeline carries a `Citation` that
//! records its exact provenance: file path, line range, chunk ID, retrieval
//! rank, and relevance score.  Citations are injected into MCP search
//! responses and can be formatted as Markdown footnotes for inline LLM output.
//!
//! ## Acceptance criteria (S21)
//!
//! - Every MCP `cascade.search` response includes a `citations` array.
//! - `CitationSet::deduplicate` merges citations that point to the same file
//!   with overlapping line ranges.
//! - `CitationSet::to_markdown` produces valid Markdown footnote syntax.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::citation

use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

// ── Citation ──────────────────────────────────────────────────────────────────

/// Provenance record for a single retrieved chunk.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::citation::Citation
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Citation {
    /// Absolute path to the source file.
    pub file_path: Option<PathBuf>,

    /// 1-based start line in the source file.
    pub start_line: Option<usize>,

    /// 1-based end line in the source file.
    pub end_line: Option<usize>,

    /// Chunk identifier (matches `Chunk::id`).
    pub chunk_id: String,

    /// 0-based rank in the retrieval result set (0 = most relevant).
    pub retrieval_rank: usize,

    /// Relevance score from the retrieval step (higher = more relevant).
    pub relevance_score: f32,

    /// Name of the retrieval strategy that produced this hit.
    pub strategy: String,
}

impl Citation {
    /// Format as a Markdown footnote reference `[^1]`.
    pub fn to_footnote_ref(&self, n: usize) -> String {
        format!("[^{n}]")
    }

    /// Format as a Markdown footnote definition.
    ///
    /// ```text
    /// [^1]: `src/lib.rs` lines 42–58 (score: 0.92)
    /// ```
    pub fn to_footnote_def(&self, n: usize) -> String {
        let path = self
            .file_path
            .as_ref()
            .map(|p| p.display().to_string())
            .unwrap_or_else(|| self.chunk_id.clone());
        let lines = match (self.start_line, self.end_line) {
            (Some(s), Some(e)) => format!(" lines {s}–{e}"),
            (Some(s), None) => format!(" line {s}"),
            _ => String::new(),
        };
        format!(
            "[^{n}]: `{path}`{lines} (score: {:.2})",
            self.relevance_score
        )
    }
}

// ── CitationSet ───────────────────────────────────────────────────────────────

/// Ordered set of citations for a single search response.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::citation::CitationSet
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CitationSet {
    pub citations: Vec<Citation>,
}

impl CitationSet {
    /// Construct from a vec of citations.
    pub fn new(citations: Vec<Citation>) -> Self {
        Self { citations }
    }

    /// Merge citations that reference the same file with overlapping line ranges.
    ///
    /// Two citations are merged when:
    /// 1. They point to the same `file_path`.
    /// 2. Their line ranges overlap (start_line of one is ≤ end_line of other).
    ///
    /// The merged citation retains the higher `relevance_score` and the lower
    /// `retrieval_rank`.
    pub fn deduplicate(&mut self) {
        if self.citations.len() <= 1 {
            return;
        }
        self.citations.sort_by(|a, b| {
            a.file_path
                .cmp(&b.file_path)
                .then(a.start_line.cmp(&b.start_line))
        });

        let mut merged: Vec<Citation> = Vec::with_capacity(self.citations.len());
        for cite in self.citations.drain(..) {
            if let Some(last) = merged.last_mut() {
                if last.file_path == cite.file_path && ranges_overlap(last, &cite) {
                    // Merge: extend range, keep better score/rank.
                    last.end_line = max_opt(last.end_line, cite.end_line);
                    if cite.relevance_score > last.relevance_score {
                        last.relevance_score = cite.relevance_score;
                    }
                    if cite.retrieval_rank < last.retrieval_rank {
                        last.retrieval_rank = cite.retrieval_rank;
                    }
                    continue;
                }
            }
            merged.push(cite);
        }
        self.citations = merged;
    }

    /// Render all citations as Markdown footnote blocks.
    pub fn to_markdown(&self) -> String {
        self.citations
            .iter()
            .enumerate()
            .map(|(i, c)| c.to_footnote_def(i + 1))
            .collect::<Vec<_>>()
            .join("\n")
    }

    /// Return the number of citations.
    pub fn len(&self) -> usize {
        self.citations.len()
    }

    /// Return `true` if there are no citations.
    pub fn is_empty(&self) -> bool {
        self.citations.is_empty()
    }
}

// ── RagCitation ───────────────────────────────────────────────────────────────

/// Full provenance record for a single RAG search result, as returned by the
/// retrieval pipeline after RRF score fusion.
///
/// This is the canonical return type for [`citations_from_chunk_ids`] and all
/// MCP `cascade.search` responses.  Every field that comes from a secondary
/// retrieval pass (`dense_score`, `sparse_score`, `fts5_score`,
/// `reranker_score`) is `Option` so callers can distinguish "not computed"
/// from "scored zero".
///
/// SPORT: MASTER-LIBS.md → cascade-rag::citation::RagCitation
///
/// # Ticket
/// T-P4-E01-33
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct RagCitation {
    /// Primary key of the chunk row in `rag_chunks`.
    pub chunk_id: i64,

    /// Absolute path to the source file.
    pub source_path: PathBuf,

    /// 1-based start line in the source file.
    pub line_start: usize,

    /// 1-based end line in the source file.
    pub line_end: usize,

    /// Dot-separated heading hierarchy at the chunk location (e.g. `"## Intro > ### Details"`).
    pub heading_path: Option<String>,

    /// First 200 *characters* of the chunk text (character-boundary safe).
    pub snippet: String,

    /// Fused RRF score — the primary relevance signal after score fusion.
    pub rrf_score: f64,

    /// Raw cosine similarity from the dense vector search pass (`None` if not run).
    pub dense_score: Option<f64>,

    /// Sparse embedding similarity (`None` if not run).
    pub sparse_score: Option<f64>,

    /// BM25 score from the FTS5 pass (`None` if not run).
    pub fts5_score: Option<f64>,

    /// Cross-encoder reranker score (`None` if not run).
    pub reranker_score: Option<f64>,

    /// SHA-256 hex digest of the source file at index time — used to detect staleness.
    pub source_hash: String,
}

impl RagCitation {
    /// Truncate `text` to at most 200 *characters*, respecting char boundaries.
    fn snippet_from(text: &str) -> String {
        let end = text
            .char_indices()
            .nth(200)
            .map(|(idx, _)| idx)
            .unwrap_or(text.len());
        text[..end].to_string()
    }
}

// ── citations_from_chunk_ids ──────────────────────────────────────────────────

/// Load [`RagCitation`] records for a list of `(chunk_id, rrf_score)` pairs.
///
/// Executes a single SQL query with an `IN (…)` clause to join `rag_chunks`
/// with `rag_sources` and collect all required fields in one round-trip.
///
/// The `source_hash` is read from `rag_sources.content_hash`; a missing hash
/// (NULL) is serialised as `""`.
///
/// # Errors
/// Returns [`rusqlite::Error`] on any database failure.
///
/// # SQL injection safety
/// The chunk IDs are passed as a repeated `?` parameter list, never
/// interpolated as a string.  See the IN-clause construction below.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::citation::citations_from_chunk_ids
///
/// # Ticket
/// T-P4-E01-33
pub fn citations_from_chunk_ids(
    conn: &Connection,
    chunk_ids: &[(i64, f64)],
) -> Result<Vec<RagCitation>, rusqlite::Error> {
    if chunk_ids.is_empty() {
        return Ok(vec![]);
    }

    // Build the IN clause: (?1, ?2, …)
    let placeholders: Vec<String> = (1..=chunk_ids.len()).map(|i| format!("?{i}")).collect();
    let sql = format!(
        "SELECT rc.id, rs.file_path, rc.line_start, rc.line_end, \
                rc.heading_path, rc.chunk_text, rs.content_hash \
         FROM rag_chunks rc \
         JOIN rag_sources rs ON rc.source_id = rs.id \
         WHERE rc.id IN ({})",
        placeholders.join(", ")
    );

    // Build a lookup map: chunk_id → rrf_score
    let score_map: std::collections::HashMap<i64, f64> = chunk_ids.iter().copied().collect();

    let mut stmt = conn.prepare(&sql)?;

    // Bind each chunk ID as a positional parameter.
    let ids: Vec<rusqlite::types::Value> = chunk_ids
        .iter()
        .map(|(id, _)| rusqlite::types::Value::Integer(*id))
        .collect();

    let rows = stmt.query_map(rusqlite::params_from_iter(ids.iter()), |row| {
        let id: i64 = row.get(0)?;
        let file_path: String = row.get(1)?;
        let line_start: Option<i64> = row.get(2)?;
        let line_end: Option<i64> = row.get(3)?;
        let heading_path: Option<String> = row.get(4)?;
        let chunk_text: String = row.get(5)?;
        let content_hash: Option<String> = row.get(6)?;

        Ok((
            id,
            file_path,
            line_start,
            line_end,
            heading_path,
            chunk_text,
            content_hash,
        ))
    })?;

    let mut citations = Vec::with_capacity(chunk_ids.len());
    for row in rows {
        let (id, file_path, line_start, line_end, heading_path, chunk_text, content_hash) = row?;
        let rrf_score = score_map.get(&id).copied().unwrap_or(0.0);
        citations.push(RagCitation {
            chunk_id: id,
            source_path: PathBuf::from(&file_path),
            line_start: line_start.unwrap_or(0) as usize,
            line_end: line_end.unwrap_or(0) as usize,
            heading_path,
            snippet: RagCitation::snippet_from(&chunk_text),
            rrf_score,
            dense_score: None,
            sparse_score: None,
            fts5_score: None,
            reranker_score: None,
            source_hash: content_hash.unwrap_or_default(),
        });
    }
    Ok(citations)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn ranges_overlap(a: &Citation, b: &Citation) -> bool {
    match (a.start_line, a.end_line, b.start_line, b.end_line) {
        (Some(as_), Some(ae), Some(bs), Some(be)) => as_ <= be && bs <= ae,
        _ => false,
    }
}

fn max_opt(a: Option<usize>, b: Option<usize>) -> Option<usize> {
    match (a, b) {
        (Some(x), Some(y)) => Some(x.max(y)),
        (x, None) => x,
        (None, y) => y,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn cite(file: &str, start: usize, end: usize, score: f32) -> Citation {
        Citation {
            file_path: Some(PathBuf::from(file)),
            start_line: Some(start),
            end_line: Some(end),
            chunk_id: format!("{file}-{start}"),
            retrieval_rank: 0,
            relevance_score: score,
            strategy: "test".into(),
        }
    }

    #[test]
    fn dedup_overlapping_ranges() {
        let mut set = CitationSet::new(vec![
            cite("src/lib.rs", 1, 20, 0.9),
            cite("src/lib.rs", 15, 35, 0.8), // overlaps with first
            cite("src/main.rs", 1, 10, 0.7),
        ]);
        set.deduplicate();
        assert_eq!(set.len(), 2, "overlapping ranges must be merged");
        assert_eq!(set.citations[0].end_line, Some(35));
        assert!((set.citations[0].relevance_score - 0.9).abs() < 1e-6);
    }

    #[test]
    fn footnote_format() {
        let c = cite("src/lib.rs", 42, 58, 0.92);
        assert_eq!(
            c.to_footnote_def(1),
            "[^1]: `src/lib.rs` lines 42–58 (score: 0.92)"
        );
    }

    // ── T-P4-E01-33: RagCitation tests ───────────────────────────────────────

    fn rag_cite(chunk_id: i64, source: &str, rrf: f64) -> RagCitation {
        RagCitation {
            chunk_id,
            source_path: PathBuf::from(source),
            line_start: 1,
            line_end: 10,
            heading_path: None,
            snippet: "short snippet".into(),
            rrf_score: rrf,
            dense_score: Some(0.8),
            sparse_score: None,
            fts5_score: None,
            reranker_score: None,
            source_hash: "abc123".into(),
        }
    }

    #[test]
    fn rag_citation_has_12_fields_and_roundtrips_json() {
        let c = rag_cite(42, "src/lib.rs", 0.95);
        let json = serde_json::to_string(&c).expect("serialize");
        // Verify camelCase keys
        assert!(json.contains("\"chunkId\""), "chunkId camelCase field");
        assert!(
            json.contains("\"sourcePath\""),
            "sourcePath camelCase field"
        );
        assert!(json.contains("\"lineStart\""), "lineStart camelCase field");
        assert!(json.contains("\"lineEnd\""), "lineEnd camelCase field");
        assert!(
            json.contains("\"headingPath\""),
            "headingPath camelCase field"
        );
        assert!(json.contains("\"rrfScore\""), "rrfScore camelCase field");
        assert!(
            json.contains("\"denseScore\""),
            "denseScore camelCase field"
        );
        assert!(
            json.contains("\"sparseScore\""),
            "sparseScore camelCase field"
        );
        assert!(json.contains("\"fts5Score\""), "fts5Score camelCase field");
        assert!(
            json.contains("\"rerankerScore\""),
            "rerankerScore camelCase field"
        );
        assert!(
            json.contains("\"sourceHash\""),
            "sourceHash camelCase field"
        );
        assert!(json.contains("\"snippet\""), "snippet field");

        let back: RagCitation = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(back.chunk_id, 42);
        assert_eq!(back.rrf_score, 0.95);
        assert_eq!(back.source_hash, "abc123");
    }

    #[test]
    fn rag_citation_snippet_truncated_at_200_chars() {
        let long_text: String = "a".repeat(300);
        let snippet = RagCitation::snippet_from(&long_text);
        assert_eq!(
            snippet.len(),
            200,
            "snippet must be exactly 200 chars for ASCII"
        );
    }

    #[test]
    fn rag_citation_snippet_truncated_char_boundary_safe() {
        // 3-byte UTF-8 codepoint (€ = U+20AC)
        let text: String = std::iter::repeat_n('€', 100).collect();
        let snippet = RagCitation::snippet_from(&text);
        // 200 characters = 100 '€' (200 chars limit hits char count, not byte 200)
        assert_eq!(snippet.chars().count(), 100);
        // snippet must be valid UTF-8 (String type guarantees this)
    }

    #[test]
    fn rag_citation_optional_scores_nullable_in_json() {
        let c = RagCitation {
            chunk_id: 1,
            source_path: PathBuf::from("a.rs"),
            line_start: 1,
            line_end: 5,
            heading_path: None,
            snippet: "x".into(),
            rrf_score: 0.5,
            dense_score: None,
            sparse_score: None,
            fts5_score: None,
            reranker_score: None,
            source_hash: "".into(),
        };
        let json = serde_json::to_string(&c).unwrap();
        let v: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert!(
            v["denseScore"].is_null(),
            "None fields must serialize as JSON null"
        );
    }

    #[test]
    fn citations_from_chunk_ids_empty_list() {
        // Should return empty vec without hitting the DB
        let conn = Connection::open_in_memory().unwrap();
        let result = citations_from_chunk_ids(&conn, &[]).unwrap();
        assert!(result.is_empty());
    }

    #[test]
    fn citations_from_chunk_ids_returns_one_per_id() {
        use crate::db::migrations::run_migrations;

        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();

        // Insert a source and a chunk
        conn.execute(
            "INSERT INTO rag_sources (id, file_path, content_hash, schema_version) \
             VALUES (1, '/tmp/test.md', 'deadbeef', 1)",
            [],
        )
        .unwrap();
        conn.execute(
            "INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, \
             line_start, line_end, schema_version) \
             VALUES (10, 1, 0, 'hello world chunk', 1, 5, 1)",
            [],
        )
        .unwrap();

        let result = citations_from_chunk_ids(&conn, &[(10, 0.75)]).unwrap();
        assert_eq!(result.len(), 1);
        let c = &result[0];
        assert_eq!(c.chunk_id, 10);
        assert_eq!(c.rrf_score, 0.75);
        assert_eq!(c.source_hash, "deadbeef");
        assert_eq!(c.snippet, "hello world chunk");
        assert_eq!(c.line_start, 1);
        assert_eq!(c.line_end, 5);
    }
}
