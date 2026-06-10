//! Usage analytics types for the Cascade IPC layer (T-P3-E04-29).
//!
//! # Purpose
//! Defines the wire types for three IPC commands that expose the central
//! quota/usage store (`~/.cascade/usage/YYYY-MM-DD.json`) to the React UI:
//!   - `cascade_usage_summary`  — aggregated totals for a period (day/week/month)
//!   - `cascade_usage_history`  — one entry per month for the last N months
//!   - `cascade_usage_ledger`   — per-account ledger entries for a period
//!
//! # Design
//! All structs are `#[serde(rename_all = "camelCase")]` so the TS consumer
//! receives idiomatic camelCase JSON without extra transformation.
//!
//! # Constraints
//! - Zero I/O in this module — types only; aggregation lives in `cascade-daemon`.
//! - `f64` cost fields: callers MUST NOT write `NaN`/`Infinity`; use `0.0` for
//!   unknown or free-tier cost.
//!
//! SPORT: MASTER-RUST-CRATES.md — cascade-types: usage_analytics module (T-P3-E04-29)

use serde::{Deserialize, Serialize};

// ── UsagePeriod ───────────────────────────────────────────────────────────────

/// Selects which calendar window a summary covers.
///
/// `offset = 0` is the current window; `offset = -1` is the previous one.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UsagePeriod {
    /// Calendar window granularity.
    pub kind: UsagePeriodKind,
    /// How many windows to step back from the current one.  0 = current.
    pub offset: i32,
}

/// Granularity of a [`UsagePeriod`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum UsagePeriodKind {
    Day,
    Week,
    Month,
}

// ── Per-axis breakdowns ───────────────────────────────────────────────────────

/// Token and cost usage attributed to one model.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelUsageStat {
    /// Model identifier (e.g. `"claude-sonnet-4-6"`).
    pub model: String,
    /// Total tokens (prompt + completion).
    pub tokens: u64,
    /// Estimated USD cost.
    pub cost_usd: f64,
}

/// Token and cost usage attributed to one account.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountUsageStat {
    /// Account email address.
    pub email: String,
    /// Total tokens (prompt + completion).
    pub tokens: u64,
    /// Estimated USD cost.
    pub cost_usd: f64,
}

// ── UsageSummary (cascade_usage_summary) ─────────────────────────────────────

/// Aggregated usage for a single [`UsagePeriod`].
///
/// Returned by `cascade_usage_summary`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UsageSummary {
    /// Total tokens across all models and accounts in the period.
    pub total_tokens: u64,
    /// Total estimated USD cost in the period.
    pub total_cost_usd: f64,
    /// Per-model breakdown.
    pub by_model: Vec<ModelUsageStat>,
    /// Per-account breakdown.
    pub by_account: Vec<AccountUsageStat>,
    /// Human-readable label for the period (e.g. `"Week of 2026-06-02"`).
    pub period_label: String,
    /// ISO-8601 date (`YYYY-MM-DD`) of the first day included.
    pub period_start: String,
    /// ISO-8601 date (`YYYY-MM-DD`) of the last day included.
    pub period_end: String,
}

// ── MonthlyUsage (cascade_usage_history) ─────────────────────────────────────

/// Usage totals for one calendar month.
///
/// One entry in the `Vec` returned by `cascade_usage_history`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MonthlyUsage {
    /// Display label (e.g. `"Jun 2026"`).
    pub month_label: String,
    /// Total tokens for the month.
    pub tokens: u64,
    /// Total estimated USD cost for the month.
    pub cost_usd: f64,
    /// Per-model breakdown for the month.
    pub by_model: Vec<ModelUsageStat>,
}

// ── AccountLedger (cascade_usage_ledger) ─────────────────────────────────────

/// A single completion event recorded in the usage ledger.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LedgerEntry {
    /// ISO-8601 timestamp of the completion (UTC).
    pub timestamp: String,
    /// Model identifier.
    pub model: String,
    /// Prompt (input) token count.
    pub prompt_tokens: u64,
    /// Completion (output) token count.
    pub completion_tokens: u64,
    /// Estimated USD cost for this call.
    pub cost_usd: f64,
    /// Provider identifier (e.g. `"anthropic"`, `"gemini"`).
    pub provider: String,
    /// Account email (may duplicate the outer `AccountLedger.account` field
    /// when ledger entries from multiple sub-accounts are merged).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub account: Option<String>,
}

/// All ledger entries for one account in a given period.
///
/// Returned by `cascade_usage_ledger`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountLedger {
    /// Account email address used as the query key.
    pub account: String,
    /// Ledger entries in ascending timestamp order.
    pub entries: Vec<LedgerEntry>,
}

// ── DailyUsageFile (internal deserialization target) ─────────────────────────

/// Schema of one `~/.cascade/usage/YYYY-MM-DD.json` file.
///
/// Written by the P2 daemon; read here for aggregation. Not exposed on the IPC
/// wire — only the derived [`UsageSummary`] / [`MonthlyUsage`] / [`AccountLedger`]
/// structs are.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DailyUsageFile {
    /// ISO-8601 date this file covers (`YYYY-MM-DD`, UTC).
    pub date: String,
    /// Completion events recorded on this day.
    #[serde(default)]
    pub entries: Vec<DailyEntry>,
}

/// One completion event inside a [`DailyUsageFile`].
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DailyEntry {
    /// ISO-8601 timestamp of the completion (UTC).
    pub timestamp: String,
    /// Account email.
    pub account: String,
    /// Model identifier.
    pub model: String,
    /// Provider identifier.
    pub provider: String,
    /// Prompt token count.
    pub prompt_tokens: u64,
    /// Completion token count.
    pub completion_tokens: u64,
    /// Estimated USD cost (0.0 when unknown).
    pub cost_usd: f64,
}
