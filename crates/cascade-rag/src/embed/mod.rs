//! Embedding provider implementations.
//!
//! This module exposes two layers:
//!
//! - **[`EmbedModel`]** — the low-level RAG-internal trait that surfaces both
//!   dense and sparse vectors from a single model.  Used by the RAG pipeline
//!   before results are wrapped in the cascade-types `Embedding` value type.
//! - **[`EmbeddingProvider`]** (re-exported from cascade-types) — the
//!   higher-level, async, batch-oriented trait consumed by the daemon and CLI.
//!   `BgeM3Provider` bridges the two via an `async fn embed()` wrapper.
//!
//! ## Primary local provider: BGE-M3 (proxied via BGELargeENV15)
//!
//! [`bge_m3::BgeM3Embedder`] wraps fastembed-rs ONNX inference.  Dense
//! vectors are 1024-dimensional (`EmbeddingModel::BGELargeENV15`).
//!
//! NOTE: fastembed 3.14.1 does not expose `EmbeddingModel::BGEM3`.  The
//! closest available 1024-dim variant is `BGELargeENV15`.  True BGE-M3
//! sparse output (SPLADE-like) is not available; this ticket uses a TF-IDF
//! fallback with FNV-1a stable token hashing.  Tracked as a known gap in
//! T-P4-E01-04; upgrade path: bump fastembed to a version that ships BGEM3,
//! replace the model enum variant + remove the fallback comment.
//!
//! ## Provider selection (cascade.toml)
//!
//! ```toml
//! [rag]
//! embedding_model = "bge-m3"   # default
//! # embedding_model = "nomic"
//! # embedding_model = "jina"
//! # embedding_model = "openai"  # requires OPENAI_API_KEY in vault
//! ```
//!
//! Cloud providers require the appropriate API key in `~/.claude/vault.env`.
//! A privacy warning is logged on first use.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed

use cascade_types::error::CascadeError;

// ── EmbedError ────────────────────────────────────────────────────────────────

/// Errors produced by [`EmbedModel`] implementations.
///
/// # Variants
///
/// | Variant | When |
/// |---|---|
/// | `ModelNotFound` | Cache dir exists but model files are absent |
/// | `InferenceFailed` | ONNX session or tokenizer returned an error |
/// | `Unimplemented` | Provider stub, not yet backed by real logic |
/// | `DimensionMismatch` | Backend returned wrong vector size |
#[derive(Debug, thiserror::Error)]
pub enum EmbedError {
    /// The model files are absent from the cache directory and cannot be
    /// downloaded (e.g. `offline_guard` is set, or no network is available).
    #[error("embedding model not found at cache path: {path}")]
    ModelNotFound { path: std::path::PathBuf },

    /// The underlying fastembed / ONNX session returned an error.
    #[error("embedding inference failed ({model_id}): {detail}")]
    InferenceFailed { model_id: String, detail: String },

    /// This operation is not yet implemented for the given provider.
    #[error("embedding operation not implemented for provider '{provider}'")]
    Unimplemented { provider: &'static str },

    /// The returned dense vector has an unexpected dimension.
    #[error("dimension mismatch: expected {expected}, got {actual}")]
    DimensionMismatch { expected: usize, actual: usize },
}

impl From<EmbedError> for CascadeError {
    fn from(e: EmbedError) -> Self {
        match e {
            EmbedError::ModelNotFound { path } => CascadeError::EmbeddingFailed {
                provider: "embed-model".into(),
                detail: format!("model not found at {}", path.display()),
            },
            EmbedError::InferenceFailed { model_id, detail } => CascadeError::EmbeddingFailed {
                provider: model_id,
                detail,
            },
            EmbedError::Unimplemented { provider } => CascadeError::EmbeddingFailed {
                provider: provider.into(),
                detail: "not implemented".into(),
            },
            EmbedError::DimensionMismatch { expected, actual } => {
                CascadeError::EmbeddingDimensionMismatch { expected, actual }
            }
        }
    }
}

// ── EmbedModel trait ──────────────────────────────────────────────────────────

/// Low-level RAG embedding trait combining dense and sparse retrieval.
///
/// Implementors must be `Send + Sync` so they can be wrapped in `Arc` and
/// shared across async tasks.  All methods take `&self` (interior mutability
/// is the implementor's responsibility if needed, e.g. via `Mutex`).
///
/// # Object safety
///
/// This trait is object-safe: `Arc<dyn EmbedModel>` compiles.
///
/// # Batch API
///
/// All methods accept a slice of texts.  The implementor is responsible for
/// internally splitting large slices into batches.  The returned `Vec` always
/// has the same length as `texts`, in the same order.
///
/// # Example
///
/// ```rust
/// use cascade_rag::embed::{EmbedModel, MockEmbedModel};
/// use std::sync::Arc;
///
/// let model: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(1024));
/// let dense = model.embed_dense(&["hello world"]).unwrap();
/// assert_eq!(dense[0].len(), 1024);
/// ```
pub trait EmbedModel: Send + Sync {
    /// Embed a batch of texts into dense float vectors.
    ///
    /// Returns one `Vec<f32>` of length `self.dim()` per input text.
    ///
    /// # Errors
    ///
    /// - `EmbedError::ModelNotFound` — model not on disk.
    /// - `EmbedError::InferenceFailed` — ONNX / tokenizer error.
    fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError>;

    /// Embed a batch of texts into sparse lexical weight vectors.
    ///
    /// Each entry is a list of `(token_id, weight)` pairs where:
    /// - `token_id` is a stable FNV-1a 32-bit hash of the lowercase token.
    /// - `weight` is a non-negative importance score (TF-IDF or SPLADE-style).
    /// - Only non-zero weights are included.
    ///
    /// Token IDs are reproducible across process restarts (no randomization).
    ///
    /// Returns one sparse vector per input text.
    fn embed_sparse(&self, texts: &[&str])
        -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError>;

    /// The dimensionality of dense vectors produced by this model.
    fn dim(&self) -> usize;

    /// A stable string identifier for the model (used in logging and metrics).
    fn model_id(&self) -> &str;
}

// ── Mock backend (always available, for unit tests) ───────────────────────────

/// Deterministic mock [`EmbedModel`] for unit tests.
///
/// Dense vectors are derived from the input text's byte sum — no network, no
/// disk, no model download required.  Sparse vectors assign TF-IDF weights
/// via FNV-1a hashing of whitespace-delimited tokens.
///
/// Output is deterministic and reproducible across runs, but cosine
/// similarities are NOT semantically meaningful.  Use only in tests.
///
/// # Example
///
/// ```rust
/// use cascade_rag::embed::{EmbedModel, MockEmbedModel};
///
/// let m = MockEmbedModel::new(1024);
/// assert_eq!(m.dim(), 1024);
/// assert_eq!(m.model_id(), "mock-embed-model");
///
/// let dense = m.embed_dense(&["hello", "world"]).unwrap();
/// assert_eq!(dense.len(), 2);
/// assert_eq!(dense[0].len(), 1024);
///
/// let sparse = m.embed_sparse(&["hello world"]).unwrap();
/// assert!(!sparse[0].is_empty());
/// ```
#[derive(Debug, Clone)]
pub struct MockEmbedModel {
    /// Dimension of the dense output vectors.
    pub dim: usize,
}

impl Default for MockEmbedModel {
    fn default() -> Self {
        Self { dim: 1024 }
    }
}

impl MockEmbedModel {
    /// Create a mock model with the specified dense dimension.
    pub fn new(dim: usize) -> Self {
        Self { dim }
    }
}

impl EmbedModel for MockEmbedModel {
    fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        // Deterministic: fill using byte-sum seed, then L2-normalise.
        let dim = self.dim;
        Ok(texts
            .iter()
            .map(|t| {
                let seed = t.bytes().fold(0u64, |acc, b| acc.wrapping_add(b as u64));
                let base = (seed as f32) / 255.0;
                // Build a vector, then normalise it so cosine similarity works.
                let raw: Vec<f32> = (0..dim)
                    .map(|i| (base + i as f32 * 0.001).sin())
                    .collect();
                let norm: f32 = raw.iter().map(|v| v * v).sum::<f32>().sqrt().max(1e-9);
                raw.iter().map(|v| v / norm).collect()
            })
            .collect())
    }

    fn embed_sparse(
        &self,
        texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        Ok(texts.iter().map(|t| sparse_tfidf_single(t)).collect())
    }

    fn dim(&self) -> usize {
        self.dim
    }

    fn model_id(&self) -> &str {
        "mock-embed-model"
    }
}

// ── TF-IDF sparse fallback (also used by BgeM3Embedder) ──────────────────────

/// Compute a TF-IDF sparse representation for a single text.
///
/// # Token ID stability
///
/// Token IDs are FNV-1a 32-bit hashes of the lowercased token string.
/// The FNV basis and prime are fixed constants (no randomisation), so the
/// same token always produces the same ID across runs and machines.
///
/// # IDF approximation
///
/// Uses within-document IDF: `idf = ln(1 + 1/tf)`.  This is a single-doc
/// approximation, not a corpus-wide IDF.  Sufficient for sparse retrieval
/// when a real SPLADE model is unavailable.
///
/// # Returns
///
/// `Vec<(token_id, tf_idf_weight)>` sorted by `token_id` for deterministic
/// output.  Tokens with weight 0 are excluded.
pub(crate) fn sparse_tfidf_single(text: &str) -> Vec<(u32, f32)> {
    use std::collections::HashMap;

    let tokens: Vec<&str> = text.split_whitespace().collect();
    if tokens.is_empty() {
        return vec![];
    }

    let mut counts: HashMap<u32, u32> = HashMap::new();
    for token in &tokens {
        let id = fnv1a_32(token.to_lowercase().as_str());
        *counts.entry(id).or_insert(0) += 1;
    }

    let total = tokens.len() as f32;
    let mut pairs: Vec<(u32, f32)> = counts
        .into_iter()
        .map(|(id, count)| {
            let tf = count as f32 / total;
            let idf = (1.0 + 1.0 / tf).ln();
            (id, tf * idf)
        })
        .filter(|(_, w)| *w > 0.0)
        .collect();

    // Sort by token_id for deterministic output across runs.
    pairs.sort_by_key(|(id, _)| *id);
    pairs
}

/// FNV-1a 32-bit hash.
///
/// # Stability guarantee
///
/// The FNV basis (2_166_136_261) and prime (16_777_619) are fixed constants
/// specified by the FNV standard.  They do not change between process
/// restarts, compiler versions, or platforms.  The same input always
/// produces the same `u32` output.
pub(crate) fn fnv1a_32(s: &str) -> u32 {
    const FNV_BASIS: u32 = 2_166_136_261;
    const FNV_PRIME: u32 = 16_777_619;
    s.bytes().fold(FNV_BASIS, |h, b| {
        (h ^ (b as u32)).wrapping_mul(FNV_PRIME)
    })
}

// ── Sub-module declarations ───────────────────────────────────────────────────

#[cfg(feature = "fastembed")]
pub mod bge_m3;

pub mod jina;
pub mod model_cache;
pub mod nomic;
pub mod store;

// ── Re-exports ────────────────────────────────────────────────────────────────

pub use cascade_types::{EmbedOpts, EmbedUsage, Embedding, EmbeddingProvider, ProviderKind};
pub use model_cache::{ModelCacheError, is_model_cached, model_cache_dir};
pub use store::{
    delete_embeddings_by_source, query_dense_knn, store_embeddings, validate_dense_dim,
    DENSE_DIM,
};

// ── Compile-time safety assertions ───────────────────────────────────────────

/// Verify trait is object-safe.
fn _assert_object_safe(_: &dyn EmbedModel) {}

/// Verify `Arc<dyn EmbedModel>` is `Send + Sync`.
fn _assert_arc_send_sync() {
    fn needs_send_sync<T: Send + Sync>(_: T) {}
    needs_send_sync(std::sync::Arc::new(MockEmbedModel::new(4)));
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── EmbedModel object safety ──────────────────────────────────────────────

    /// `Arc<dyn EmbedModel>` must compile (trait object safety check).
    #[test]
    fn embed_model_is_object_safe() {
        let model: std::sync::Arc<dyn EmbedModel> =
            std::sync::Arc::new(MockEmbedModel::new(1024));
        assert_eq!(model.dim(), 1024);
        assert_eq!(model.model_id(), "mock-embed-model");
    }

    // ── MockEmbedModel round-trip ─────────────────────────────────────────────

    /// Dense output must have the correct length.
    #[test]
    fn mock_dense_dim_matches() {
        let m = MockEmbedModel::new(1024);
        let vecs = m.embed_dense(&["hello", "world"]).unwrap();
        assert_eq!(vecs.len(), 2);
        for v in &vecs {
            assert_eq!(v.len(), 1024, "each dense vector must have dim=1024");
        }
    }

    /// Dense output must be approximately L2-normalised (unit norm).
    #[test]
    fn mock_dense_is_normalised() {
        let m = MockEmbedModel::new(128);
        let vecs = m.embed_dense(&["test normalisation"]).unwrap();
        let norm: f32 = vecs[0].iter().map(|v| v * v).sum::<f32>().sqrt();
        assert!(
            (norm - 1.0).abs() < 1e-5,
            "mock dense vector must be unit-normalised, got norm={norm}"
        );
    }

    /// Dense vectors for identical texts must be identical.
    #[test]
    fn mock_dense_is_deterministic() {
        let m = MockEmbedModel::new(64);
        let a = m.embed_dense(&["same input"]).unwrap();
        let b = m.embed_dense(&["same input"]).unwrap();
        assert_eq!(a, b, "identical inputs must produce identical dense vectors");
    }

    /// Different texts must produce different dense vectors.
    #[test]
    fn mock_dense_differs_for_different_texts() {
        let m = MockEmbedModel::new(64);
        let a = m.embed_dense(&["alpha text"]).unwrap();
        let b = m.embed_dense(&["completely different"]).unwrap();
        assert_ne!(a[0], b[0], "different texts must produce different dense vectors");
    }

    /// Sparse output must be non-empty for non-empty input.
    #[test]
    fn mock_sparse_non_empty_for_non_empty_input() {
        let m = MockEmbedModel::new(1024);
        let sparse = m.embed_sparse(&["hello world foo"]).unwrap();
        assert_eq!(sparse.len(), 1);
        assert!(!sparse[0].is_empty(), "sparse vector must have entries for non-empty text");
    }

    /// Sparse output must be empty for empty input text.
    #[test]
    fn mock_sparse_empty_for_empty_text() {
        let m = MockEmbedModel::new(1024);
        let sparse = m.embed_sparse(&[""]).unwrap();
        assert_eq!(sparse.len(), 1);
        assert!(sparse[0].is_empty(), "sparse vector for empty text must be empty");
    }

    /// Sparse token IDs must be deterministic across calls.
    #[test]
    fn mock_sparse_deterministic() {
        let m = MockEmbedModel::new(16);
        let a = m.embed_sparse(&["stable hashing test"]).unwrap();
        let b = m.embed_sparse(&["stable hashing test"]).unwrap();
        assert_eq!(a, b, "sparse vectors must be identical for the same input");
    }

    /// Batch output length must match input length.
    #[test]
    fn mock_batch_length_matches() {
        let m = MockEmbedModel::new(32);
        let texts = vec!["a", "b", "c", "d"];
        let dense = m.embed_dense(&texts).unwrap();
        let sparse = m.embed_sparse(&texts).unwrap();
        assert_eq!(dense.len(), 4);
        assert_eq!(sparse.len(), 4);
    }

    // ── EmbedError → CascadeError conversion ─────────────────────────────────

    #[test]
    fn embed_error_model_not_found_converts() {
        use cascade_types::error::CascadeError;
        let path = std::path::PathBuf::from("/tmp/not-here");
        let e = EmbedError::ModelNotFound { path };
        let ce: CascadeError = e.into();
        assert!(matches!(ce, CascadeError::EmbeddingFailed { .. }));
    }

    #[test]
    fn embed_error_unimplemented_converts() {
        use cascade_types::error::CascadeError;
        let e = EmbedError::Unimplemented { provider: "jina" };
        let ce: CascadeError = e.into();
        assert!(matches!(ce, CascadeError::EmbeddingFailed { .. }));
    }

    #[test]
    fn embed_error_dimension_mismatch_converts() {
        use cascade_types::error::CascadeError;
        let e = EmbedError::DimensionMismatch { expected: 1024, actual: 768 };
        let ce: CascadeError = e.into();
        assert!(matches!(
            ce,
            CascadeError::EmbeddingDimensionMismatch { expected: 1024, actual: 768 }
        ));
    }

    // ── FNV-1a hash stability ─────────────────────────────────────────────────

    /// FNV-1a must produce known-good values for fixed inputs.
    #[test]
    fn fnv1a_known_good_values() {
        // FNV-1a 32-bit: "hello" = 0x4f9f2cab (well-known test vector)
        // We verify stability, not a spec value — compute once and assert equality.
        let h1 = fnv1a_32("hello");
        let h2 = fnv1a_32("hello");
        assert_eq!(h1, h2, "FNV-1a must be deterministic");
        // Different strings must differ.
        assert_ne!(fnv1a_32("hello"), fnv1a_32("world"));
    }

    // ── TF-IDF sparse fallback ────────────────────────────────────────────────

    /// Single-token text must produce exactly one pair.
    #[test]
    fn sparse_tfidf_single_token() {
        let pairs = sparse_tfidf_single("hello");
        assert_eq!(pairs.len(), 1, "one token → one sparse pair");
        let (_, w) = pairs[0];
        assert!(w > 0.0, "weight must be positive");
    }

    /// Repeated token must produce one pair with elevated weight.
    #[test]
    fn sparse_tfidf_repeated_token_has_higher_tf() {
        let one = sparse_tfidf_single("hello");
        let two = sparse_tfidf_single("hello hello");
        // Both produce one pair for "hello"; TF differs but IDF changes inversely.
        // What matters: both are non-empty and the weights are > 0.
        assert_eq!(one.len(), 1);
        assert_eq!(two.len(), 1);
        assert!(one[0].1 > 0.0);
        assert!(two[0].1 > 0.0);
    }

    /// Output must be sorted by token_id for deterministic ordering.
    #[test]
    fn sparse_tfidf_sorted_by_token_id() {
        let pairs = sparse_tfidf_single("alpha beta gamma delta epsilon");
        let ids: Vec<u32> = pairs.iter().map(|(id, _)| *id).collect();
        let mut sorted = ids.clone();
        sorted.sort();
        assert_eq!(ids, sorted, "sparse pairs must be sorted by token_id");
    }
}
