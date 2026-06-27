//! Dense vector ANN retriever backed by sqlite-vec.
//!
//! Embeds the query string and executes a K-nearest-neighbour search against
//! the `vec_chunks` virtual table using cosine distance.
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

/// Dense vector retriever using sqlite-vec KNN search.
pub struct VectorRetriever {
    /// Shared index handle — wired to the ANN query in T-P1-E7-S05-03.
    _index: Arc<RagIndex>,
    embedder: Arc<dyn EmbeddingProvider>,
}

impl VectorRetriever {
    /// Construct from a shared index handle and an embedding provider.
    pub fn new(index: Arc<RagIndex>, embedder: Arc<dyn EmbeddingProvider>) -> Self {
        Self {
            _index: index,
            embedder,
        }
    }
}

#[async_trait]
impl Retriever for VectorRetriever {
    /// Embed `query` and execute a KNN search.
    ///
    /// Returns cosine-similarity scores normalised to `[0.0, 1.0]`.
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

        // Serialise as little-endian f32 blob for sqlite-vec.
        let blob: Vec<u8> = query_vec
            .values
            .iter()
            .flat_map(|f| f.to_le_bytes())
            .collect();

        // Execute ANN query via sqlite-vec `vec_distance_cosine`.
        // The full SQL (implemented in T-P1-E7-S05-03) looks like:
        //   SELECT chunk_id, vec_distance_cosine(embedding, ?1) AS dist
        //   FROM vec_chunks
        //   ORDER BY dist ASC
        //   LIMIT ?2
        //
        // For this scaffold we return an empty result set; the index module
        // will expose the query method in S05.
        let _ = blob; // used when sqlite-vec query is wired
        let _ = opts.k;

        // TODO(T-P1-E7-S05-03): execute ANN query against vec_chunks table.
        Ok(vec![])
    }

    fn name(&self) -> &str {
        "sqlite-vec-ann"
    }
}
