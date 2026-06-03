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

/// In-memory daemon state.
///
/// Holds quota snapshots in a bounded ring buffer so the quota poller can
/// aggregate recent history without reading a file on every poll cycle.
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
}

impl DaemonState {
    /// Create a new DaemonState with an empty snapshot ring.
    pub fn new() -> Self {
        Self {
            snapshot_ring: VecDeque::new(),
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
