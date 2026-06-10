//! Daemon runtime state.
//!
//! Purpose: holds the daemon's in-memory state: active tasks, event subscribers,
//! quota snapshots, and other runtime data that must be shared across async tasks.
//!
//! Inputs:  initialized by the supervisor at startup; mutated by async tasks
//!          (quota poller, project poller, etc.).
//! Outputs: shared via Arc<Mutex<>> or Arc<RwLock<>> to multiple tasks.
//! Constraints: no file I/O in this module; all I/O is delegated to specific
//!              modules (config, event_bus, quota_store, etc.).
//! SPORT: .claude/docs/MASTER-DAEMON.md — DaemonState (T-P2-E02-31)

use std::collections::VecDeque;
use std::sync::Arc;

use cascade_rag::workers::WorkerPool;

/// In-memory daemon state.
///
/// Holds quota snapshots in a bounded ring buffer so the quota poller can
/// aggregate recent history without reading a file on every poll cycle.
/// Also holds the parallel embedding worker pool so the indexing pipeline
/// and health-check handler can share a single pool instance.
///
/// The snapshot type is a JSON value to remain agnostic to how quota polling
/// results are structured.
#[derive(Debug, Clone)]
pub struct DaemonState {
    /// Rolling buffer of the last N quota snapshots (oldest → newest).
    /// Bounded by config.quota_store.max_snapshots (default 1000).
    /// The quota poller pushes new snapshots here and prunes excess entries.
    /// Stored as serde_json::Value to remain schema-flexible across harnesses.
    pub snapshot_ring: VecDeque<serde_json::Value>,

    /// Parallel embedding worker pool for the RAG indexing pipeline.
    ///
    /// `None` until the daemon initialises RAG (requires a project root).
    /// Set by the supervisor after the first project is opened.
    /// Call `drain_and_shutdown()` on SIGTERM before dropping.
    ///
    /// SPORT: MASTER-COMPONENTS.md → WorkerPool (T-P4-E04-03)
    pub worker_pool: Option<Arc<WorkerPool>>,
}

impl DaemonState {
    /// Create a new DaemonState with an empty snapshot ring and no worker pool.
    pub fn new() -> Self {
        Self {
            snapshot_ring: VecDeque::new(),
            worker_pool: None,
        }
    }

    /// Returns the current RAG worker queue depth, or 0 if no pool is active.
    ///
    /// Used by the `cascade status` JSON output (`rag.worker_queue_depth`).
    pub fn worker_queue_depth(&self) -> usize {
        self.worker_pool
            .as_ref()
            .map(|p| p.queue_depth())
            .unwrap_or(0)
    }

    /// Gracefully drain and shut down the worker pool.
    ///
    /// Must be called on SIGTERM/SIGINT before dropping `DaemonState` to ensure
    /// all in-flight embedding batches complete before the process exits.
    pub fn drain_worker_pool(&self) {
        if let Some(ref pool) = self.worker_pool {
            pool.drain_and_shutdown();
        }
    }

    /// Add a snapshot to the ring and enforce the max-size bound.
    ///
    /// Inputs:  `snapshot` — the new quota snapshot (JSON value)
    ///          `max_snapshots` — bound size
    /// Outputs: the ring contains at most `max_snapshots` entries (oldest entries
    ///          are dropped if needed).
    ///
    /// This method is called by the quota poller after each successful poll.
    pub fn push_snapshot(&mut self, snapshot: serde_json::Value, max_snapshots: usize) {
        self.snapshot_ring.push_back(snapshot);
        while self.snapshot_ring.len() > max_snapshots {
            self.snapshot_ring.pop_front();
        }
    }
}

impl Default for DaemonState {
    fn default() -> Self {
        Self::new()
    }
}
