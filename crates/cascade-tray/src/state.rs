//! TrayState and DaemonStatus — shared data types for the cascade tray.
//!
//! Purpose: Carry live cascade daemon telemetry that the tray icon renders.
//!   All fields are serializable so the daemon can push state updates over IPC
//!   (JSON) to the tray process without sharing memory.
//! Inputs: populated by the cascade daemon from its internal counters.
//! Outputs: consumed by TrayHandle::update() to refresh the menu/icon.
//! Constraints:
//!   - Must derive Default so tests can construct a zero-value instance.
//!   - Must derive PartialEq for serde round-trip assertions.
//!   - HashMap keys are tier identifiers (e.g. "T0", "T1", "T2", "T3").
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

/// Operational status of the cascade daemon process.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum DaemonStatus {
    /// Daemon is running and accepting work.
    Running,
    /// Daemon is alive but has paused new dispatches (e.g. quota pause).
    Paused,
    /// Daemon process is not running.
    #[default]
    Stopped,
}

/// Live telemetry snapshot displayed in the cascade system-tray icon.
///
/// The daemon serializes this struct to JSON and sends it to the tray process
/// over IPC. The tray backend deserializes it and updates the native menu.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TrayState {
    /// Active-agent counts per tier label (e.g. `{"T0": 1, "T2": 8}`).
    /// An empty map means no agents are running.
    pub tier_counts: HashMap<String, u32>,

    /// How stale the RAG index is, in seconds since last refresh.
    /// A value of 0 means fully fresh; u64::MAX is used when unknown.
    pub rag_freshness_secs: u64,

    /// Total number of agents currently dispatched across all tiers.
    pub active_agents: u32,

    /// Count of unread messages in the cascade inbox directories.
    pub inbox_unread: u32,

    /// Current operational state of the cascade daemon.
    pub daemon_status: DaemonStatus,

    /// Fleet quota summary for the menu-bar display (E-P6-03 v1.1).
    ///
    /// Each entry is formatted as `"account_id: used/limit"` or
    /// `"account_id: used"` when no limit is known. An empty `Vec` means
    /// no fleet data is available yet (quota-store.json absent or unreadable).
    #[serde(default)]
    pub fleet_summary: Vec<String>,
}

impl Default for TrayState {
    fn default() -> Self {
        Self {
            tier_counts: HashMap::new(),
            rag_freshness_secs: 0,
            active_agents: 0,
            inbox_unread: 0,
            daemon_status: DaemonStatus::Stopped,
            fleet_summary: Vec::new(),
        }
    }
}
