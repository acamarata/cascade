//! FTS5 BM25 keyword retriever.
//!
//! Executes a full-text query against the `fts_chunks` virtual table and
//! returns results ordered by descending BM25 relevance.
//!
//! BM25 scores from SQLite are negative (more negative = more relevant).  This
//! module normalises them to `[0.0, 1.0]` before returning hits, using the
//! approach in [`normalise_bm25`].
//!
//! ## Performance target (S01-04)
//!
//! Keyword search returns top-K=10 results in < 200 ms on a 100K-chunk index.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::FtsRetriever

use async_trait::async_trait;
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
};

use crate::index::RagIndex;

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
        let hits = raw
            .into_iter()
            .enumerate()
            .map(|(rank, (chunk_id, raw_score))| RetrievalHit {
                chunk_id: chunk_id.clone(),
                text: String::new(), // TODO: join to chunks table for text
                file_path: None,
                start_line: None,
                end_line: None,
                score: normalise_bm25(raw_score),
                rank,
                tier: None,
            })
            .collect();
        Ok(hits)
    }

    fn name(&self) -> &str {
        "fts5-bm25"
    }
}

/// Normalise a raw BM25 score from SQLite FTS5 to `[0.0, 1.0]`.
///
/// SQLite's `bm25()` returns negative values (more negative = more relevant).
/// We convert with `1 / (1 + e^score)` which maps:
/// - very negative (e.g. -10) → close to 1.0 (highly relevant)
/// - zero → 0.5
/// - positive (unexpected) → < 0.5
///
/// This is a soft normalisation; relative ordering is preserved.
pub fn normalise_bm25(raw: f32) -> f32 {
    1.0 / (1.0 + raw.exp())
}
