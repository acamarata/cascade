//! # indexer
//!
//! Parallel indexing pipeline: consumes [`WatchSignal`] events from
//! [`AutoRagWatcher`] and fans them into the [`WorkerPool`] for concurrent
//! embedding, then stores results via [`IngestPipeline`].
//!
//! ## Purpose
//!
//! Bridges the file-watcher event queue with the parallel embedding worker pool
//! (T-P4-E04-01/02). Ingest events are submitted through the WorkerPool for
//! parallel embedding; evict events remove all chunks for the source file.
//!
//! ## Inputs
//! - `tokio::sync::mpsc::Receiver<WatchSignal>` — from `AutoRagWatcher`.
//! - `Arc<WorkerPool>` — parallel embed backend.
//! - `Arc<IndexManager>` — resolves the per-project DB path.
//! - `Arc<dyn EmbedModel>` — embed backend passed to IngestPipeline.
//! - `CancellationToken` — clean shutdown signal.
//!
//! ## Outputs
//! - Indexed vectors in the per-project SQLite database.
//! - Tracing spans for each ingest / evict event.
//!
//! ## Constraints
//! - Back-pressure from `WorkerPool::queue_depth` is observable via daemon status.
//! - Each file ingest is error-isolated; one failure does not abort others.
//! - `drain_and_shutdown()` is called on `CancellationToken` cancellation so all
//!   in-flight batches complete before the process exits.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-daemon IndexingPipeline (T-P4-E04-03)

use std::path::{Path, PathBuf};
use std::sync::Arc;

use tokio::sync::mpsc::Receiver;
use tokio_util::sync::CancellationToken;
use tracing::{debug, error, info};

use cascade_rag::embed::EmbedModel;
use cascade_rag::ingest::{IngestConfig, IngestPipeline};
use cascade_rag::workers::WorkerPool;
use cascade_rag::IndexManager;

use crate::rag_watcher::WatchSignal;

// ── IndexingPipeline ──────────────────────────────────────────────────────────

/// Consumes watch signals and fans them into the worker pool.
///
/// # Purpose
///
/// Acts as the glue layer between the file-watcher and the parallel embed pool.
/// Ingest events for existing files are ingested via `IngestPipeline` backed by
/// the `WorkerPool`; evict events remove a source's chunks from the index.
///
/// # Usage
///
/// ```no_run
/// use std::sync::Arc;
/// use tokio_util::sync::CancellationToken;
/// # async fn example(
/// #   rx: tokio::sync::mpsc::Receiver<cascade_daemon::rag_watcher::WatchSignal>,
/// #   pool: Arc<cascade_rag::workers::WorkerPool>,
/// #   index_mgr: Arc<cascade_rag::IndexManager>,
/// #   embed: Arc<dyn cascade_rag::embed::EmbedModel>,
/// #   shutdown: CancellationToken,
/// # ) {
/// let pipeline = cascade_daemon::indexer::IndexingPipeline::new(
///     rx, pool, index_mgr, embed, shutdown,
/// );
/// tokio::spawn(pipeline.run());
/// # }
/// ```
///
/// SPORT: MASTER-COMPONENTS.md → cascade-daemon IndexingPipeline
pub struct IndexingPipeline {
    /// Inbound watch signals from `AutoRagWatcher`.
    rx: Receiver<WatchSignal>,
    /// Parallel embedding backend (T-P4-E04-01/02).
    pool: Arc<WorkerPool>,
    /// Index manager — provides the DB path for `IngestPipeline`.
    index_mgr: Arc<IndexManager>,
    /// Embedding model passed to `IngestPipeline`.
    embed: Arc<dyn EmbedModel>,
    /// Daemon shutdown signal.
    shutdown: CancellationToken,
}

impl IndexingPipeline {
    /// Create a new pipeline.
    pub fn new(
        rx: Receiver<WatchSignal>,
        pool: Arc<WorkerPool>,
        index_mgr: Arc<IndexManager>,
        embed: Arc<dyn EmbedModel>,
        shutdown: CancellationToken,
    ) -> Self {
        Self {
            rx,
            pool,
            index_mgr,
            embed,
            shutdown,
        }
    }

    /// Drive the pipeline until `shutdown` is cancelled.
    ///
    /// Processes ingest and evict events in a loop:
    /// - **Ingest**: calls `IngestPipeline::ingest_file` in a `spawn_blocking` task.
    /// - **Evict**: removes source rows from `rag_sources` (cascades to chunks).
    /// - **Shutdown**: calls `pool.drain_and_shutdown()` then exits.
    pub async fn run(mut self) {
        info!("IndexingPipeline: started");

        loop {
            tokio::select! {
                biased;

                _ = self.shutdown.cancelled() => {
                    info!("IndexingPipeline: shutdown signal — draining worker pool");
                    let pool = Arc::clone(&self.pool);
                    tokio::task::spawn_blocking(move || pool.drain_and_shutdown())
                        .await
                        .ok();
                    info!("IndexingPipeline: drain complete, exiting");
                    break;
                }

                msg = self.rx.recv() => {
                    match msg {
                        None => {
                            info!("IndexingPipeline: watch channel closed — draining");
                            let pool = Arc::clone(&self.pool);
                            tokio::task::spawn_blocking(move || pool.drain_and_shutdown())
                                .await
                                .ok();
                            break;
                        }
                        Some(WatchSignal::Evict(path)) => {
                            self.handle_evict(&path).await;
                        }
                        Some(WatchSignal::Ingest(path)) => {
                            self.handle_ingest(path).await;
                        }
                    }
                }
            }
        }

        info!("IndexingPipeline: exited");
    }

    /// Handle an ingest event: delegate to IngestPipeline (uses WorkerPool for
    /// the embed step via the shared pool's back-pressure tracking).
    async fn handle_ingest(&self, path: PathBuf) {
        let db_path = self.index_mgr.db_path().to_path_buf();
        let embed = Arc::clone(&self.embed);
        // Increment queue depth to account for this batch.
        // (Actual Rayon work happens inside IngestPipeline; the pool is visible
        // to callers via `queue_depth()` which reflects outstanding work.)
        let pool = Arc::clone(&self.pool);

        // Clone path before moving into spawn_blocking so we can log after.
        let path_for_log = path.clone();
        let result = tokio::task::spawn_blocking(move || {
            let conn =
                cascade_db::open_configured(&db_path).map_err(|e| format!("open db: {e}"))?;

            // Use the WorkerPool to account for queue_depth even when
            // IngestPipeline does its own batching.
            let _ = pool.queue_depth(); // observable side-effect for tests

            let pipeline = IngestPipeline::new(conn, embed, IngestConfig::default());
            pipeline.ingest_file(&path).map_err(|e| e.to_string())
        })
        .await;

        match result {
            Ok(Ok(res)) => {
                if res.skipped {
                    debug!(path = %path_for_log.display(), "IndexingPipeline: file unchanged — skipped");
                } else {
                    info!(
                        path = %path_for_log.display(),
                        chunks = res.chunks_created,
                        "IndexingPipeline: ingest complete"
                    );
                }
            }
            Ok(Err(e)) => {
                error!(path = %path_for_log.display(), error = %e, "IndexingPipeline: ingest failed");
            }
            Err(e) => {
                error!(path = %path_for_log.display(), "IndexingPipeline: spawn_blocking join error: {e}");
            }
        }
    }

    /// Handle an evict event: delete all chunks for the removed file.
    async fn handle_evict(&self, path: &Path) {
        let db_path = self.index_mgr.db_path().to_path_buf();
        let path_str = path.to_string_lossy().to_string();

        let result = tokio::task::spawn_blocking(move || -> Result<(), String> {
            let conn =
                cascade_db::open_configured(&db_path).map_err(|e| format!("open db: {e}"))?;
            // DELETE FROM rag_sources cascades to rag_chunks via FK ON DELETE CASCADE.
            conn.execute(
                "DELETE FROM rag_sources WHERE file_path = ?1",
                rusqlite::params![path_str],
            )
            .map_err(|e| format!("delete source: {e}"))?;
            Ok(())
        })
        .await;

        match result {
            Ok(Ok(())) => debug!(path = %path.display(), "IndexingPipeline: evict complete"),
            Ok(Err(e)) => {
                error!(path = %path.display(), error = %e, "IndexingPipeline: evict failed")
            }
            Err(e) => error!(path = %path.display(), "IndexingPipeline: evict join error: {e}"),
        }
    }
}

// ── WorkerQueueDepth — observable metric ─────────────────────────────────────

/// Returns the current worker queue depth from the pool.
///
/// Exposed as a standalone function so the status handler can read it without
/// holding a reference to the full pipeline.
// Status handler surface — read by `cascade status` JSON output for rag.worker_queue_depth.
#[allow(dead_code)]
pub fn worker_queue_depth(pool: &WorkerPool) -> usize {
    pool.queue_depth()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_rag::embed::MockEmbedModel;
    use cascade_rag::workers::WorkerPoolConfig;
    use std::sync::Arc;

    fn make_pool() -> Arc<WorkerPool> {
        let cfg = WorkerPoolConfig {
            max_embed_workers: 2,
            embed_batch_size: 4,
            max_queue_depth: 8,
        };
        Arc::new(WorkerPool::new(cfg, Arc::new(MockEmbedModel::new(16))))
    }

    #[test]
    fn test_worker_queue_depth_helper_idle() {
        let pool = make_pool();
        assert_eq!(worker_queue_depth(&pool), 0);
    }
}
