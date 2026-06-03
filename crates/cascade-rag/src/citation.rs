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
}
