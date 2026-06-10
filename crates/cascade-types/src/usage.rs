//! Purpose: Response types for the GET /api/personal/usage-history daemon route.
//!   Provides the wire-format structs that the usage_history handler serialises to
//!   JSON and the dashboard React component deserialises for charting.
//! Inputs:  Constructed in `cascade-daemon::http::usage_history` from aggregated
//!   `QuotaStore::rolling_history` snapshots.
//! Outputs: Serialised JSON over HTTP; deserialisable by TypeScript/React clients.
//! Constraints: All date strings are "YYYY-MM-DD" UTC; cost is f32 (sufficient for
//!   display precision). Empty slices are valid and expected on fresh installs.
//! SPORT: MASTER-TABLES.md — cascade-types crate (T-P3-E02-23)

use serde::{Deserialize, Serialize};

// ── per-day aggregated totals ─────────────────────────────────────────────────

/// Token/cost totals for a single UTC calendar day (all accounts combined).
///
/// Inputs:  aggregated from [`cascade_types::HistoryEntry`] slices.
/// Outputs: field of [`UsageHistoryResponse::daily_totals`].
/// Constraints: `date` is "YYYY-MM-DD" UTC; never `NaN` in cost fields.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DailyTotal {
    /// UTC date in "YYYY-MM-DD" format.
    pub date: String,
    /// Sum of input tokens for this day across all accounts.
    pub tokens_in: u64,
    /// Sum of output tokens for this day across all accounts.
    pub tokens_out: u64,
    /// Estimated cost in USD for this day (display precision only).
    pub cost_usd: f32,
}

// ── per-account breakdown ─────────────────────────────────────────────────────

/// Per-account usage broken down by day, aligned to the same date range as
/// [`UsageHistoryResponse::daily_totals`].
///
/// Inputs:  constructed per `account_id` from `rolling_history` snapshots.
/// Outputs: element of [`UsageHistoryResponse::per_account`].
/// Constraints: `totals_by_day` has the same length as `daily_totals` and is
///   ordered oldest-to-newest. Missing days have zeroed token/cost fields.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AccountUsage {
    /// Stable account identifier (e.g. `"cc-acct1"`).
    pub account_id: String,
    /// Human-readable label (harness + account_id, e.g. `"cc / cc-acct1"`).
    pub label: String,
    /// Per-day totals aligned to the global `daily_totals` date range.
    pub totals_by_day: Vec<DailyTotal>,
}

// ── summary ───────────────────────────────────────────────────────────────────

/// Period-wide aggregate across all accounts and all days in the requested range.
///
/// Inputs:  summed from [`DailyTotal`] entries.
/// Outputs: field of [`UsageHistoryResponse::summary`].
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct UsageSummary {
    /// Total input tokens across all accounts and all days.
    pub total_tokens_in: u64,
    /// Total output tokens across all accounts and all days.
    pub total_tokens_out: u64,
    /// Total estimated cost in USD for the period.
    pub total_cost_usd: f32,
    /// The number of days in the requested range (mirrors `date_range_days`).
    pub date_range_days: u32,
}

// ── top-level response ────────────────────────────────────────────────────────

/// Wire-format response for GET /api/personal/usage-history.
///
/// Inputs:  built by `cascade-daemon::http::usage_history::handler_usage_history`.
/// Outputs: serialised to JSON with `axum::Json`.
/// Constraints: All slices may be empty on a fresh install; this is not an error.
///   `date_range_days` reflects the validated `?days` query parameter value.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct UsageHistoryResponse {
    /// Combined daily totals across all accounts, ordered oldest-to-newest.
    pub daily_totals: Vec<DailyTotal>,
    /// Per-account breakdown, each with `totals_by_day` aligned to `daily_totals`.
    pub per_account: Vec<AccountUsage>,
    /// Period-wide summary statistics.
    pub summary: UsageSummary,
    /// The number of days this response covers (1–90).
    pub date_range_days: u32,
}
