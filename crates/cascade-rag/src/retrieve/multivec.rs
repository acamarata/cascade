// Purpose: Multi-vector (ColBERT-style) retriever for the BGE-M3 tier-4 RAG mode.
//
// Gated behind feature `rag-multivec` (OFF by default). Loaded only when
// `cascade.toml [rag] multi_vec = true` and the feature is compiled in.
//
// Algorithm:
//   1. Embed query tokens via `MultiVecEmbedModel::embed_multi_vec`.
//   2. For each candidate chunk, load token vectors from `rag_token_embeddings`.
//   3. Score via MaxSim (sum of per-query-token maximums over all doc tokens).
//   4. Return chunks ranked by descending MaxSim score.
//
// Inputs:  query &str, pre-filtered candidate chunk IDs, DB connection, embedder
// Outputs: Vec<(chunk_id, maxsim_score)> sorted descending
// Constraints: feature = "rag-multivec"; not thread-safe by itself — callers wrap in Arc
// SPORT: MASTER-LIBS.md → cascade-rag::retrieve::multivec

use rusqlite::Connection;

use crate::embed::multivector::{load_token_embeddings, maxsim, MultiVecEmbedModel};
use crate::embed::EmbedError;

// ── MultiVecRetriever ─────────────────────────────────────────────────────────

/// ColBERT-style MaxSim retriever over pre-filtered candidate chunks.
///
/// This retriever is a **re-ranker** in the RAG pipeline: it expects a small
/// candidate set (e.g. top-50 from FTS5 or dense ANN) and re-ranks them using
/// MaxSim late-interaction scoring.  Running it against the full index would be
/// prohibitively expensive at scale.
///
/// # Example
///
/// ```rust,ignore
/// use cascade_rag::retrieve::multivec::MultiVecRetriever;
/// use cascade_rag::embed::multivector::MockMultiVecEmbedModel;
/// use rusqlite::Connection;
/// use std::sync::Arc;
///
/// let conn = Connection::open_in_memory().unwrap();
/// // (run migrations + store token embeddings beforehand)
/// let embedder = Arc::new(MockMultiVecEmbedModel::new(8));
/// let retriever = MultiVecRetriever::new(embedder);
/// let ranked = retriever.rerank(&conn, "query text", &[1, 2, 3]).unwrap();
/// ```
pub struct MultiVecRetriever<E: MultiVecEmbedModel + Sync> {
    embedder: std::sync::Arc<E>,
}

impl<E: MultiVecEmbedModel + Sync + Send> MultiVecRetriever<E> {
    /// Construct a `MultiVecRetriever` with the given embedder.
    pub fn new(embedder: std::sync::Arc<E>) -> Self {
        Self { embedder }
    }

    /// Re-rank `candidates` (chunk IDs) by MaxSim score against `query`.
    ///
    /// Returns `(chunk_id, maxsim_score)` pairs sorted **descending** by score.
    /// Chunks whose token embeddings are not stored are assigned score 0.0 and
    /// appear at the bottom of the list.
    ///
    /// # Errors
    ///
    /// Returns `EmbedError` if query embedding or DB access fails.
    pub fn rerank(
        &self,
        conn: &Connection,
        query: &str,
        candidates: &[i64],
    ) -> Result<Vec<(i64, f32)>, EmbedError> {
        if candidates.is_empty() {
            return Ok(vec![]);
        }

        // Embed query tokens.
        let q_vecs = self.embedder.embed_multi_vec(query)?;
        if q_vecs.is_empty() {
            // Empty query: return candidates in original order with score 0.
            return Ok(candidates.iter().map(|&id| (id, 0.0)).collect());
        }

        // Score each candidate.
        let mut scored: Vec<(i64, f32)> = candidates
            .iter()
            .map(|&chunk_id| {
                let d_vecs = load_token_embeddings(conn, chunk_id).unwrap_or_default();
                let score = if d_vecs.is_empty() {
                    0.0
                } else {
                    maxsim(&q_vecs, &d_vecs)
                };
                (chunk_id, score)
            })
            .collect();

        // Sort descending by MaxSim score; tie-break by chunk_id ascending.
        scored.sort_by(|a, b| {
            b.1.partial_cmp(&a.1)
                .unwrap_or(std::cmp::Ordering::Equal)
                .then_with(|| a.0.cmp(&b.0))
        });

        Ok(scored)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::embed::multivector::{store_token_embeddings, MockMultiVecEmbedModel};
    use rusqlite::Connection;
    use std::sync::Arc;

    fn setup_conn() -> Connection {
        let conn = Connection::open_in_memory().expect("in-memory DB");
        // run_migrations also applies migration 6 (rag_token_embeddings) when feature is on.
        crate::db::migrations::run_migrations(&conn).expect("base migrations");
        // Seed parent rows so FK constraints are satisfied.
        conn.execute_batch(
            "INSERT INTO rag_sources (id, file_path, schema_version) VALUES (1, '/test/doc.md', 1);
             INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, schema_version) VALUES (1, 1, 0, 'chunk one',   1);
             INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, schema_version) VALUES (2, 1, 1, 'chunk two',   1);
             INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, schema_version) VALUES (999, 1, 2, 'chunk 999', 1);",
        )
        .expect("seed parent rows for FK");
        conn
    }

    /// Rerank must order chunk with higher MaxSim score first.
    #[test]
    fn rerank_orders_by_maxsim_descending() {
        let conn = setup_conn();
        let dim = 4;
        let m = Arc::new(MockMultiVecEmbedModel::new(dim));

        // Store token vectors for two chunks using the mock model.
        let chunk1_vecs = m.embed_multi_vec("hello world").unwrap();
        let chunk2_vecs = m.embed_multi_vec("xyz abc qrs").unwrap();
        store_token_embeddings(&conn, 1, &chunk1_vecs).unwrap();
        store_token_embeddings(&conn, 2, &chunk2_vecs).unwrap();

        let retriever = MultiVecRetriever::new(Arc::clone(&m));

        // Query "hello world" should score chunk 1 higher (same tokens).
        let ranked = retriever.rerank(&conn, "hello world", &[1, 2]).unwrap();
        assert_eq!(ranked.len(), 2);
        assert_eq!(
            ranked[0].0, 1,
            "chunk 1 must rank first for query 'hello world'"
        );
        assert!(
            ranked[0].1 > 0.0,
            "MaxSim score must be positive for matching query"
        );
    }

    /// Rerank with empty candidates returns empty vec.
    #[test]
    fn rerank_empty_candidates_returns_empty() {
        let conn = setup_conn();
        let m = Arc::new(MockMultiVecEmbedModel::new(4));
        let retriever = MultiVecRetriever::new(m);
        let ranked = retriever.rerank(&conn, "query", &[]).unwrap();
        assert!(ranked.is_empty());
    }

    /// Chunks with no stored token embeddings get score 0.0.
    #[test]
    fn rerank_missing_token_embeddings_score_zero() {
        let conn = setup_conn();
        let m = Arc::new(MockMultiVecEmbedModel::new(4));
        let retriever = MultiVecRetriever::new(m);
        // chunk 999 has no stored embeddings.
        let ranked = retriever.rerank(&conn, "some query", &[999]).unwrap();
        assert_eq!(ranked.len(), 1);
        assert_eq!(ranked[0].0, 999);
        assert_eq!(ranked[0].1, 0.0, "missing token embeddings → score 0.0");
    }
}
