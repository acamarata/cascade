//! Jina Embeddings v3 provider — stub.
//!
//! # Purpose
//!
//! Placeholder implementation. Full Jina embedding is NOT available in this
//! build — selecting this provider fails loud at construction time
//! ([`JinaProvider::new`] returns `Err`) rather than surfacing a generic
//! `EmbedError::Unimplemented` deep in the embed call path. Full implementation
//! is tracked in E-P7-20 (Deferred-Backlog Group B).
//!
//! # Constraints
//!
//! - [`JinaProvider::new`] returns `Err(CascadeError::Other)` with a clear
//!   "not available in this build" message (capability check at construction).
//! - `EmbedModel` / `EmbeddingProvider` methods return `EmbedError::Unimplemented`
//!   as a defense-in-depth fallback (e.g. if constructed via `Default`).
//! - Does NOT panic.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed::jina

use async_trait::async_trait;

use cascade_types::{
    error::{CascadeError, Result},
    EmbedOpts, Embedding, EmbeddingProvider, ProviderKind,
};

use super::{EmbedError, EmbedModel};

/// Embedding dimension for Jina Embeddings v3.
const JINA_DIM: usize = 1024;

/// Jina Embeddings v3 provider (stub — not available in this build).
///
/// Full implementation is tracked in E-P7-20 (Deferred-Backlog Group B).
/// [`JinaProvider::new`] fails loud at construction time with a clear
/// "not available in this build" error. The `EmbedModel` /
/// `EmbeddingProvider` methods return [`EmbedError::Unimplemented`] only as a
/// defense-in-depth fallback (e.g. if a value is obtained via `Default`).
#[derive(Debug, Default, Clone)]
pub struct JinaProvider;

impl JinaProvider {
    /// Construct a new Jina provider.
    ///
    /// # Errors
    ///
    /// Always returns `Err` — the Jina embedding provider is not available
    /// in this build. Full implementation is tracked in E-P7-20
    /// (Deferred-Backlog Group B). This fails loud at construction time
    /// (provider-selection) rather than surfacing a generic error deep in the
    /// embed call path.
    pub fn new() -> Result<Self> {
        Err(CascadeError::Other(
            "embedding provider 'jina' is not available in this build — full implementation is \
             tracked in E-P7-20 (Deferred-Backlog Group B). Use 'bge-m3' (the default \
             Multilingual E5 Large compatibility mode) instead."
                .to_string(),
        ))
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

    /// `new()` must fail loud at construction time with a clear "not available
    /// in this build" capability error — NOT a generic error deep in the embed
    /// call path (T-P7-E14-01).
    #[test]
    fn jina_new_returns_capability_error() {
        let result = JinaProvider::new();
        let err = result.expect_err("new() must Err — jina is not available in this build");
        match err {
            CascadeError::Other(msg) => {
                assert!(
                    msg.contains("not available in this build"),
                    "error must say 'not available in this build', got: {msg}"
                );
                assert!(
                    msg.contains("E-P7-20"),
                    "error must reference E-P7-20 tracking, got: {msg}"
                );
                assert!(
                    msg.contains("jina"),
                    "error must name the provider, got: {msg}"
                );
            }
            other => panic!("expected CascadeError::Other, got {other:?}"),
        }
    }

    // The remaining tests exercise the defense-in-depth embed fallback path
    // (e.g. if a value is obtained via `Default` bypassing `new()`). The
    // primary, loud failure is `new()` above; these ensure the embed methods
    // still error rather than silently succeeding.

    #[test]
    fn jina_embed_dense_returns_unimplemented() {
        let p = JinaProvider;
        let result = p.embed_dense(&["hello"]);
        assert!(
            matches!(result, Err(EmbedError::Unimplemented { provider: "jina" })),
            "embed_dense must return Unimplemented, got: {result:?}"
        );
    }

    #[test]
    fn jina_embed_sparse_returns_unimplemented() {
        let p = JinaProvider;
        let result = p.embed_sparse(&["hello"]);
        assert!(
            matches!(result, Err(EmbedError::Unimplemented { provider: "jina" })),
            "embed_sparse must return Unimplemented, got: {result:?}"
        );
    }

    #[test]
    fn jina_does_not_panic_on_empty_input() {
        let p = JinaProvider;
        // Even empty input must return Err, not panic.
        let _ = p.embed_dense(&[]);
        let _ = p.embed_sparse(&[]);
    }

    #[tokio::test]
    async fn jina_provider_embed_returns_err() {
        let p = JinaProvider;
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
