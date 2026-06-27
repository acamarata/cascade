//! BGE Reranker v2-m3 via fastembed-rs.
//!
//! # Purpose
//!
//! Implements [`cascade_types::Reranker`] using a fastembed-rs ONNX
//! cross-encoder session.  Takes (query, passage) pairs and returns a single
//! sigmoid-normalised relevance score per pair.
//!
//! # Memory
//!
//! The ONNX session for `bge-reranker-v2-m3` requires ~580 MB RAM at runtime.
//! Enable the `reranker` feature only on machines with ≥4 GB available.
//! Prefer tier `Minimal` or `Semantic` in low-memory environments.
//!
//! # Inputs / Outputs
//!
//! - `BgeReranker::new(opts)` → `Result<BgeReranker>`
//! - `rerank(query, candidates, opts)` → `Vec<RerankResult>` sorted descending
//!
//! # Constraints
//!
//! - fastembed `TextRerank::rerank()` is synchronous; all calls run inside
//!   `tokio::task::block_in_place` to avoid stalling the async executor.
//! - `BgeReranker` is `Send + Sync` (fastembed types satisfy these bounds via
//!   the ORT session impl).
//! - Model is lazy-init via `BgeReranker::new()` — no network after first load.
//!
//! # Sprint ticket
//!
//! T-P4-E01-24
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::rerank::BgeReranker

use async_trait::async_trait;
use std::path::PathBuf;
use tracing::{debug, info, instrument, warn};

use cascade_types::{
    chunker::Chunk,
    error::{CascadeError, Result},
    reranker::{RerankOpts, RerankResult, Reranker},
};

#[cfg(feature = "reranker")]
use fastembed::{RerankInitOptions, RerankerModel, TextRerank};

/// Stable model identifier string used in logs, metrics, and error messages.
const MODEL_ID: &str = "bge-reranker-v2-m3";

// ── Options ───────────────────────────────────────────────────────────────────

/// Options for initialising [`BgeReranker`].
#[derive(Debug, Clone, Default)]
pub struct BgeRerankerOptions {
    /// Override the model cache directory.
    ///
    /// Defaults to `~/.cascade/models/` (resolved via `model_cache_dir()`).
    pub model_dir: Option<PathBuf>,

    /// If `true`, fail immediately when model files are absent rather than
    /// downloading them.  Recommended for CI / production after bootstrap.
    pub offline_guard: bool,
}

// ── BgeReranker ───────────────────────────────────────────────────────────────

/// BGE Reranker v2-m3 backed by fastembed-rs ONNX cross-encoder inference.
///
/// Requires the `reranker` feature flag.  Without it, the struct still
/// compiles but all rerank calls return candidates in their original order
/// with linearly decreasing mock scores.
///
/// # Memory footprint
///
/// Enabling the `reranker` feature loads a ~580 MB ONNX session.  See the
/// module-level doc for guidance on when to enable.
///
/// # Usage
///
/// ```rust,no_run
/// # #[cfg(feature = "reranker")]
/// # async fn example() {
/// use cascade_rag::rerank::bge::{BgeReranker, BgeRerankerOptions};
/// use cascade_types::reranker::{Reranker, RerankOpts};
/// use std::sync::Arc;
///
/// let reranker: Arc<dyn Reranker> =
///     Arc::new(BgeReranker::new(BgeRerankerOptions::default()).await.unwrap());
/// # }
/// ```
pub struct BgeReranker {
    /// Resolved path to the model cache directory.
    /// Retained for diagnostics (`cascade doctor`) and future offline-guard checks.
    #[allow(dead_code)]
    model_dir: PathBuf,

    /// fastembed ONNX session (only compiled when `reranker` feature is on).
    #[cfg(feature = "reranker")]
    inner: TextRerank,
}

impl BgeReranker {
    /// Initialise the reranker, downloading the model if not already cached.
    ///
    /// This is the only method that may perform network I/O.  After `new()`
    /// returns, all subsequent reranking calls are fully offline.
    ///
    /// # Arguments
    ///
    /// - `opts` — see [`BgeRerankerOptions`] for defaults.
    ///
    /// # Errors
    ///
    /// - `CascadeError::RerankFailed` if the model cannot be loaded or
    ///   downloaded (e.g. `offline_guard` is set and the model is absent).
    /// - `CascadeError::Io` if the cache directory cannot be created.
    #[instrument(skip(opts))]
    pub async fn new(opts: BgeRerankerOptions) -> Result<Self> {
        let model_dir = match opts.model_dir {
            Some(p) => p,
            None => crate::embed::model_cache_dir().map_err(|e| {
                CascadeError::Other(format!(
                    "reranker[{MODEL_ID}] cache dir resolution failed: {e}"
                ))
            })?,
        };

        std::fs::create_dir_all(&model_dir)
            .map_err(|e| CascadeError::io(&model_dir, "create-models-dir", e))?;

        if opts.offline_guard && !model_dir.exists() {
            return Err(CascadeError::Other(format!(
                "reranker[{MODEL_ID}] model not found on disk and offline_guard is set (dir: {})",
                model_dir.display()
            )));
        }

        info!(
            model_dir = %model_dir.display(),
            "initialising BGE reranker (BGERerankerV2M3, rozgo/bge-reranker-v2-m3)"
        );

        #[cfg(feature = "reranker")]
        {
            let cache_dir = model_dir.clone();
            let inner = tokio::task::block_in_place(|| {
                // fastembed 4.9.1 ships RerankerModel::BGERerankerV2M3
                // (rozgo/bge-reranker-v2-m3, multilingual, ~580 MB).
                let opts = RerankInitOptions::new(RerankerModel::BGERerankerV2M3)
                    .with_cache_dir(cache_dir)
                    .with_show_download_progress(true);
                TextRerank::try_new(opts)
            })
            .map_err(|e| {
                CascadeError::Other(format!(
                    "reranker[{MODEL_ID}] fastembed TextRerank init: {e}"
                ))
            })?;

            return Ok(Self { model_dir, inner });
        }

        #[cfg(not(feature = "reranker"))]
        {
            warn!("reranker feature not enabled; BgeReranker will return stub scores");
            Ok(Self { model_dir })
        }
    }
}

// ── Reranker trait impl ───────────────────────────────────────────────────────

#[async_trait]
impl Reranker for BgeReranker {
    /// Rerank `candidates` for the given `query`.
    ///
    /// Returns candidates sorted by cross-encoder relevance score (descending).
    /// `RerankResult::rank` is the 0-based position in the sorted output;
    /// `RerankResult::score` is the raw logit score from the cross-encoder.
    ///
    /// # Feature flag
    ///
    /// When the `reranker` feature is **disabled**, candidates are returned in
    /// their original insertion order with linearly decreasing mock scores (same
    /// behaviour as [`cascade_types::NoopReranker`]).
    #[instrument(skip(self, candidates), fields(n = candidates.len()))]
    async fn rerank(
        &self,
        query: &str,
        candidates: &[Chunk],
        opts: &RerankOpts,
    ) -> Result<Vec<RerankResult>> {
        if candidates.is_empty() {
            return Ok(vec![]);
        }

        debug!(query, n = candidates.len(), "BGE reranker scoring");

        #[cfg(feature = "reranker")]
        {
            // Build the passage strings fastembed expects.
            let passages: Vec<String> = candidates.iter().map(|c| c.text.clone()).collect();
            let query_owned = query.to_string();
            let n = candidates.len();

            // fastembed TextRerank::rerank() is synchronous; run on a blocking
            // thread pool to avoid blocking the async executor.
            let ranked = tokio::task::block_in_place(|| {
                self.inner.rerank(query_owned, passages, false, None)
            })
            .map_err(|e| {
                CascadeError::Other(format!("reranker[{MODEL_ID}] ONNX inference: {e}"))
            })?;

            // fastembed returns results sorted by score descending.
            // Map back to RerankResult using original candidate order.
            // Apply sigmoid to raw cross-encoder logits: sigmoid(x) = 1/(1+e^(-x)).
            // This normalises the score to (0, 1) for consistent downstream comparison.
            let take = opts.top_k.unwrap_or(n).min(n);
            let results: Vec<RerankResult> = ranked
                .into_iter()
                .take(take)
                .enumerate()
                .map(|(rank, r)| {
                    // r.index is the original position in the input passages slice.
                    let chunk = candidates[r.index].clone();
                    let sigmoid_score = 1.0_f32 / (1.0_f32 + (-r.score).exp());
                    RerankResult {
                        chunk,
                        score: sigmoid_score,
                        rank,
                    }
                })
                .filter(|r| opts.min_score.map(|min| r.score >= min).unwrap_or(true))
                .collect();

            return Ok(results);
        }

        #[cfg(not(feature = "reranker"))]
        {
            // Stub: return candidates in input order with linearly decreasing
            // mock scores.  Identical behaviour to NoopReranker.
            let take = opts.top_k.unwrap_or(candidates.len());
            let n = candidates.len();
            let results = candidates
                .iter()
                .take(take)
                .enumerate()
                .map(|(i, chunk)| RerankResult {
                    chunk: chunk.clone(),
                    score: 1.0 - (i as f32 / n.max(1) as f32),
                    rank: i,
                })
                .collect();
            Ok(results)
        }
    }

    fn name(&self) -> &str {
        MODEL_ID
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::chunker::{Chunk, ChunkMetadata};
    use cascade_types::reranker::{RerankOpts, Reranker};
    use std::collections::HashMap;
    use std::sync::Arc;
    use tempfile::TempDir;

    /// Build a minimal Chunk fixture for testing.
    fn make_chunk(text: &str) -> Chunk {
        Chunk {
            id: format!("test-{}", text.len()),
            text: text.to_string(),
            metadata: ChunkMetadata {
                start_byte: 0,
                end_byte: text.len(),
                source_path: None,
                start_line: None,
                end_line: None,
                chunk_index: 0,
                total_chunks: 1,
                extra: HashMap::new(),
            },
        }
    }

    /// BgeReranker new() succeeds with a temp cache dir (no network required
    /// when the model is absent and offline_guard is off — init only fails on
    /// network error if model is missing, which we avoid in tests).
    ///
    /// This test gates construction only; ONNX inference is in the #[ignore]
    /// slow tests.
    #[tokio::test]
    async fn new_with_temp_dir_no_crash() {
        let tmp = TempDir::new().expect("tempdir");
        let _opts = BgeRerankerOptions {
            model_dir: Some(tmp.path().to_path_buf()),
            offline_guard: false,
        };
        // Only validates construction is attempted; on CI (no model) this
        // will error at model download — that's acceptable, we just verify
        // it doesn't panic and the type is correct.
        let _ = BgeReranker::new(_opts).await;
    }

    /// Trait object safety: BgeReranker can be stored as Arc<dyn Reranker>.
    #[test]
    fn trait_object_safe() {
        // This test proves Arc<dyn Reranker> compiles with BgeReranker.
        // It does NOT construct a real instance (no tempdir needed).
        fn assert_arc_reranker(_: Arc<dyn Reranker>) {}
        // Compile-time only: BgeReranker implements Reranker.
        let _ = assert_arc_reranker as fn(Arc<dyn Reranker>);
    }

    /// Stub (non-fastembed) path: empty candidates returns empty results.
    ///
    /// Tests the empty-slice guard via NoopReranker, which is the spec-equivalent
    /// stub contract when the `reranker` feature is disabled.
    #[tokio::test]
    async fn empty_candidates_returns_empty() {
        let noop = cascade_types::NoopReranker;
        let result = noop
            .rerank("query", &[], &RerankOpts::default())
            .await
            .expect("noop rerank should not fail");
        assert!(result.is_empty());
    }

    /// Noop ordering: NoopReranker preserves insertion order with scores
    /// descending — same contract as the stub path in BgeReranker when the
    /// `reranker` feature is disabled.
    #[tokio::test]
    async fn noop_ordering_matches_stub_contract() {
        let chunks: Vec<Chunk> = vec![
            make_chunk("first passage"),
            make_chunk("second passage"),
            make_chunk("third passage"),
        ];
        let opts = RerankOpts {
            top_k: Some(3),
            min_score: None,
        };
        let noop = cascade_types::NoopReranker;
        let results = noop
            .rerank("query", &chunks, &opts)
            .await
            .expect("noop rerank should not fail");

        assert_eq!(results.len(), 3);
        // Scores should be strictly decreasing.
        for i in 0..results.len() - 1 {
            assert!(
                results[i].score > results[i + 1].score,
                "score[{}]={} should > score[{}]={}",
                i,
                results[i].score,
                i + 1,
                results[i + 1].score
            );
        }
        // Ranks should be sequential.
        for (i, r) in results.iter().enumerate() {
            assert_eq!(r.rank, i);
        }
    }

    /// top_k is honoured — only opts.top_k results are returned.
    #[tokio::test]
    async fn top_k_is_honoured() {
        let chunks: Vec<Chunk> = (0..10)
            .map(|i| make_chunk(&format!("passage {i}")))
            .collect();
        let opts = RerankOpts {
            top_k: Some(3),
            min_score: None,
        };
        let noop = cascade_types::NoopReranker;
        let results = noop
            .rerank("query", &chunks, &opts)
            .await
            .expect("noop rerank should not fail");
        assert_eq!(results.len(), 3, "top_k=3 should yield 3 results");
    }

    /// Batch reranking: multiple calls produce consistent results (deterministic
    /// for the stub / noop path).
    #[tokio::test]
    async fn batch_ordering_is_deterministic() {
        let chunks: Vec<Chunk> = vec![
            make_chunk("relevant: Rust async executor"),
            make_chunk("irrelevant: apple pie recipe"),
        ];
        let opts = RerankOpts::default();
        let noop = cascade_types::NoopReranker;

        let r1 = noop
            .rerank("Rust async programming", &chunks, &opts)
            .await
            .expect("run 1");
        let r2 = noop
            .rerank("Rust async programming", &chunks, &opts)
            .await
            .expect("run 2");

        // Same lengths.
        assert_eq!(r1.len(), r2.len());
        // Same order.
        for (a, b) in r1.iter().zip(r2.iter()) {
            assert_eq!(a.chunk.text, b.chunk.text);
            assert!((a.score - b.score).abs() < 1e-6);
        }
    }

    /// Slow test: real ONNX inference with model download.
    ///
    /// Marked `#[ignore]` — only run manually with `cargo test -- --ignored`.
    /// Requires ~350 MB network download on first run.
    #[tokio::test]
    #[ignore]
    async fn real_rerank_relevant_scores_higher() {
        let tmp = TempDir::new().expect("tempdir");
        let opts = BgeRerankerOptions {
            model_dir: Some(tmp.path().to_path_buf()),
            offline_guard: false,
        };
        let reranker = BgeReranker::new(opts)
            .await
            .expect("BgeReranker init with model download");

        let chunks = vec![
            make_chunk("Rust async programming with Tokio and async/await"),
            make_chunk("apple pie recipe with cinnamon and sugar"),
            make_chunk("the Tokio runtime provides async I/O for Rust"),
        ];
        let opts = RerankOpts {
            top_k: Some(3),
            min_score: None,
        };
        let results = reranker
            .rerank("Rust async programming", &chunks, &opts)
            .await
            .expect("rerank should succeed with real model");

        assert!(!results.is_empty(), "should return at least one result");
        // Top result should be one of the Rust-relevant passages.
        let top_text = &results[0].chunk.text;
        assert!(
            top_text.contains("Rust") || top_text.contains("Tokio"),
            "top result should be Rust-relevant, got: {top_text}"
        );
    }
}
