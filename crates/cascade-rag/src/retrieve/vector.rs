//! Dense vector ANN retriever backed by RagIndex dense store.
//!
//! Embeds the query string and executes a K-nearest-neighbour search against
//! the dense store (`vec_chunks` with the `vec` feature, `dense_fallback`
//! without it).
//!
//! ## Acceptance criteria (S05-06)
//!
//! ANN query returns top-K=10 results in < 200 ms on a 100K-vector index.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::VectorRetriever

use async_trait::async_trait;
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::{CascadeError, Result},
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
    EmbedOpts, EmbedUsage, EmbeddingProvider,
};

use crate::index::RagIndex;

/// Dense vector retriever using RagIndex KNN search.
pub struct VectorRetriever {
    /// Shared index handle providing `dense_query`.
    index: Arc<RagIndex>,
    embedder: Arc<dyn EmbeddingProvider>,
}

impl VectorRetriever {
    /// Construct from a shared index handle and an embedding provider.
    pub fn new(index: Arc<RagIndex>, embedder: Arc<dyn EmbeddingProvider>) -> Self {
        Self { index, embedder }
    }
}

#[async_trait]
impl Retriever for VectorRetriever {
    /// Embed `query`, execute a KNN search, and return scored hits.
    ///
    /// Score = `1 / (1 + distance)` — higher is better, in `(0, 1]`.
    #[instrument(skip(self), fields(k = opts.k))]
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        // Embed the query string.
        let embed_opts = EmbedOpts {
            usage: EmbedUsage::Query,
            model_override: None,
            truncate_dim: None,
        };
        let embeddings = self.embedder.embed(&[query], &embed_opts).await?;
        let query_vec =
            embeddings
                .into_iter()
                .next()
                .ok_or_else(|| CascadeError::EmbeddingFailed {
                    provider: "unknown".into(),
                    detail: "empty embedding result".into(),
                })?;

        // Execute KNN via the index's dense store.
        let knn = self.index.dense_query(&query_vec.values, opts.k).await?;

        // Map (chunk_id, distance) → RetrievalHit.
        let hits = knn
            .into_iter()
            .enumerate()
            .map(|(rank, (chunk_id, dist))| RetrievalHit {
                chunk_id,
                text: String::new(),      // populated by higher-level caller if needed
                file_path: None,
                start_line: None,
                end_line: None,
                score: 1.0 / (1.0 + dist),
                rank,
                tier: None,
            })
            .collect();

        Ok(hits)
    }

    fn name(&self) -> &str {
        "sqlite-vec-ann"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use cascade_types::{Embedding, EmbedOpts, EmbeddingProvider};
    use std::sync::Arc;
    use tempfile::NamedTempFile;

    // ── Mock embedder ─────────────────────────────────────────────────────────

    /// Returns a fixed vector regardless of input — for deterministic tests.
    struct FixedEmbedder {
        vec: Vec<f32>,
    }

    #[async_trait]
    impl EmbeddingProvider for FixedEmbedder {
        async fn embed(
            &self,
            _texts: &[&str],
            _opts: &EmbedOpts,
        ) -> cascade_types::error::Result<Vec<Embedding>> {
            Ok(vec![Embedding {
                values: self.vec.clone(),
                token_count: None,
            }])
        }

        fn dimension(&self) -> usize {
            self.vec.len()
        }
    }

    // ── Helper ────────────────────────────────────────────────────────────────

    async fn open_index_with_dim(
        path: &std::path::Path,
        dim: usize,
    ) -> Arc<RagIndex> {
        Arc::new(
            RagIndex::open_with_dim(path, dim)
                .await
                .expect("RagIndex::open_with_dim"),
        )
    }

    // ── dense_query: nearest neighbour is top-1 ───────────────────────────────

    /// Insert two chunks with known embeddings; query with the embedding of chunk A.
    /// Chunk A must be top-1, chunk B second, and scores must be in (0, 1].
    #[tokio::test]
    async fn dense_query_returns_nearest_top1() {
        let dim = 4usize;
        let tmp = NamedTempFile::new().expect("tempfile");
        let idx = open_index_with_dim(tmp.path(), dim).await;

        // Insert two chunks.
        idx.upsert_chunk("a", None, None, None, "chunk a", None)
            .await
            .unwrap();
        idx.upsert_chunk("b", None, None, None, "chunk b", None)
            .await
            .unwrap();

        let embed_a = vec![1.0f32, 0.0, 0.0, 0.0];
        let embed_b = vec![0.0f32, 1.0, 0.0, 0.0];
        idx.upsert_embedding("a", &embed_a).await.unwrap();
        idx.upsert_embedding("b", &embed_b).await.unwrap();

        // Query with exactly embed_a — distance to "a" must be 0 (top-1).
        let results = idx.dense_query(&embed_a, 2).await.unwrap();
        assert_eq!(results.len(), 2, "must return 2 results");
        assert_eq!(results[0].0, "a", "nearest must be chunk 'a'");
        assert!(results[0].1 < 1e-6, "distance to exact match must be ~0");
        assert!(
            results[1].1 > results[0].1,
            "chunk 'b' must be farther than 'a'"
        );
    }

    // ── VectorRetriever::retrieve wires dense_query ───────────────────────────

    /// Verify that VectorRetriever::retrieve returns the right chunk as rank-0
    /// and that scores are in (0, 1].
    #[tokio::test]
    async fn vector_retriever_returns_nearest_chunk() {
        let dim = 4usize;
        let tmp = NamedTempFile::new().expect("tempfile");
        let idx = open_index_with_dim(tmp.path(), dim).await;

        let embed_a = vec![1.0f32, 0.0, 0.0, 0.0];
        let embed_b = vec![0.0f32, 0.0, 1.0, 0.0];

        idx.upsert_chunk("a", None, None, None, "text a", None)
            .await
            .unwrap();
        idx.upsert_chunk("b", None, None, None, "text b", None)
            .await
            .unwrap();
        idx.upsert_embedding("a", &embed_a).await.unwrap();
        idx.upsert_embedding("b", &embed_b).await.unwrap();

        // Mock embedder returns embed_a so "a" should be nearest.
        let embedder: Arc<dyn EmbeddingProvider> =
            Arc::new(FixedEmbedder { vec: embed_a.clone() });
        let retriever = VectorRetriever::new(Arc::clone(&idx), embedder);

        let opts = RetrieveOpts {
            k: 2,
            ..Default::default()
        };
        let hits = retriever.retrieve("ignored query", &opts).await.unwrap();

        assert_eq!(hits.len(), 2, "must return 2 hits");
        assert_eq!(hits[0].chunk_id, "a", "top-1 must be chunk 'a'");
        assert_eq!(hits[0].rank, 0, "rank of top hit must be 0");
        assert!(
            hits[0].score > hits[1].score,
            "nearest chunk must have higher score"
        );
        assert!(
            hits[0].score > 0.0 && hits[0].score <= 1.0,
            "score must be in (0, 1]"
        );
    }
}
