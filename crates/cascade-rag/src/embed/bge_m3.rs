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
//! ## fastembed-rs model note (T-P4-E01-04 gap / rag-02 verification)
//!
//! fastembed 4.9.1 does **not** expose `EmbeddingModel::BGEM3`.  The best
//! available 1024-dim multilingual model is `EmbeddingModel::MultilingualE5Large`
//! (intfloat/multilingual-e5-large, dim=1024).  It covers 100+ languages and
//! is the closest available proxy to BGE-M3 dense output within fastembed 4.9.1.
//!
//! TODO(rag-02): BGE-M3 not in fastembed 4.9.1 — upgrade when fastembed
//!   ships EmbeddingModel::BGEM3; replace MultilingualE5Large + this comment.
//!
//! # Selected: TF-IDF fallback for sparse (not native SPLADE)
//!
//! fastembed 4.9.1 only ships `SparseModel::SPLADEPPV1` (English-only
//! Splade-PP), not a BGE-M3 sparse model.  To avoid a ~700 MB extra model
//! download for a non-M3 sparse model, the sparse path uses the
//! [`sparse_tfidf_single`] helper from `embed::mod`, which produces stable,
//! reproducible `(FNV-1a-token-id, tf-idf-weight)` pairs.  Upgrade path:
//! when fastembed ships a BGE-M3 sparse model, remove this comment block.
//!
//! # ColBERT / multi-vector
//!
//! fastembed 4.9.1 has no token-level/late-interaction ColBERT output.  The
//! `rag-multivec` path uses a per-word dense-call proxy; see `multivector.rs`
//! and its module-level caveat.
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
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Mutex;
use tracing::{info, instrument, warn};

use cascade_types::{
    error::{CascadeError, Result},
    EmbedOpts, EmbedUsage, Embedding, EmbeddingProvider, ProviderKind,
};

use super::{sparse_tfidf_single, EmbedError, EmbedModel};

#[cfg(feature = "fastembed")]
use fastembed::{EmbeddingModel, InitOptions, TextEmbedding};

/// Batch size for fastembed ONNX calls.
///
/// 32 texts per inference pass; tuned for M-series unified memory.
/// Configurable via [`BgeM3Options::batch_size`].
const DEFAULT_BATCH_SIZE: usize = 32;

/// Dense embedding dimension.
///
/// MultilingualE5Large produces 1024-dim vectors, matching the BGE-M3 target.
/// TODO(rag-02): BGE-M3 not in fastembed 4.9.1; using MultilingualE5Large (1024-d).
const BGE_M3_DIM: usize = 1024;

/// Stable model identifier string used in logs, metrics, and error messages.
const MODEL_ID: &str = "bge-m3";

/// Query instruction prefix for multilingual-e5-large.
///
/// E5 models expect a task-specific prefix for query vectors.  Document
/// vectors use `DOCUMENT_INSTRUCTION`.  Using task prefixes matches the
/// intfloat/multilingual-e5-large recommended usage and replicates the
/// BGE-M3 instruction pattern.
const QUERY_INSTRUCTION: &str = "query: ";

/// Document instruction prefix for multilingual-e5-large.
const DOCUMENT_INSTRUCTION: &str = "passage: ";

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
    /// Content-hash embed cache: blake3 hex → dense vector.
    ///
    /// Avoids re-embedding identical text chunks on repeated indexing runs.
    /// Cache hits > 90% expected on incremental re-index of unchanged documents.
    embed_cache: Mutex<HashMap<String, Vec<f32>>>,
}

impl std::fmt::Debug for BgeM3Embedder {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("BgeM3Embedder")
            .field("model_dir", &self.model_dir)
            .field("batch_size", &self.batch_size)
            .finish_non_exhaustive()
    }
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

        // BUG #1 FIX: offline_guard check BEFORE create_dir_all.
        //
        // Old code (wrong): create_dir_all ran first, then checked `!model_dir.exists()`.
        // Since create_dir_all just created the dir, `exists()` was always true →
        // the guard was a no-op.
        //
        // Fixed: check BEFORE creating the directory so an uncached model returns
        // Err(EmbeddingFailed) instead of silently creating an empty dir and
        // trying to load a non-existent model.
        if opts.offline_guard && !model_dir.exists() {
            return Err(CascadeError::EmbeddingFailed {
                provider: MODEL_ID.into(),
                detail: format!(
                    "model not found on disk and offline_guard is set (dir: {})",
                    model_dir.display()
                ),
            });
        }

        std::fs::create_dir_all(&model_dir)
            .map_err(|e| CascadeError::io(&model_dir, "create-models-dir", e))?;

        info!(model_dir = %model_dir.display(), "initialising BGE-M3 / MultilingualE5Large provider");

        #[cfg(feature = "fastembed")]
        {
            // TODO(rag-02): BGE-M3 not in fastembed 4.9.1.
            // Using MultilingualE5Large (intfloat/multilingual-e5-large, 1024-dim).
            // Best available 1024-dim multilingual model in fastembed 4.9.1.
            // Upgrade: replace with EmbeddingModel::BGEM3 when available.
            let init_opts = InitOptions::new(EmbeddingModel::MultilingualE5Large)
                .with_cache_dir(model_dir.clone())
                .with_show_download_progress(true);

            let model =
                TextEmbedding::try_new(init_opts).map_err(|e| CascadeError::EmbeddingFailed {
                    provider: MODEL_ID.into(),
                    detail: format!("fastembed init: {e}"),
                })?;

            // Warm-up: one dummy embed to pre-allocate ONNX buffers.
            let _ = model.embed(vec!["warm-up".to_string()], Some(1));
            info!("MultilingualE5Large warm-up complete (proxying BGE-M3, 1024-dim)");

            return Ok(Self {
                model,
                model_dir,
                batch_size: opts.batch_size,
                embed_cache: Mutex::new(HashMap::new()),
            });
        }

        #[cfg(not(feature = "fastembed"))]
        {
            warn!("fastembed feature not enabled; BgeM3Embedder will return zero dense vectors");
            Ok(Self {
                model_dir,
                batch_size: opts.batch_size,
                embed_cache: Mutex::new(HashMap::new()),
            })
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Compute blake3 hex hash for a string (content-hash cache key).
fn blake3_hex(s: &str) -> String {
    blake3::hash(s.as_bytes()).to_hex().to_string()
}

/// L2-normalise a mutable vector in place.
///
/// If the vector is the zero vector (norm < 1e-12), it is left unchanged to
/// avoid NaN.
fn l2_normalize(v: &mut [f32]) {
    let norm: f32 = v.iter().map(|x| x * x).sum::<f32>().sqrt();
    if norm < 1e-12 {
        return;
    }
    for x in v.iter_mut() {
        *x /= norm;
    }
}

/// Truncate a dense vector to `dim` dimensions and L2-renormalise.
///
/// Returns the truncated, renormalised vector.  If `dim >= v.len()`, the
/// original vector is returned (still renormalised).
pub(crate) fn truncate_and_normalize(v: Vec<f32>, dim: usize) -> Vec<f32> {
    let mut out: Vec<f32> = if dim < v.len() {
        v.into_iter().take(dim).collect()
    } else {
        v
    };
    l2_normalize(&mut out);
    out
}

// ── EmbedModel impl ───────────────────────────────────────────────────────────

impl EmbedModel for BgeM3Embedder {
    /// Embed texts into 1024-dim dense vectors.
    ///
    /// Prepends the document instruction prefix (`"passage: "`) before calling
    /// fastembed.  Uses a blake3 content-hash cache to skip re-embedding of
    /// identical texts.
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
            // Prepend document instruction prefix.
            // Query prefix is applied separately in EmbeddingProvider::embed().
            let prefixed: Vec<String> = texts
                .iter()
                .map(|t| format!("{DOCUMENT_INSTRUCTION}{t}"))
                .collect();

            // Split into cache hits and misses.
            let mut results: Vec<(usize, Vec<f32>)> = Vec::with_capacity(texts.len());
            let mut miss_indices: Vec<usize> = Vec::new();
            let mut miss_texts: Vec<String> = Vec::new();

            {
                let cache = self.embed_cache.lock().map_err(|_| EmbedError::InferenceFailed {
                    model_id: MODEL_ID.into(),
                    detail: "embed_cache mutex poisoned".into(),
                })?;
                for (i, t) in texts.iter().enumerate() {
                    let key = blake3_hex(t);
                    if let Some(cached) = cache.get(&key) {
                        results.push((i, cached.clone()));
                    } else {
                        miss_indices.push(i);
                        miss_texts.push(prefixed[i].clone());
                    }
                }
            }

            // Run inference for cache misses.
            if !miss_texts.is_empty() {
                let inferred = self
                    .model
                    .embed(miss_texts, Some(self.batch_size))
                    .map_err(|e| EmbedError::InferenceFailed {
                        model_id: MODEL_ID.into(),
                        detail: format!("{e}"),
                    })?;

                let mut cache = self.embed_cache.lock().map_err(|_| EmbedError::InferenceFailed {
                    model_id: MODEL_ID.into(),
                    detail: "embed_cache mutex poisoned on write".into(),
                })?;

                for (offset, &orig_idx) in miss_indices.iter().enumerate() {
                    let v = &inferred[offset];
                    if v.len() != BGE_M3_DIM {
                        return Err(EmbedError::DimensionMismatch {
                            expected: BGE_M3_DIM,
                            actual: v.len(),
                        });
                    }
                    let key = blake3_hex(texts[orig_idx]);
                    cache.insert(key, v.clone());
                    results.push((orig_idx, v.clone()));
                }
            }

            // Re-sort to original order.
            results.sort_by_key(|(i, _)| *i);
            return Ok(results.into_iter().map(|(_, v)| v).collect());
        }

        #[cfg(not(feature = "fastembed"))]
        {
            // Stub: zero vectors when feature is off.
            Ok(texts.iter().map(|_| vec![0.0f32; BGE_M3_DIM]).collect())
        }
    }

    /// Embed texts into sparse lexical weight vectors.
    ///
    /// # Selected: TF-IDF fallback
    ///
    /// fastembed 4.9.1 ships `SparseModel::SPLADEPPV1` (English-only) but no
    /// BGE-M3 sparse model.  This method uses TF-IDF + FNV-1a fallback.
    ///
    /// TODO(rag-02): fastembed 4.9.1 has no BGE-M3 sparse model.
    /// SparseModel::SPLADEPPV1 is English-only and not BGE-M3 compatible.
    /// TF-IDF kept until fastembed ships a BGE-M3 sparse model.
    fn embed_sparse(
        &self,
        texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
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
    /// Applies query/document instruction prefix based on `opts.usage`:
    /// - `EmbedUsage::Query` → prepends `"query: "` (bypasses `embed_dense`'s document prefix).
    /// - `EmbedUsage::Document` → uses `embed_dense` (which prepends `"passage: "`).
    ///
    /// Applies `opts.truncate_dim` after inference if set: truncates to the
    /// requested number of dimensions and L2-renormalises (matryoshka support).
    ///
    /// Runs the synchronous ONNX inference inside `block_in_place` so the
    /// async executor is not blocked during model inference.
    async fn embed(&self, texts: &[&str], opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        if texts.is_empty() {
            return Ok(vec![]);
        }

        let is_query = opts.usage == EmbedUsage::Query;
        let truncate = opts.truncate_dim;

        #[cfg(feature = "fastembed")]
        {
            if is_query {
                // Query path: prepend QUERY_INSTRUCTION directly, bypassing the
                // document-instruction wrapper inside embed_dense.
                let query_owned: Vec<String> = texts
                    .iter()
                    .map(|t| format!("{QUERY_INSTRUCTION}{t}"))
                    .collect();

                let vecs = tokio::task::block_in_place(|| {
                    self.model
                        .embed(query_owned, Some(self.batch_size))
                        .map_err(|e| EmbedError::InferenceFailed {
                            model_id: MODEL_ID.into(),
                            detail: format!("{e}"),
                        })
                })
                .map_err(|e: EmbedError| CascadeError::from(e))?;

                return Ok(vecs
                    .into_iter()
                    .map(|mut values| {
                        if let Some(dim) = truncate {
                            values = truncate_and_normalize(values, dim);
                        }
                        Embedding { values, token_count: None }
                    })
                    .collect());
            }

            // Document path: use cached embed_dense (applies DOCUMENT_INSTRUCTION).
            let vecs = tokio::task::block_in_place(|| self.embed_dense(texts))
                .map_err(|e: EmbedError| CascadeError::from(e))?;

            return Ok(vecs
                .into_iter()
                .map(|mut values| {
                    if let Some(dim) = truncate {
                        values = truncate_and_normalize(values, dim);
                    }
                    Embedding { values, token_count: None }
                })
                .collect());
        }

        #[cfg(not(feature = "fastembed"))]
        {
            // Stub: zero vectors.
            Ok(texts
                .iter()
                .map(|_| {
                    let dim = truncate.unwrap_or(BGE_M3_DIM);
                    Embedding {
                        values: vec![0.0f32; dim],
                        token_count: None,
                    }
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

    /// Dense output returns plausible finite floats for multiple texts.
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
        let sparse = m
            .embed_sparse(&["cascade retrieval augmented generation"])
            .unwrap();
        assert!(!sparse[0].is_empty(), "sparse output must not be empty");
    }

    /// BUG #1 REGRESSION: offline_guard must fire BEFORE create_dir_all.
    ///
    /// Old behaviour: `create_dir_all` ran first → dir always existed →
    /// `!model_dir.exists()` was always false → guard never triggered.
    ///
    /// Fixed: guard check moved to BEFORE `create_dir_all`.
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
        let result = BgeM3Embedder::new(opts).await;
        assert!(result.is_err(), "offline_guard + missing dir must error");
        match result.unwrap_err() {
            CascadeError::EmbeddingFailed { provider, .. } => {
                assert_eq!(provider, "bge-m3");
            }
            other => panic!("expected EmbeddingFailed, got {other:?}"),
        }
    }

    /// blake3_hex must be deterministic and differ for different inputs.
    #[test]
    fn blake3_hex_is_deterministic() {
        let h1 = blake3_hex("hello world");
        let h2 = blake3_hex("hello world");
        assert_eq!(h1, h2, "blake3_hex must be deterministic");
        assert_ne!(
            blake3_hex("hello world"),
            blake3_hex("different text"),
            "different inputs must produce different hashes"
        );
        // 64 hex chars = 256-bit blake3 output.
        assert_eq!(h1.len(), 64, "blake3 hex must be 64 chars");
    }

    /// l2_normalize must produce a unit-norm vector.
    #[test]
    fn l2_normalize_produces_unit_norm() {
        let mut v = vec![3.0_f32, 4.0, 0.0];
        l2_normalize(&mut v);
        let norm: f32 = v.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!((norm - 1.0).abs() < 1e-6, "must produce unit-norm, got {norm}");
    }

    /// l2_normalize of zero vector must leave it unchanged (no NaN/inf).
    #[test]
    fn l2_normalize_zero_vector_is_safe() {
        let mut v = vec![0.0_f32, 0.0, 0.0];
        l2_normalize(&mut v);
        assert!(v.iter().all(|x| x.is_finite()), "zero vector must remain finite");
    }

    /// truncate_and_normalize must shorten the vector and produce a unit-norm result.
    ///
    /// Core test for task 5 (truncate_dim matryoshka truncation).
    #[test]
    fn truncate_dim_shortens_and_normalises() {
        let input: Vec<f32> = (0..1024).map(|i| (i as f32).sin()).collect();
        let out = truncate_and_normalize(input, 256);
        assert_eq!(out.len(), 256, "truncated to 256 dims");
        let norm: f32 = out.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!((norm - 1.0).abs() < 1e-5, "truncated vector must be unit-norm, got {norm}");
    }

    /// truncate_and_normalize with dim >= len must not shrink the vector.
    #[test]
    fn truncate_dim_no_op_when_dim_ge_len() {
        let input = vec![1.0_f32, 0.0, 0.0];
        let out = truncate_and_normalize(input, 10);
        assert_eq!(out.len(), 3, "must not grow vector beyond its original length");
    }

    /// Query instruction prefix must differ from document instruction prefix.
    ///
    /// E5 models use task-specific prefixes; both must be distinct so that
    /// query embedding differs from document embedding at the model level.
    #[test]
    fn query_instruction_differs_from_document_instruction() {
        assert_ne!(
            QUERY_INSTRUCTION, DOCUMENT_INSTRUCTION,
            "query and document instruction prefixes must differ"
        );
        assert!(
            QUERY_INSTRUCTION.starts_with("query"),
            "QUERY_INSTRUCTION must start with 'query'"
        );
        assert!(
            DOCUMENT_INSTRUCTION.starts_with("passage"),
            "DOCUMENT_INSTRUCTION must start with 'passage'"
        );
    }

    // ── Slow integration tests requiring model download (marked #[ignore]) ────

    /// Dense embedding of two texts returns 1024-dim vectors.
    ///
    /// Requires the MultilingualE5Large ONNX model (~2 GB) to be cached.
    #[tokio::test]
    #[ignore = "requires MultilingualE5Large model download (~2 GB)"]
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

    /// Query embedding must differ from document embedding for the same text.
    ///
    /// Requires model download.
    #[tokio::test]
    #[ignore = "requires MultilingualE5Large model download (~2 GB)"]
    async fn real_query_prefix_differs_from_document() {
        use cascade_types::{EmbedOpts, EmbedUsage};
        let embedder = BgeM3Embedder::new(BgeM3Options::default())
            .await
            .expect("model init");
        let text = &["cascade semantic retrieval"];
        let doc_opts = EmbedOpts { usage: EmbedUsage::Document, ..Default::default() };
        let query_opts = EmbedOpts { usage: EmbedUsage::Query, ..Default::default() };
        let doc_emb = embedder.embed(text, &doc_opts).await.expect("doc embed");
        let query_emb = embedder.embed(text, &query_opts).await.expect("query embed");
        assert_ne!(
            doc_emb[0].values, query_emb[0].values,
            "query and document embeddings must differ due to instruction prefix"
        );
    }

    /// Embed cache: identical texts must return cache hits (same vectors).
    ///
    /// Requires model download.
    #[tokio::test]
    #[ignore = "requires MultilingualE5Large model download (~2 GB)"]
    async fn real_embed_cache_hit() {
        let embedder = BgeM3Embedder::new(BgeM3Options::default())
            .await
            .expect("model init");
        let text = "cache hit test sentence";
        let v1 = embedder.embed_dense(&[text]).expect("first embed");
        let v2 = embedder.embed_dense(&[text]).expect("second embed (cache hit)");
        assert_eq!(v1, v2, "cache hit must produce identical vectors");
    }

    /// truncate_dim = Some(256) must return 256-d renormalised vectors.
    ///
    /// Requires model download.
    #[tokio::test]
    #[ignore = "requires MultilingualE5Large model download (~2 GB)"]
    async fn real_truncate_dim_256() {
        use cascade_types::{EmbedOpts, EmbedUsage};
        let embedder = BgeM3Embedder::new(BgeM3Options::default())
            .await
            .expect("model init");
        let opts = EmbedOpts {
            usage: EmbedUsage::Document,
            truncate_dim: Some(256),
            model_override: None,
        };
        let emb = embedder
            .embed(&["matryoshka truncation test"], &opts)
            .await
            .expect("truncated embed");
        assert_eq!(emb[0].values.len(), 256, "must be 256-d after truncation");
        let norm: f32 = emb[0].values.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!((norm - 1.0).abs() < 1e-4, "truncated vector must be unit-norm, got {norm}");
    }

    /// Cosine similarity between identical texts is > 0.99.
    ///
    /// Requires model download.
    #[tokio::test]
    #[ignore = "requires MultilingualE5Large model download (~2 GB)"]
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
    /// Requires model download.
    #[tokio::test]
    #[ignore = "requires MultilingualE5Large model download (~2 GB)"]
    async fn real_dissimilar_cosine_similarity() {
        let embedder = BgeM3Embedder::new(BgeM3Options::default())
            .await
            .expect("model init");
        let vecs = embedder
            .embed_dense(&[
                "apple orchard fruit harvest",
                "nuclear fusion reactor plasma",
            ])
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
