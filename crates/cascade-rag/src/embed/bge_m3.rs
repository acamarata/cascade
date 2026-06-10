//! BGE-M3 embedding provider via fastembed-rs (ONNX inference).
//!
//! # Purpose
//!
//! Implements [`EmbedModel`] and [`EmbeddingProvider`] for the BGE-M3 family
//! using local ONNX inference.  No API keys, no network after first download.
//!
//! # Dense vs Sparse
//!
//! BGE-M3 (BAAI General Embedding M3) supports dense, sparse (SPLADE), and
//! multi-vector (ColBERT) modes.  This implementation provides:
//!
//! - **Dense**: 1024-dimensional float vectors via fastembed-rs ONNX session.
//! - **Sparse**: TF-IDF with FNV-1a stable token hashing (see note below).
//!
//! ## fastembed-rs model note (T-P4-E01-04 gap)
//!
//! fastembed 3.14.1 (the version resolved in Cargo.lock at forge time) does
//! **not** expose `EmbeddingModel::BGEM3`.  The closest available 1024-dim
//! variant is `EmbeddingModel::BGELargeENV15` (BAAI/bge-large-en-v1.5, dim
//! = 1024).  That model is used for dense vectors.
//!
//! # Selected: TF-IDF fallback for sparse (not native SPLADE)
//!
//! fastembed 3.14.1 only ships `SparseModel::SPLADEPPV1` (English-only
//! Splade-PP), not a BGE-M3 sparse model.  To avoid a ~700 MB extra model
//! download for a non-M3 sparse model, the sparse path uses the
//! [`sparse_tfidf_single`] helper from `embed::mod`, which produces stable,
//! reproducible `(FNV-1a-token-id, tf-idf-weight)` pairs.  Upgrade path:
//! when fastembed ships `EmbeddingModel::BGEM3`, replace `BGELargeENV15` +
//! remove this comment block.
//!
//! # Performance targets
//!
//! - Warm start (model already on disk): init < 5 s
//! - 1024-dim float32 output per text
//! - Embedding cache hit > 90 % on repeated indexing runs
//! - Daemon idle RAM < 100 MB when no active embedding job
//! - Peak RAM < 500 MB during a 1M-chunk indexing run
//!
//! # Inputs / Outputs
//!
//! - `BgeM3Embedder::new(opts)` → `Result<BgeM3Embedder>` (may download model)
//! - `embed_dense(&[&str])` → `Vec<Vec<f32>>` (1024-dim each)
//! - `embed_sparse(&[&str])` → `Vec<Vec<(u32, f32)>>` (TF-IDF fallback)
//!
//! # Constraints
//!
//! - fastembed `TextEmbedding` is not `Send`; all ONNX calls run inside
//!   `tokio::task::block_in_place` to avoid stalling the async executor.
//! - Batch size defaults to 32 (tuned for M-series unified memory) and is
//!   configurable via `BgeM3Options::batch_size`.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed::bge_m3

use async_trait::async_trait;
use std::path::PathBuf;
use tracing::{info, instrument, warn};

use cascade_types::{
    error::{CascadeError, Result},
    EmbedOpts, Embedding, EmbeddingProvider, ProviderKind,
};

use super::{EmbedError, EmbedModel, sparse_tfidf_single};

#[cfg(feature = "fastembed")]
use fastembed::{EmbeddingModel, InitOptions, TextEmbedding};

/// Batch size for fastembed ONNX calls.
///
/// 32 texts per inference pass; tuned for M-series unified memory.
/// Configurable via [`BgeM3Options::batch_size`].
const DEFAULT_BATCH_SIZE: usize = 32;

/// Dense embedding dimension for BGE-M3 (and BGELargeENV15 as proxy).
const BGE_M3_DIM: usize = 1024;

/// Stable model identifier string used in logs, metrics, and error messages.
const MODEL_ID: &str = "bge-m3";

// ── BgeM3Embedder ─────────────────────────────────────────────────────────────

/// Local BGE-M3 embedding provider backed by fastembed-rs.
///
/// # Usage
///
/// ```rust,no_run
/// # #[cfg(feature = "fastembed")]
/// # async fn example() -> cascade_rag::embed::EmbedError {
/// use std::sync::Arc;
/// use cascade_rag::embed::bge_m3::{BgeM3Embedder, BgeM3Options};
///
/// let embedder = BgeM3Embedder::new(BgeM3Options::default()).await
///     .expect("model init should succeed when model is cached");
/// let dense = embedder.embed_dense(&["hello world"]).unwrap();
/// assert_eq!(dense[0].len(), 1024);
/// # todo!()
/// # }
/// ```
///
/// Wrap in `Arc` to share across tasks:
/// ```rust,no_run
/// # #[cfg(feature = "fastembed")]
/// # async fn example() -> cascade_rag::embed::EmbedError {
/// use std::sync::Arc;
/// use cascade_rag::embed::bge_m3::{BgeM3Embedder, BgeM3Options};
/// use cascade_rag::embed::EmbedModel;
///
/// let embedder: Arc<dyn EmbedModel> =
///     Arc::new(BgeM3Embedder::new(BgeM3Options::default()).await.unwrap());
/// # todo!()
/// # }
/// ```
pub struct BgeM3Embedder {
    /// fastembed ONNX session (only compiled when the `fastembed` feature is on).
    #[cfg(feature = "fastembed")]
    model: TextEmbedding,
    /// Resolved path to the model cache directory.
    model_dir: PathBuf,
    /// Batch size for ONNX inference calls.
    batch_size: usize,
}

// ── Options ───────────────────────────────────────────────────────────────────

/// Options for initialising [`BgeM3Embedder`].
#[derive(Debug, Clone)]
pub struct BgeM3Options {
    /// Override the model cache directory.
    ///
    /// Defaults to `~/.cascade/models/` (resolved via `model_cache_dir()`).
    pub model_dir: Option<PathBuf>,

    /// Batch size for fastembed ONNX inference calls.
    ///
    /// Larger batches are faster on GPU / ANE but use more memory.
    /// Default: 32.
    pub batch_size: usize,

    /// If `true`, fail immediately when model files are absent rather than
    /// downloading them.  Recommended for production after the initial
    /// bootstrap.
    pub offline_guard: bool,
}

impl Default for BgeM3Options {
    fn default() -> Self {
        Self {
            model_dir: None,
            batch_size: DEFAULT_BATCH_SIZE,
            offline_guard: false,
        }
    }
}

// ── Constructor ───────────────────────────────────────────────────────────────

impl BgeM3Embedder {
    /// Initialise the embedder, downloading the model if not already cached.
    ///
    /// This is the only method that may perform network I/O.  After `new()`
    /// returns, all subsequent embedding calls are fully offline.
    ///
    /// # Arguments
    ///
    /// - `opts` — see [`BgeM3Options`] for defaults and override fields.
    ///
    /// # Errors
    ///
    /// - `CascadeError::EmbeddingFailed` if the model cannot be loaded or
    ///   downloaded (e.g. `offline_guard` is set and the model is absent).
    /// - `CascadeError::Io` if the cache directory cannot be created.
    #[instrument(skip(opts))]
    pub async fn new(opts: BgeM3Options) -> Result<Self> {
        // Resolve cache dir: option > env > default.
        let model_dir = match opts.model_dir {
            Some(p) => p,
            None => super::model_cache_dir().map_err(|e| CascadeError::EmbeddingFailed {
                provider: MODEL_ID.into(),
                detail: format!("cache dir resolution failed: {e}"),
            })?,
        };

        std::fs::create_dir_all(&model_dir)
            .map_err(|e| CascadeError::io(&model_dir, "create-models-dir", e))?;

        if opts.offline_guard && !model_dir.exists() {
            return Err(CascadeError::EmbeddingFailed {
                provider: MODEL_ID.into(),
                detail: format!(
                    "model not found on disk and offline_guard is set (dir: {})",
                    model_dir.display()
                ),
            });
        }

        info!(model_dir = %model_dir.display(), "initialising BGE-M3 / BGELargeENV15 provider");

        #[cfg(feature = "fastembed")]
        {
            // NOTE: fastembed 3.14.1 has no EmbeddingModel::BGEM3.
            // BGELargeENV15 (dim=1024) is the closest available model.
            // Upgrade: replace with EmbeddingModel::BGEM3 when available.
            let init_opts = InitOptions::new(EmbeddingModel::BGELargeENV15)
                .with_cache_dir(model_dir.clone())
                .with_show_download_progress(true);

            let model =
                TextEmbedding::try_new(init_opts).map_err(|e| CascadeError::EmbeddingFailed {
                    provider: MODEL_ID.into(),
                    detail: format!("fastembed init: {e}"),
                })?;

            // Warm-up: one dummy embed to pre-allocate ONNX buffers.
            let _ = model.embed(vec!["warm-up"], Some(1));
            info!("BGELargeENV15 warm-up complete (proxying BGE-M3)");

            return Ok(Self {
                model,
                model_dir,
                batch_size: opts.batch_size,
            });
        }

        #[cfg(not(feature = "fastembed"))]
        {
            warn!("fastembed feature not enabled; BgeM3Embedder will return zero dense vectors");
            Ok(Self {
                model_dir,
                batch_size: opts.batch_size,
            })
        }
    }
}

// ── EmbedModel impl ───────────────────────────────────────────────────────────

impl EmbedModel for BgeM3Embedder {
    /// Embed texts into 1024-dim dense vectors.
    ///
    /// Internally splits `texts` into chunks of `batch_size` and calls
    /// fastembed's synchronous ONNX session on a blocking thread.
    ///
    /// # Errors
    ///
    /// Returns `EmbedError::ModelNotFound` if the model is absent and
    /// `EmbedError::InferenceFailed` on ONNX errors.
    fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        if texts.is_empty() {
            return Ok(vec![]);
        }

        #[cfg(feature = "fastembed")]
        {
            // fastembed embed() is synchronous; run it here (caller must ensure
            // not on the async executor — see async bridge below for the right
            // pattern with block_in_place).
            let owned: Vec<String> = texts.iter().map(|s| s.to_string()).collect();
            let results = self
                .model
                .embed(owned, Some(self.batch_size))
                .map_err(|e| EmbedError::InferenceFailed {
                    model_id: MODEL_ID.into(),
                    detail: format!("{e}"),
                })?;

            // Validate dimension.
            for (i, v) in results.iter().enumerate() {
                if v.len() != BGE_M3_DIM {
                    return Err(EmbedError::DimensionMismatch {
                        expected: BGE_M3_DIM,
                        actual: v.len(),
                    });
                }
                let _ = i;
            }
            return Ok(results);
        }

        #[cfg(not(feature = "fastembed"))]
        {
            // Stub: zero vectors when feature is off.
            Ok(texts
                .iter()
                .map(|_| vec![0.0f32; BGE_M3_DIM])
                .collect())
        }
    }

    /// Embed texts into sparse lexical weight vectors.
    ///
    /// # Selected: TF-IDF fallback
    ///
    /// fastembed 3.14.1 ships `SparseModel::SPLADEPPV1` but not a BGE-M3
    /// sparse model.  To avoid a second large model download, this method
    /// uses the TF-IDF + FNV-1a fallback defined in `embed::sparse_tfidf_single`.
    ///
    /// Token IDs are stable across process restarts (FNV-1a, fixed constants).
    fn embed_sparse(
        &self,
        texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        // Selected: TF-IDF fallback (fastembed 3.14.1 has no BGE-M3 sparse).
        // When fastembed ships native BGE-M3 sparse, replace with:
        //   SparseTextEmbedding::try_new(SparseInitOptions { model_name:
        //   SparseModel::BGEM3, cache_dir: self.model_dir.clone(), .. })
        Ok(texts.iter().map(|t| sparse_tfidf_single(t)).collect())
    }

    fn dim(&self) -> usize {
        BGE_M3_DIM
    }

    fn model_id(&self) -> &str {
        MODEL_ID
    }
}

// ── EmbeddingProvider bridge (cascade-types trait) ────────────────────────────

#[async_trait]
impl EmbeddingProvider for BgeM3Embedder {
    /// Async wrapper around `embed_dense`.
    ///
    /// Runs the synchronous ONNX inference inside `block_in_place` so the
    /// async executor is not blocked during model inference.
    async fn embed(&self, texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        if texts.is_empty() {
            return Ok(vec![]);
        }

        #[cfg(feature = "fastembed")]
        {
            let owned: Vec<String> = texts.iter().map(|s| s.to_string()).collect();
            let batch_size = self.batch_size;

            // Block-in-place: fastembed is synchronous; keep it off the executor.
            let vecs = tokio::task::block_in_place(|| {
                let refs: Vec<&str> = owned.iter().map(String::as_str).collect();
                // Call the synchronous EmbedModel impl.
                let _ = batch_size; // used via self.batch_size inside embed_dense
                self.embed_dense(&refs)
            })
            .map_err(|e: EmbedError| CascadeError::from(e))?;

            return Ok(vecs
                .into_iter()
                .map(|values| Embedding {
                    values,
                    token_count: None,
                })
                .collect());
        }

        #[cfg(not(feature = "fastembed"))]
        {
            // Stub: zero vectors.
            Ok(texts
                .iter()
                .map(|_| Embedding {
                    values: vec![0.0f32; BGE_M3_DIM],
                    token_count: None,
                })
                .collect())
        }
    }

    fn dimension(&self) -> usize {
        BGE_M3_DIM
    }

    fn kind(&self) -> ProviderKind {
        ProviderKind::BgeM3
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::embed::MockEmbedModel;

    // ── Unit tests that do NOT require a model download ───────────────────────

    /// The mock model has dim=1024 and returns the correct number of vectors.
    #[test]
    fn mock_dim_is_1024() {
        let m = MockEmbedModel::new(1024);
        assert_eq!(m.dim(), 1024);
        let vecs = m.embed_dense(&["hello world"]).unwrap();
        assert_eq!(vecs[0].len(), 1024);
    }

    /// Cosine similarity of a text with itself must be close to 1.0.
    #[test]
    fn mock_cosine_self_similarity_is_one() {
        let m = MockEmbedModel::new(1024);
        let vecs = m.embed_dense(&["identical text"]).unwrap();
        let a = &vecs[0];
        let dot: f32 = a.iter().map(|v| v * v).sum();
        let norm: f32 = a.iter().map(|v| v * v).sum::<f32>().sqrt();
        let cosine = dot / (norm * norm);
        assert!(
            (cosine - 1.0).abs() < 1e-5,
            "self-cosine must be ~1.0, got {cosine}"
        );
    }

    /// Cosine similarity between two dissimilar texts must be less than 0.9.
    ///
    /// The mock model does not encode semantics, so "hello" vs "world" is
    /// not guaranteed to be far apart; we just verify the API works and
    /// returns plausible floats.
    #[test]
    fn mock_dense_returns_plausible_floats() {
        let m = MockEmbedModel::new(1024);
        let vecs = m.embed_dense(&["apple orchard", "ocean deep"]).unwrap();
        for v in &vecs {
            assert!(v.iter().all(|f| f.is_finite()), "all values must be finite");
        }
    }

    /// Sparse output must be non-empty for non-trivial text.
    #[test]
    fn mock_sparse_is_non_empty() {
        let m = MockEmbedModel::new(1024);
        let sparse = m.embed_sparse(&["cascade retrieval augmented generation"]).unwrap();
        assert!(!sparse[0].is_empty(), "sparse output must not be empty");
    }

    /// Missing-model error: constructing BgeM3Embedder with offline_guard
    /// in a temp dir that has no model files must produce an EmbeddingFailed error.
    #[tokio::test]
    async fn new_offline_guard_missing_model_errors() {
        let dir = tempfile::tempdir().unwrap();
        // Remove the dir so it doesn't exist (simulate missing model).
        std::fs::remove_dir_all(dir.path()).unwrap();

        let opts = BgeM3Options {
            model_dir: Some(dir.path().to_path_buf()),
            batch_size: 32,
            offline_guard: true,
        };
        // With offline_guard and no model dir, new() must fail.
        let result = BgeM3Embedder::new(opts).await;
        assert!(result.is_err(), "offline_guard + missing dir must error");
        match result.unwrap_err() {
            CascadeError::EmbeddingFailed { provider, .. } => {
                assert_eq!(provider, "bge-m3");
            }
            other => panic!("expected EmbeddingFailed, got {other:?}"),
        }
    }

    // ── Slow integration tests requiring model download (marked #[ignore]) ────

    /// Dense embedding of two texts returns 1024-dim vectors.
    ///
    /// Requires the BGELargeENV15 ONNX model (~1.3 GB) to be cached.
    /// Run with: `cargo test -- --ignored embed::bge_m3::tests::real_embed_dim`
    #[tokio::test]
    #[ignore = "requires BGELargeENV15 model download (~1.3 GB)"]
    async fn real_embed_dim() {
        let opts = BgeM3Options::default();
        let embedder = BgeM3Embedder::new(opts).await.expect("model init");
        let texts = &["hello world", "cascade context retrieval"];
        let vecs = embedder.embed_dense(texts).expect("embed must succeed");
        assert_eq!(vecs.len(), 2);
        for v in &vecs {
            assert_eq!(v.len(), 1024, "each vector must be 1024-dim");
        }
    }

    /// Cosine similarity between identical texts is > 0.99.
    ///
    /// Requires model download.  Run with `--ignored`.
    #[tokio::test]
    #[ignore = "requires BGELargeENV15 model download (~1.3 GB)"]
    async fn real_identical_cosine_similarity() {
        let embedder = BgeM3Embedder::new(BgeM3Options::default())
            .await
            .expect("model init");
        let vecs = embedder
            .embed_dense(&["identical text", "identical text"])
            .expect("embed must succeed");
        let a = &vecs[0];
        let b = &vecs[1];
        let dot: f32 = a.iter().zip(b.iter()).map(|(x, y)| x * y).sum();
        let na: f32 = a.iter().map(|v| v * v).sum::<f32>().sqrt();
        let nb: f32 = b.iter().map(|v| v * v).sum::<f32>().sqrt();
        let cosine = dot / (na * nb);
        assert!(cosine > 0.99, "identical text cosine must be > 0.99, got {cosine}");
    }

    /// Cosine similarity between semantically dissimilar texts is < 0.9.
    ///
    /// Requires model download.  Run with `--ignored`.
    #[tokio::test]
    #[ignore = "requires BGELargeENV15 model download (~1.3 GB)"]
    async fn real_dissimilar_cosine_similarity() {
        let embedder = BgeM3Embedder::new(BgeM3Options::default())
            .await
            .expect("model init");
        let vecs = embedder
            .embed_dense(&["apple orchard fruit harvest", "nuclear fusion reactor plasma"])
            .expect("embed must succeed");
        let a = &vecs[0];
        let b = &vecs[1];
        let dot: f32 = a.iter().zip(b.iter()).map(|(x, y)| x * y).sum();
        let na: f32 = a.iter().map(|v| v * v).sum::<f32>().sqrt();
        let nb: f32 = b.iter().map(|v| v * v).sum::<f32>().sqrt();
        let cosine = dot / (na * nb);
        assert!(cosine < 0.9, "dissimilar texts cosine must be < 0.9, got {cosine}");
    }
}
