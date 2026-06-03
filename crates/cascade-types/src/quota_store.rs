//! Quota store types shared across all Cascade crates.
//!
//! These structs form the canonical schema for `~/.cascade/quota-store.json`.
//! The I/O functions (`write_quota_store`, `read_quota_store`) live in
//! `cascade-core::quota_store` to avoid pulling I/O dependencies into this
//! zero-IO crate. All other crates that need the types import from here
//! (`use cascade_types::QuotaStore`).
//!
//! [`QuotaState`] is the raw per-account snapshot emitted by the quota poller
//! (T-P2-E02-09). [`aggregate_quota`] (T-P2-E02-30, in `cascade-core`) accepts
//! a slice of `QuotaState` and builds the aggregated [`QuotaStore`].
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — QuotaStore / AccountEntry /
//!        ModelUsage / HistoryEntry / QuotaState rows (T-P2-E02-29, T-P2-E02-30)

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

// ── Schema version ────────────────────────────────────────────────────────────

/// Monotonically-increasing schema version for `~/.cascade/quota-store.json`.
///
/// Increment when a **breaking** change is made to [`QuotaStore`] (field
/// removal, type change). Additive new optional fields do not need a bump
/// because `serde(default)` handles them transparently.
pub const QUOTA_STORE_SCHEMA_VERSION: u32 = 1;

// ── Structs ───────────────────────────────────────────────────────────────────

/// Per-model usage snapshot within one account.
///
/// Inputs:  populated from a polled `/health` or API-usage endpoint.
/// Outputs: serialised into [`AccountEntry::models`].
/// Constraints: `pct_used` is clamped to `0.0..=100.0`; callers MUST NOT
///              write `NaN` — use `None` when the value is unknown.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ModelUsage {
    /// Tokens or requests used in the current billing window.
    pub used: u64,
    /// Hard limit for the current billing window, if known.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub limit: Option<u64>,
    /// ISO-8601 timestamp at which the limit resets, if known.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reset_at: Option<String>,
    /// Usage as a percentage of `limit`; `None` when `limit` is unknown.
    ///
    /// Invariant: when `Some`, the value must be in `0.0..=100.0`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pct_used: Option<f32>,
}

/// Usage snapshot for one AI account across all its models.
///
/// Inputs:  populated by the quota-aggregation layer (T-P2-E02-30).
/// Outputs: written into [`QuotaStore::accounts`].
/// Constraints: `account_id` must be stable across polling cycles so the
///              round-robin scheduler can use it as a stable key.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AccountEntry {
    /// Stable identifier for this account (e.g. `"cc-acct1"`, `"oc-dsv4"`).
    pub account_id: String,
    /// Which AI harness owns this account: `"cc"`, `"oc"`, `"codex"`, `"cursor"`.
    pub harness: String,
    /// Per-model usage keyed by model identifier (e.g. `"claude-sonnet-4-6"`).
    pub models: HashMap<String, ModelUsage>,
    /// Total tokens/requests used this calendar week across all models.
    pub week_total_used: u64,
    /// Total tokens/requests used this calendar month across all models.
    pub month_total_used: u64,
    /// ISO-8601 timestamp of the most recent successful poll for this account.
    pub last_polled: String,
}

/// A point-in-time snapshot stored in [`QuotaStore::rolling_history`].
///
/// Inputs:  cloned from the accounts Vec at snapshot time by the aggregator.
/// Outputs: serialised into the `rolling_history` array; older entries are
///          trimmed by the daemon based on `retention_days`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct HistoryEntry {
    /// Unix epoch seconds at which this snapshot was captured.
    pub ts: i64,
    /// Full account state at snapshot time.
    pub accounts_snapshot: Vec<AccountEntry>,
}

/// Raw per-account/per-model quota snapshot captured by the quota poller.
///
/// This is the **input** type for [`aggregate_quota`] (T-P2-E02-30).
/// One `QuotaState` is emitted per polling cycle; a slice of ordered snapshots
/// (oldest→newest) is consumed by `aggregate_quota` to build a [`QuotaStore`].
///
/// Inputs:  polled from AI-harness health/usage endpoints by the daemon.
/// Outputs: passed to `cascade_core::quota_aggregator::aggregate_quota`.
/// Constraints: `ts` is Unix epoch seconds; must be non-decreasing across the
///              ordered slice passed to `aggregate_quota`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct QuotaState {
    /// Stable identifier for the account being polled (e.g. `"cc-acct1"`).
    pub account_id: String,
    /// AI harness: `"cc"`, `"oc"`, `"codex"`, or `"cursor"`.
    pub harness: String,
    /// Unix epoch seconds when this snapshot was captured.
    pub ts: i64,
    /// Per-model usage at the time of the poll.
    pub models: HashMap<String, ModelUsage>,
}

/// Central quota/usage store — single source of truth for all harnesses.
///
/// Serialised to `~/.cascade/quota-store.json` by the daemon aggregator
/// (T-P2-E02-30) and read by the fleet widget, round-robin scheduler, and
/// P3 analytics. The JSON is written atomically (tmp→rename) so no reader
/// ever observes a partial file.
///
/// Inputs:  built by the aggregation layer; version-stamped at build time.
/// Outputs: persisted JSON; consumed by downstream readers.
/// Constraints: `rolling_history` must be trimmed by the daemon before each
///              write so it does not grow without bound.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct QuotaStore {
    /// Must equal [`QUOTA_STORE_SCHEMA_VERSION`]; validated on read.
    pub schema_version: u32,
    /// ISO-8601 timestamp of the last write.
    pub updated_at: String,
    /// One entry per known AI account (all harnesses).
    pub accounts: Vec<AccountEntry>,
    /// Cross-account totals per model for the current week (`model_id → used`).
    pub week_totals: HashMap<String, u64>,
    /// Cross-account totals per model for the current month.
    pub month_totals: HashMap<String, u64>,
    /// Rolling snapshots trimmed to the configured `retention_days`.
    pub rolling_history: Vec<HistoryEntry>,
}
