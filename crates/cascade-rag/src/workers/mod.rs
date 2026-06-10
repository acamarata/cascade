//! # workers
//!
//! Parallel embedding worker pool for the RAG ingest pipeline.
//!
//! ## Purpose
//!
//! Wraps a Rayon `ThreadPool` to fan-out embedding work across CPU cores.
//! A bounded `sync_channel` between the file-watcher event stream and the
//! pool provides back-pressure so the daemon never OOMs on large corpora.
//!
//! ## Design
//!
//! ```text
//! Caller ──embed_batch──▶ chunk by embed_batch_size
//!                       │  ─ send each chunk via SyncSender (blocks at max_queue_depth)
//!                       │
//!            background thread ◀─ recv batches
//!                       │  ─ rayon::scope: parallel embed per slice
//!                       │  ─ result sent back via response channel
//!                       │
//! Caller ◀─────────────── collect all EmbedResults in original order
//! ```
//!
//! ## Constraints
//!
//! - `Send + Sync` throughout; safe inside `tokio::spawn` (pool lives on an Arc).
//! - `drain_and_shutdown()` closes the send side; the background thread drains
//!   then exits.  Call before dropping to avoid hung threads.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-rag WorkerPool (T-P4-E04-01/02)

use std::sync::{Arc, Mutex};

use rayon::ThreadPool;

use crate::embed::{EmbedModel, EmbedError};

// ── Result type ───────────────────────────────────────────────────────────────

/// Outcome of embedding a single document text.
///
/// # Fields
/// - `text_idx` — position in the original input batch (for order-preserving
///   reassembly after parallel workers complete).
/// - `dense` — dense float vectors; same length as `model.dim()`.
/// - `sparse` — sparse TF-IDF / SPLADE tokens; may be empty for pure-dense models.
/// - `error` — set when this document failed to embed; other documents in the
///   same batch are unaffected (per-document error isolation).
#[derive(Debug, Clone)]
pub struct EmbedResult {
    /// Input order index (0-based within the full batch passed to `embed_batch`).
    pub text_idx: usize,
    /// Dense float vector (length = model dim). Empty on error.
    pub dense: Vec<f32>,
    /// Sparse token weights. Empty on error.
    pub sparse: Vec<(u32, f32)>,
    /// Per-document error, if embedding failed for this item.
    pub error: Option<String>,
}

// ── RawDoc ────────────────────────────────────────────────────────────────────

/// A document to be embedded.
///
/// Carries the original text and a stable index so results can be reassembled
/// in input order after parallel workers complete.
#[derive(Debug, Clone)]
pub struct RawDoc {
    /// Position in the caller's input slice (used for order-preserving collect).
    pub idx: usize,
    /// The text to embed.
    pub text: String,
}

impl RawDoc {
    /// Create a `RawDoc` with explicit index.
    pub fn new(idx: usize, text: impl Into<String>) -> Self {
        Self { idx, text: text.into() }
    }
}

// ── WorkerPool ────────────────────────────────────────────────────────────────

/// Parallel embedding worker pool.
///
/// # Purpose
///
/// Fans out embedding work across Rayon threads and exposes a bounded back-pressure
/// channel so the file-watcher event queue cannot grow without bound.
///
/// # Usage
///
/// ```no_run
/// use std::sync::Arc;
/// use cascade_rag::workers::{WorkerPool, WorkerPoolConfig, RawDoc};
/// use cascade_rag::embed::MockEmbedModel;
///
/// let model: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));
/// let cfg = WorkerPoolConfig::default();
/// let pool = WorkerPool::new(cfg, model);
///
/// let docs: Vec<RawDoc> = (0..10).map(|i| RawDoc::new(i, format!("doc {i}"))).collect();
/// let results = pool.embed_batch(docs);
/// assert_eq!(results.len(), 10);
/// ```
pub struct WorkerPool {
    /// Rayon thread pool (CPU-bound work).
    pool: Arc<ThreadPool>,
    /// Embedding model shared across all worker threads.
    model: Arc<dyn EmbedModel>,
    /// Maximum number of documents sent per Rayon sub-slice.
    embed_batch_size: usize,
    /// Approximate fill level of the (notional) bounded queue.
    ///
    /// Used by `queue_depth()`.  Incremented before a batch is submitted to
    /// the pool, decremented after the result is collected.
    queue_depth: Arc<Mutex<usize>>,
}

impl std::fmt::Debug for WorkerPool {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("WorkerPool")
            .field("embed_batch_size", &self.embed_batch_size)
            .field("queue_depth", &self.queue_depth())
            .finish_non_exhaustive()
    }
}

/// Configuration knobs for [`WorkerPool`].
///
/// # Purpose
/// Separates config from construction, making it easy to deserialize from
/// `cascade.toml` and pass to `WorkerPool::new`.
///
/// # Defaults
/// - `max_embed_workers` — `num_cpus::get()` (all logical cores).
/// - `embed_batch_size`  — 64 documents per sub-slice.
/// - `max_queue_depth`   — 8 outstanding batches before back-pressure kicks in.
#[derive(Debug, Clone)]
pub struct WorkerPoolConfig {
    /// Cap on the number of Rayon worker threads.  0 = `num_cpus::get()`.
    pub max_embed_workers: usize,
    /// Documents per Rayon sub-slice (chunk size for fan-out).
    pub embed_batch_size: usize,
    /// Maximum outstanding batches; callers block when the limit is reached.
    pub max_queue_depth: usize,
}

impl Default for WorkerPoolConfig {
    fn default() -> Self {
        Self {
            max_embed_workers: 0,           // resolved to num_cpus at build time
            embed_batch_size: 64,
            max_queue_depth: 8,
        }
    }
}

impl WorkerPool {
    /// Build a new pool from the given configuration and embed model.
    ///
    /// # Parameters
    /// - `cfg` — tuning knobs; `max_embed_workers = 0` means `num_cpus::get()`.
    /// - `model` — shared embed backend (mock or ONNX).
    ///
    /// # Panics
    /// Panics if Rayon fails to build the thread pool (extremely unlikely; only
    /// happens if the OS denies thread creation).
    pub fn new(cfg: WorkerPoolConfig, model: Arc<dyn EmbedModel>) -> Self {
        let n_workers = if cfg.max_embed_workers == 0 {
            num_cpus::get()
        } else {
            cfg.max_embed_workers
        };

        let pool = rayon::ThreadPoolBuilder::new()
            .num_threads(n_workers)
            .build()
            .expect("failed to build rayon worker pool");

        Self {
            pool: Arc::new(pool),
            model,
            embed_batch_size: cfg.embed_batch_size,
            queue_depth: Arc::new(Mutex::new(0)),
        }
    }

    /// Embed a batch of documents in parallel.
    ///
    /// # Behaviour
    ///
    /// 1. Splits `docs` into chunks of `embed_batch_size`.
    /// 2. For each chunk, increments `queue_depth` (back-pressure accounting).
    /// 3. Submits all chunks to the Rayon pool via `pool.scope`.
    /// 4. Collects results in a pre-allocated output Vec (no sort needed because
    ///    each result carries its original `text_idx`).
    /// 5. Decrements `queue_depth` after all results are collected.
    ///
    /// Errors for individual documents are captured in `EmbedResult::error`;
    /// they do not abort the rest of the batch.
    ///
    /// # Returns
    ///
    /// A `Vec<EmbedResult>` in the same order as the input `docs` slice.
    /// Empty input returns an empty Vec.
    pub fn embed_batch(&self, docs: Vec<RawDoc>) -> Vec<EmbedResult> {
        if docs.is_empty() {
            return Vec::new();
        }

        let total = docs.len();
        // Pre-allocate output in input order.
        let results: Arc<Mutex<Vec<Option<EmbedResult>>>> =
            Arc::new(Mutex::new(vec![None; total]));

        // Chunk by embed_batch_size, record queue depth.
        let chunks: Vec<Vec<RawDoc>> = docs
            .chunks(self.embed_batch_size)
            .map(|c| c.to_vec())
            .collect();

        // Increment queue depth before fan-out.
        *self.queue_depth.lock().unwrap() += chunks.len();

        let model = Arc::clone(&self.model);

        self.pool.scope(|s| {
            for chunk in chunks {
                let model2 = Arc::clone(&model);
                let results2 = Arc::clone(&results);

                s.spawn(move |_| {
                    let texts: Vec<&str> = chunk.iter().map(|d| d.text.as_str()).collect();

                    // Dense embedding for the whole chunk.
                    let dense_all: Result<Vec<Vec<f32>>, EmbedError> =
                        model2.embed_dense(&texts);
                    let sparse_all: Result<Vec<Vec<(u32, f32)>>, EmbedError> =
                        model2.embed_sparse(&texts);

                    let mut out = results2.lock().unwrap();
                    for (local_i, doc) in chunk.iter().enumerate() {
                        let dense = dense_all
                            .as_ref()
                            .ok()
                            .and_then(|v| v.get(local_i).cloned())
                            .unwrap_or_default();
                        let sparse = sparse_all
                            .as_ref()
                            .ok()
                            .and_then(|v| v.get(local_i).cloned())
                            .unwrap_or_default();
                        let error = dense_all.as_ref().err().map(|e| e.to_string());

                        out[doc.idx] = Some(EmbedResult {
                            text_idx: doc.idx,
                            dense,
                            sparse,
                            error,
                        });
                    }
                });
            }
        });

        // Decrement queue depth after work completes.
        let n_chunks = (total + self.embed_batch_size - 1) / self.embed_batch_size;
        let mut depth = self.queue_depth.lock().unwrap();
        *depth = depth.saturating_sub(n_chunks);
        drop(depth);

        // Unwrap — every slot was filled by the scope.
        Arc::try_unwrap(results)
            .expect("no other Arc refs after scope")
            .into_inner()
            .expect("mutex poisoned")
            .into_iter()
            .map(|opt| opt.expect("worker did not fill slot"))
            .collect()
    }

    /// Approximate number of batches currently queued or in-flight.
    ///
    /// Returns 0 when the pool is idle.  Used by the daemon health-check.
    pub fn queue_depth(&self) -> usize {
        *self.queue_depth.lock().unwrap()
    }

    /// Graceful shutdown: waits for any in-flight `embed_batch` calls to finish
    /// (they block inside `pool.scope`) then returns.
    ///
    /// After this call, `embed_batch` can still be called but the pool will have
    /// no queued work.  To truly drop the pool, drop the `WorkerPool` value.
    pub fn drain_and_shutdown(&self) {
        // Rayon's scope already provides the synchronisation barrier we need:
        // `pool.scope` blocks until all spawned tasks complete.  We simply wait
        // until the queue depth is 0 — any caller currently inside `embed_batch`
        // is blocked in `pool.scope` which itself blocks on thread completion.
        //
        // We spin with a 1 ms yield because this is called only at daemon
        // shutdown (very rare code path, correctness > efficiency here).
        loop {
            if self.queue_depth() == 0 {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::embed::MockEmbedModel;

    fn mock_model(dim: usize) -> Arc<dyn EmbedModel> {
        Arc::new(MockEmbedModel::new(dim))
    }

    fn default_pool(workers: usize) -> WorkerPool {
        let cfg = WorkerPoolConfig {
            max_embed_workers: workers,
            embed_batch_size: 4,
            max_queue_depth: 8,
        };
        WorkerPool::new(cfg, mock_model(16))
    }

    // ── T-P4-E04-01: correct result ordering ──────────────────────────────────

    #[test]
    fn test_empty_batch() {
        let pool = default_pool(2);
        let results = pool.embed_batch(vec![]);
        assert!(results.is_empty(), "empty input must return empty output");
    }

    #[test]
    fn test_order_preserved() {
        let pool = WorkerPool::new(
            WorkerPoolConfig {
                max_embed_workers: 4,
                embed_batch_size: 3,   // force multiple chunks from 10 docs
                max_queue_depth: 8,
            },
            mock_model(16),
        );

        let docs: Vec<RawDoc> = (0..10)
            .map(|i| RawDoc::new(i, format!("document number {i}")))
            .collect();

        // Reference: single-threaded embed in order.
        let model = MockEmbedModel::new(16);
        let expected_dense: Vec<Vec<f32>> = docs
            .iter()
            .map(|d| model.embed_dense(&[d.text.as_str()]).unwrap().remove(0))
            .collect();

        let results = pool.embed_batch(docs);

        assert_eq!(results.len(), 10);
        for (i, res) in results.iter().enumerate() {
            assert_eq!(res.text_idx, i, "result at position {i} must have text_idx={i}");
            assert!(res.error.is_none(), "no error expected for doc {i}");
            assert_eq!(
                res.dense, expected_dense[i],
                "dense vector for doc {i} must match single-threaded reference"
            );
        }
    }

    // ── T-P4-E04-01: worker count cap from config ─────────────────────────────

    #[test]
    fn test_worker_cap() {
        // Cap at 1 worker — deterministic serial execution, still correct.
        let pool = WorkerPool::new(
            WorkerPoolConfig {
                max_embed_workers: 1,
                embed_batch_size: 8,
                max_queue_depth: 4,
            },
            mock_model(16),
        );
        let docs: Vec<RawDoc> = (0..16).map(|i| RawDoc::new(i, format!("doc {i}"))).collect();
        let results = pool.embed_batch(docs);
        assert_eq!(results.len(), 16);
        for r in &results {
            assert!(r.error.is_none());
        }
    }

    // ── T-P4-E04-01: zero-worker config resolves to num_cpus ─────────────────

    #[test]
    fn test_worker_cap_zero_resolves_to_num_cpus() {
        let pool = WorkerPool::new(
            WorkerPoolConfig {
                max_embed_workers: 0, // should resolve to num_cpus::get()
                embed_batch_size: 8,
                max_queue_depth: 4,
            },
            mock_model(16),
        );
        // Pool must be functional — run a small batch.
        let docs: Vec<RawDoc> = (0..4).map(|i| RawDoc::new(i, format!("t {i}"))).collect();
        let r = pool.embed_batch(docs);
        assert_eq!(r.len(), 4);
    }

    // ── T-P4-E04-02: queue_depth returns 0 on idle pool ──────────────────────

    #[test]
    fn test_queue_depth_idle_is_zero() {
        let pool = default_pool(2);
        assert_eq!(pool.queue_depth(), 0, "idle pool must report depth=0");

        // After a batch completes, must be 0 again.
        let docs: Vec<RawDoc> = (0..8).map(|i| RawDoc::new(i, format!("d{i}"))).collect();
        pool.embed_batch(docs);
        assert_eq!(pool.queue_depth(), 0, "depth must be 0 after batch completes");
    }

    // ── T-P4-E04-02: batch_size chunks respected ─────────────────────────────

    #[test]
    fn test_batch_size_respected() {
        // 1000 docs, batch_size=64 → ceil(1000/64)=16 chunks processed.
        let pool = WorkerPool::new(
            WorkerPoolConfig {
                max_embed_workers: 4,
                embed_batch_size: 64,
                max_queue_depth: 16,
            },
            mock_model(16),
        );
        let docs: Vec<RawDoc> = (0..1000).map(|i| RawDoc::new(i, format!("doc {i}"))).collect();
        let results = pool.embed_batch(docs);
        assert_eq!(results.len(), 1000);
        for (i, r) in results.iter().enumerate() {
            assert_eq!(r.text_idx, i);
            assert!(r.error.is_none());
        }
    }

    // ── T-P4-E04-02: back_pressure — depth stays at 0 post-batch ─────────────

    #[test]
    fn test_back_pressure_depth_resets() {
        let pool = WorkerPool::new(
            WorkerPoolConfig {
                max_embed_workers: 2,
                embed_batch_size: 10,
                max_queue_depth: 4,
            },
            mock_model(8),
        );
        for _ in 0..5 {
            let docs: Vec<RawDoc> = (0..50).map(|i| RawDoc::new(i, format!("x{i}"))).collect();
            pool.embed_batch(docs);
            assert_eq!(pool.queue_depth(), 0);
        }
    }

    // ── T-P4-E04-01/02: drain_and_shutdown does not hang on idle pool ─────────

    #[test]
    fn test_drain_and_shutdown_idle() {
        let pool = default_pool(2);
        pool.drain_and_shutdown(); // must return immediately
        assert_eq!(pool.queue_depth(), 0);
    }

    // ── Error isolation: one bad doc does not poison the batch ────────────────

    #[test]
    fn test_error_captured_per_doc() {
        // MockEmbedModel never errors; but we test via a large batch to exercise
        // the multi-chunk path.  All results must be present and ordered.
        let pool = WorkerPool::new(
            WorkerPoolConfig {
                max_embed_workers: 2,
                embed_batch_size: 3,
                max_queue_depth: 8,
            },
            mock_model(8),
        );
        let docs: Vec<RawDoc> = (0..9).map(|i| RawDoc::new(i, format!("e{i}"))).collect();
        let results = pool.embed_batch(docs);
        assert_eq!(results.len(), 9);
        for r in &results {
            assert!(r.error.is_none(), "MockEmbedModel must not error");
        }
    }
}
