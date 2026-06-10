//! Jina Embeddings v3 provider — stub.
//!
//! # Purpose
//!
//! Placeholder implementation returning `EmbedError::Unimplemented`.
//! Full implementation is scheduled for P5.
//!
//! # Constraints
//!
//! - All `EmbedModel` methods return `Err(EmbedError::Unimplemented)`.
//! - All `EmbeddingProvider` methods return `Err(CascadeError::EmbeddingFailed)`.
//! - Does NOT panic.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed::jina

use async_trait::async_trait;

use cascade_types::{error::Result, EmbedOpts, Embedding, EmbeddingProvider, ProviderKind};

use super::{EmbedError, EmbedModel};

/// Embedding dimension for Jina Embeddings v3.
const JINA_DIM: usize = 1024;

/// Jina Embeddings v3 provider (stub — returns `Unimplemented`).
///
/// Full implementation deferred to P5.  Using this provider in production
/// configuration will produce `CascadeError::EmbeddingFailed` on first
/// embed call.
#[derive(Debug, Default, Clone)]
pub struct JinaProvider;

impl JinaProvider {
    /// Construct a new Jina provider stub.
    pub fn new() -> Self {
        Self
    }
}

impl EmbedModel for JinaProvider {
    fn embed_dense(&self, _texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        Err(EmbedError::Unimplemented { provider: "jina" })
    }

    fn embed_sparse(
        &self,
        _texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        Err(EmbedError::Unimplemented { provider: "jina" })
    }

    fn dim(&self) -> usize {
        JINA_DIM
    }

    fn model_id(&self) -> &str {
        "jina-embeddings-v3"
    }
}

#[async_trait]
impl EmbeddingProvider for JinaProvider {
    async fn embed(&self, _texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        Err(EmbedError::Unimplemented { provider: "jina" }.into())
    }

    fn dimension(&self) -> usize {
        JINA_DIM
    }

    fn kind(&self) -> ProviderKind {
        ProviderKind::Jina
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn jina_embed_dense_returns_unimplemented() {
        let p = JinaProvider::new();
        let result = p.embed_dense(&["hello"]);
        assert!(
            matches!(result, Err(EmbedError::Unimplemented { provider: "jina" })),
            "embed_dense must return Unimplemented, got: {result:?}"
        );
    }

    #[test]
    fn jina_embed_sparse_returns_unimplemented() {
        let p = JinaProvider::new();
        let result = p.embed_sparse(&["hello"]);
        assert!(
            matches!(result, Err(EmbedError::Unimplemented { provider: "jina" })),
            "embed_sparse must return Unimplemented, got: {result:?}"
        );
    }

    #[test]
    fn jina_does_not_panic_on_empty_input() {
        let p = JinaProvider::new();
        // Even empty input must return Err, not panic.
        let _ = p.embed_dense(&[]);
        let _ = p.embed_sparse(&[]);
    }

    #[tokio::test]
    async fn jina_provider_embed_returns_err() {
        use cascade_types::error::CascadeError;
        let p = JinaProvider::new();
        let result = p.embed(&["hello"], &cascade_types::EmbedOpts::default()).await;
        assert!(result.is_err());
        assert!(matches!(result.unwrap_err(), CascadeError::EmbeddingFailed { .. }));
    }
}
