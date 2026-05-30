//! Nomic Embed Text v1.5 provider via fastembed-rs.
//!
//! 768-dimensional, English-optimised open-source model.
//! Suitable when BGE-M3's multilingual capacity is not required and a smaller
//! memory footprint is preferred.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed::nomic
//!
//! Sprint ticket: T-P1-E7-S11-01

use async_trait::async_trait;

use cascade_types::{
    error::Result,
    EmbedOpts, Embedding, EmbeddingProvider, ProviderKind,
};

/// Embedding dimension for Nomic Embed Text v1.5.
const NOMIC_DIM: usize = 768;

/// Nomic Embed Text v1.5 provider.
///
/// Requires the `fastembed` feature.  Uses the same fastembed-rs ONNX runtime
/// as [`crate::embed::bge_m3::BgeM3Provider`]; only the model variant differs.
///
/// Privacy: fully local, no data leaves the machine.
#[derive(Debug, Default)]
pub struct NomicProvider;

impl NomicProvider {
    /// Construct a new Nomic provider.  Does not download the model — that
    /// happens lazily on the first [`embed`] call.
    pub fn new() -> Self {
        Self
    }
}

#[async_trait]
impl EmbeddingProvider for NomicProvider {
    /// Embed `texts` using Nomic Embed Text v1.5.
    ///
    /// Returns 768-dim vectors.  Stub implementation — full body in
    /// T-P1-E7-S11-01.
    async fn embed(&self, texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        Ok(texts
            .iter()
            .map(|_| Embedding {
                values: vec![0.0f32; NOMIC_DIM],
                token_count: None,
            })
            .collect())
    }

    fn dimension(&self) -> usize {
        NOMIC_DIM
    }

    fn kind(&self) -> ProviderKind {
        ProviderKind::Nomic
    }
}
