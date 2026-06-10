//! # commands::usage
//!
//! Tauri IPC commands bridging the React analytics views to the daemon's
//! `ipc_usage_analytics` handlers (T-P3-E04-29 / T-P3-E04-30).
//!
//! ## Commands
//! | Tauri command               | Daemon method               |
//! |-----------------------------|------------------------------|
//! | `cascade_usage_summary`     | `cascade_usage_summary`      |
//! | `cascade_usage_history`     | `cascade_usage_history`      |
//! | `cascade_usage_ledger`      | `cascade_usage_ledger`       |
//!
//! ## Design note
//! Commands follow the same `daemon_call` pattern as `commands/providers.rs`.
//! Wire types mirror `cascade_types::usage_analytics` (camelCase serde).
//!
//! SPORT: MASTER-COMMANDS.md — usage analytics Tauri commands (T-P3-E04-30)

use serde::{Deserialize, Serialize};
use tauri::State;
use tracing::debug;

use crate::error::CascadeError;
use crate::state::AppState;

// ── Wire types (mirror cascade_types::usage_analytics, camelCase) ─────────────

/// Granularity of a usage period.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum UsagePeriodKind {
    Day,
    Week,
    Month,
}

/// Selects which calendar window a summary covers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UsagePeriod {
    pub kind: UsagePeriodKind,
    /// 0 = current window; negative = steps back.
    pub offset: i32,
}

/// Per-model usage stat.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelUsageStat {
    pub model: String,
    pub tokens: u64,
    pub cost_usd: f64,
}

/// Per-account usage stat.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountUsageStat {
    pub email: String,
    pub tokens: u64,
    pub cost_usd: f64,
}

/// Aggregated summary for a period.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UsageSummary {
    pub total_tokens: u64,
    pub total_cost_usd: f64,
    pub by_model: Vec<ModelUsageStat>,
    pub by_account: Vec<AccountUsageStat>,
    pub period_label: String,
    pub period_start: String,
    pub period_end: String,
}

/// One month in the usage history.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MonthlyUsage {
    pub month_label: String,
    pub tokens: u64,
    pub cost_usd: f64,
    pub by_model: Vec<ModelUsageStat>,
}

/// A single completion event in the per-account ledger.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LedgerEntry {
    pub timestamp: String,
    pub model: String,
    pub prompt_tokens: u64,
    pub completion_tokens: u64,
    pub cost_usd: f64,
    pub provider: String,
    pub account: Option<String>,
}

/// Full ledger for one account in a given period.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountLedger {
    pub account: String,
    pub entries: Vec<LedgerEntry>,
}

// ── IPC helper (same pattern as providers.rs) ─────────────────────────────────

async fn daemon_call<P, R>(method: &str, params: P) -> Result<R, CascadeError>
where
    P: Serialize,
    R: for<'de> Deserialize<'de>,
{
    use cascade_cli::ipc_client::IpcClient;

    let client = IpcClient::new().map_err(CascadeError::from)?;
    client
        .send::<P, R>(method, params)
        .await
        .map_err(CascadeError::from)
}

// ── T-P3-E04-30 commands ──────────────────────────────────────────────────────

/// Return aggregated usage for a calendar period (day / week / month).
///
/// Backed by the 60-second in-memory cache in the daemon.
/// JS: `invoke("cascade_usage_summary", { period: { kind, offset } })`
#[tauri::command]
pub async fn cascade_usage_summary(
    _state: State<'_, AppState>,
    period: UsagePeriod,
) -> Result<UsageSummary, CascadeError> {
    debug!(?period.kind, period.offset, "cascade_usage_summary invoked");
    daemon_call::<_, UsageSummary>("cascade_usage_summary", period).await
}

/// Return token/cost totals for the last N months, oldest first.
///
/// JS: `invoke("cascade_usage_history", { monthsBack: 6 })`
#[tauri::command]
pub async fn cascade_usage_history(
    _state: State<'_, AppState>,
    months_back: u32,
) -> Result<Vec<MonthlyUsage>, CascadeError> {
    debug!(months_back, "cascade_usage_history invoked");
    daemon_call::<_, Vec<MonthlyUsage>>(
        "cascade_usage_history",
        serde_json::json!({ "monthsBack": months_back }),
    )
    .await
}

/// Return all per-completion ledger entries for one account in a period.
///
/// JS: `invoke("cascade_usage_ledger", { accountEmail, period })`
#[tauri::command]
pub async fn cascade_usage_ledger(
    _state: State<'_, AppState>,
    account_email: String,
    period: UsagePeriod,
) -> Result<AccountLedger, CascadeError> {
    debug!(%account_email, ?period.kind, "cascade_usage_ledger invoked");
    daemon_call::<_, AccountLedger>(
        "cascade_usage_ledger",
        serde_json::json!({
            "accountEmail": account_email,
            "period": period,
        }),
    )
    .await
}
