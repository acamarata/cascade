//! Nomic Embed Text v1.5 provider — stub.
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
//! SPORT: MASTER-LIBS.md → cascade-rag::embed::nomic

use async_trait::async_trait;

use cascade_types::{error::Result, EmbedOpts, Embedding, EmbeddingProvider, ProviderKind};

use super::{EmbedError, EmbedModel};

/// Embedding dimension for Nomic Embed Text v1.5.
const NOMIC_DIM: usize = 768;

/// Nomic Embed Text v1.5 provider (stub — returns `Unimplemented`).
///
/// Full implementation deferred to P5.  Using this provider in production
/// configuration will produce `CascadeError::EmbeddingFailed` on first
/// embed call.
#[derive(Debug, Default, Clone)]
pub struct NomicProvider;

impl NomicProvider {
    /// Construct a new Nomic provider stub.
    pub fn new() -> Self {
        Self
    }
}

impl EmbedModel for NomicProvider {
    fn embed_dense(&self, _texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        Err(EmbedError::Unimplemented { provider: "nomic" })
    }

    fn embed_sparse(
        &self,
        _texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        Err(EmbedError::Unimplemented { provider: "nomic" })
    }

    fn dim(&self) -> usize {
        NOMIC_DIM
    }

    fn model_id(&self) -> &str {
        "nomic-embed-text-v1.5"
    }
}

#[async_trait]
impl EmbeddingProvider for NomicProvider {
    async fn embed(&self, _texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        Err(EmbedError::Unimplemented { provider: "nomic" }.into())
    }

    fn dimension(&self) -> usize {
        NOMIC_DIM
    }

    fn kind(&self) -> ProviderKind {
        ProviderKind::Nomic
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nomic_embed_dense_returns_unimplemented() {
        let p = NomicProvider::new();
        let result = p.embed_dense(&["hello"]);
        assert!(
            matches!(result, Err(EmbedError::Unimplemented { provider: "nomic" })),
            "embed_dense must return Unimplemented, got: {result:?}"
        );
    }

    #[test]
    fn nomic_embed_sparse_returns_unimplemented() {
        let p = NomicProvider::new();
        let result = p.embed_sparse(&["hello"]);
        assert!(
            matches!(result, Err(EmbedError::Unimplemented { provider: "nomic" })),
            "embed_sparse must return Unimplemented, got: {result:?}"
        );
    }

    #[test]
    fn nomic_does_not_panic_on_empty_input() {
        let p = NomicProvider::new();
        let _ = p.embed_dense(&[]);
        let _ = p.embed_sparse(&[]);
    }

    #[tokio::test]
    async fn nomic_provider_embed_returns_err() {
        use cascade_types::error::CascadeError;
        let p = NomicProvider::new();
        let result = p
            .embed(&["hello"], &cascade_types::EmbedOpts::default())
            .await;
        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CascadeError::EmbeddingFailed { .. }
        ));
    }
}
