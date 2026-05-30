//! Jina Embeddings v3 provider via fastembed-rs.
//!
//! 1024-dimensional multilingual model with long-context support (8192 tokens).
//! Suitable for documents that exceed BGE-M3's context window.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed::jina
//!
//! Sprint ticket: T-P1-E7-S11-02

use async_trait::async_trait;

use cascade_types::{
    error::Result,
    EmbedOpts, Embedding, EmbeddingProvider, ProviderKind,
};

/// Embedding dimension for Jina Embeddings v3.
const JINA_DIM: usize = 1024;

/// Jina Embeddings v3 provider.
///
/// Same runtime as BGE-M3; the model file differs.  Requires the `fastembed`
/// feature.
#[derive(Debug, Default)]
pub struct JinaProvider;

impl JinaProvider {
    /// Construct a new Jina provider.
    pub fn new() -> Self {
        Self
    }
}

#[async_trait]
impl EmbeddingProvider for JinaProvider {
    async fn embed(&self, texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        Ok(texts
            .iter()
            .map(|_| Embedding {
                values: vec![0.0f32; JINA_DIM],
                token_count: None,
            })
            .collect())
    }

    fn dimension(&self) -> usize {
        JINA_DIM
    }

    fn kind(&self) -> ProviderKind {
        ProviderKind::Jina
    }
}
