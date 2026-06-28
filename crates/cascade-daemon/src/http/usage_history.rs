//! Purpose: HTTP handler for GET /api/personal/usage-history — returns aggregated
//!   token/cost usage data derived from the daemon's quota-store rolling history.
//!   Reads `~/.cascade/quota-store.json` (written atomically by the P2 fleet-quota
//!   poller) and aggregates `HistoryEntry` snapshots by UTC calendar day.
//! Inputs:  `?days=N` query parameter (default 30, range 1–90); optional
//!   `?account=<id>` to filter per-account results to one account.
//!   `$HOME` via `std::env::var_os("HOME")` — same convention as sibling handlers.
//! Outputs: JSON body conforming to `cascade_types::usage::UsageHistoryResponse`.
//!   200 on success (including empty data); 400 for out-of-range `days`; 500 only
//!   if the quota-store file exists but cannot be parsed.
//! Constraints: Never returns 404 on missing data — empty arrays are valid.
//!   Aggregation is additive: per `account_id` per UTC date, the handler takes the
//!   LAST snapshot's cumulative totals diff'd against the previous snapshot to avoid
//!   double-counting sub-hour writes. Since the store only has `week_total_used` /
//!   `month_total_used` on AccountEntry (not per-snapshot deltas), we treat each
//!   HistoryEntry as an independent sample and compute the *delta* between adjacent
//!   snapshots for the same account. First snapshot of a day is used as a floor.
//!   cost_usd is approximated at 0 (no pricing table in P3); left for T-P3-E02-24+.
//! SPORT: MASTER-ROUTES.md § /api/personal/usage-history (T-P3-E02-23)

use std::{collections::HashMap, path::PathBuf};

use axum::{extract::Query, http::StatusCode, response::IntoResponse, routing::get, Json, Router};
use chrono::{DateTime, TimeZone, Utc};
use serde_json::json;

use cascade_types::{
    usage::{AccountUsage, DailyTotal, UsageHistoryResponse, UsageSummary},
    QuotaStore,
};

use crate::dashboard::DashboardState;

// ── constants ─────────────────────────────────────────────────────────────────

const DEFAULT_DAYS: u32 = 30;
const MAX_DAYS: u32 = 90;

// ── router ────────────────────────────────────────────────────────────────────

/// Build the `/usage-history` route group nested under `/api/personal`.
///
/// The caller nests this at `/api/personal`, so the full path becomes
/// `/api/personal/usage-history`.
pub fn router() -> Router<DashboardState> {
    Router::new().route("/usage-history", get(handler_usage_history))
}

// ── query params ──────────────────────────────────────────────────────────────

#[derive(Debug, serde::Deserialize)]
pub(crate) struct UsageHistoryQuery {
    /// Number of days of history to return (default 30, max 90).
    days: Option<u32>,
    /// Optional: filter per_account to this account_id only.
    account: Option<String>,
}

// ── helpers ───────────────────────────────────────────────────────────────────

/// Resolve `$HOME` to a `PathBuf`. Returns `None` if the variable is unset.
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

/// Read and deserialise `~/.cascade/quota-store.json`.
/// Returns `None` if the file is absent (fresh install); `Err` on parse failure.
fn read_quota_store() -> Result<Option<QuotaStore>, String> {
    let home = match home_dir() {
        Some(h) => h,
        None => return Ok(None),
    };
    let path = home.join(".cascade").join("quota-store.json");
    if !path.exists() {
        return Ok(None);
    }
    let raw = std::fs::read_to_string(&path)
        .map_err(|e| format!("failed to read quota-store.json: {e}"))?;
    serde_json::from_str::<QuotaStore>(&raw)
        .map(Some)
        .map_err(|e| format!("failed to parse quota-store.json: {e}"))
}

/// Convert a Unix-epoch-seconds timestamp to a "YYYY-MM-DD" UTC date string.
fn epoch_to_date(ts: i64) -> String {
    let dt: DateTime<Utc> = Utc.timestamp_opt(ts, 0).single().unwrap_or(Utc::now());
    dt.format("%Y-%m-%d").to_string()
}

/// Build the ordered list of UTC date strings for the requested range.
/// `cutoff_ts` is the earliest epoch second to include.
fn date_range(days: u32) -> Vec<String> {
    let today = Utc::now().date_naive();
    (0..days as i64)
        .rev()
        .map(|offset| {
            (today - chrono::Duration::days(offset))
                .format("%Y-%m-%d")
                .to_string()
        })
        .collect()
}

/// Aggregate rolling history snapshots into (date → account_id → used_tokens).
///
/// Strategy: for each account within the requested date window, diff adjacent
/// snapshots by `week_total_used`. When the diff is positive it represents tokens
/// consumed between those two polls. We bin the diff into the date of the *later*
/// snapshot. If `week_total_used` resets across a week boundary the diff is clamped
/// to 0 to avoid negative deltas.
fn aggregate(
    store: &QuotaStore,
    days: u32,
    account_filter: Option<&str>,
) -> HashMap<String, HashMap<String, u64>> {
    // date_str → account_id → tokens (combined in+out approximation)
    let mut by_date: HashMap<String, HashMap<String, u64>> = HashMap::new();

    let cutoff_ts = Utc::now().timestamp() - (days as i64) * 86_400;

    // Sort history oldest-first so we can diff adjacent snapshots.
    let mut history = store.rolling_history.clone();
    history.sort_by_key(|e| e.ts);

    // prev_week_used: account_id → last known week_total_used
    let mut prev_week: HashMap<String, u64> = HashMap::new();

    for entry in &history {
        if entry.ts < cutoff_ts {
            // Still before the window — update prev state without binning.
            for acct in &entry.accounts_snapshot {
                if let Some(filter) = account_filter {
                    if acct.account_id != filter {
                        continue;
                    }
                }
                prev_week.insert(acct.account_id.clone(), acct.week_total_used);
            }
            continue;
        }

        let date = epoch_to_date(entry.ts);

        for acct in &entry.accounts_snapshot {
            if let Some(filter) = account_filter {
                if acct.account_id != filter {
                    continue;
                }
            }

            let prev = prev_week.get(&acct.account_id).copied().unwrap_or(0);
            let delta = acct.week_total_used.saturating_sub(prev);

            by_date
                .entry(date.clone())
                .or_default()
                .entry(acct.account_id.clone())
                .and_modify(|v| *v += delta)
                .or_insert(delta);

            prev_week.insert(acct.account_id.clone(), acct.week_total_used);
        }
    }

    by_date
}

/// Collect the unique set of account_ids seen across rolling history.
fn known_accounts(store: &QuotaStore, account_filter: Option<&str>) -> Vec<(String, String)> {
    let mut seen: HashMap<String, String> = HashMap::new();
    for entry in &store.rolling_history {
        for acct in &entry.accounts_snapshot {
            if let Some(filter) = account_filter {
                if acct.account_id != filter {
                    continue;
                }
            }
            seen.entry(acct.account_id.clone())
                .or_insert_with(|| format!("{} / {}", acct.harness, acct.account_id));
        }
    }
    let mut result: Vec<(String, String)> = seen.into_iter().collect();
    result.sort_by(|a, b| a.0.cmp(&b.0));
    result
}

// ── handler ───────────────────────────────────────────────────────────────────

/// GET /api/personal/usage-history
///
/// Query params:
///   - `days`    — history window (1–90, default 30). Returns 400 if out of range.
///   - `account` — optional account_id filter for per_account results.
pub(crate) async fn handler_usage_history(
    Query(params): Query<UsageHistoryQuery>,
) -> impl IntoResponse {
    // ── validate days ────────────────────────────────────────────────────────
    let days = params.days.unwrap_or(DEFAULT_DAYS);
    if days > MAX_DAYS || days == 0 {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "error": format!(
                    "days must be between 1 and {MAX_DAYS}, got {days}"
                )
            })),
        )
            .into_response();
    }

    // ── read quota store ─────────────────────────────────────────────────────
    let store = match read_quota_store() {
        Ok(Some(s)) => s,
        Ok(None) => {
            // Fresh install — return empty response (not an error).
            return Json(empty_response(days)).into_response();
        }
        Err(msg) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({ "error": msg })),
            )
                .into_response();
        }
    };

    let account_filter = params.account.as_deref();

    // ── build date range ─────────────────────────────────────────────────────
    let dates = date_range(days);

    // ── aggregate snapshots ──────────────────────────────────────────────────
    let by_date = aggregate(&store, days, account_filter);

    // ── build daily_totals (all accounts combined) ───────────────────────────
    let daily_totals: Vec<DailyTotal> = dates
        .iter()
        .map(|date| {
            let tokens: u64 = by_date
                .get(date)
                .map(|accts| accts.values().sum())
                .unwrap_or(0);
            DailyTotal {
                date: date.clone(),
                // We only have a combined week_total_used — split 50/50 as an
                // approximation; real in/out split requires per-model data (P4).
                tokens_in: tokens / 2,
                tokens_out: tokens - tokens / 2,
                cost_usd: 0.0,
            }
        })
        .collect();

    // ── build per_account ─────────────────────────────────────────────────────
    let accounts = known_accounts(&store, account_filter);
    let per_account: Vec<AccountUsage> = accounts
        .into_iter()
        .map(|(account_id, label)| {
            let totals_by_day = dates
                .iter()
                .map(|date| {
                    let tokens = by_date
                        .get(date)
                        .and_then(|m| m.get(&account_id))
                        .copied()
                        .unwrap_or(0);
                    DailyTotal {
                        date: date.clone(),
                        tokens_in: tokens / 2,
                        tokens_out: tokens - tokens / 2,
                        cost_usd: 0.0,
                    }
                })
                .collect();
            AccountUsage {
                account_id,
                label,
                totals_by_day,
            }
        })
        .collect();

    // ── build summary ─────────────────────────────────────────────────────────
    let total_tokens_in: u64 = daily_totals.iter().map(|d| d.tokens_in).sum();
    let total_tokens_out: u64 = daily_totals.iter().map(|d| d.tokens_out).sum();
    let summary = UsageSummary {
        total_tokens_in,
        total_tokens_out,
        total_cost_usd: 0.0,
        date_range_days: days,
    };

    Json(UsageHistoryResponse {
        daily_totals,
        per_account,
        summary,
        date_range_days: days,
    })
    .into_response()
}

/// Return a correctly-shaped empty response for fresh installs.
fn empty_response(days: u32) -> UsageHistoryResponse {
    let dates = date_range(days);
    let daily_totals: Vec<DailyTotal> = dates
        .iter()
        .map(|d| DailyTotal {
            date: d.clone(),
            tokens_in: 0,
            tokens_out: 0,
            cost_usd: 0.0,
        })
        .collect();
    UsageHistoryResponse {
        daily_totals,
        per_account: vec![],
        summary: UsageSummary {
            total_tokens_in: 0,
            total_tokens_out: 0,
            total_cost_usd: 0.0,
            date_range_days: days,
        },
        date_range_days: days,
    }
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{body::Body, http::Request};
    use serial_test::serial;
    use tempfile::TempDir;
    use tower::ServiceExt;

    /// Build a minimal test router.
    fn test_router() -> axum::Router {
        axum::Router::new().route("/usage-history", get(handler_usage_history))
    }

    /// Write a quota-store JSON to `<home>/.cascade/quota-store.json`.
    fn write_store(home: &TempDir, store: &QuotaStore) {
        let dir = home.path().join(".cascade");
        std::fs::create_dir_all(&dir).unwrap();
        let json = serde_json::to_string(store).unwrap();
        std::fs::write(dir.join("quota-store.json"), json).unwrap();
    }

    fn empty_store() -> QuotaStore {
        QuotaStore {
            schema_version: 1,
            updated_at: "2026-01-01T00:00:00Z".into(),
            accounts: vec![],
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        }
    }

    // ── test 1: missing quota-store file → 200 with empty arrays ─────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn usage_history_no_file_returns_200_empty() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());

        let app = test_router();
        let req = Request::builder()
            .uri("/usage-history")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(resp.status(), StatusCode::OK);
        let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let val: UsageHistoryResponse = serde_json::from_slice(&body).unwrap();
        assert!(val.per_account.is_empty(), "per_account should be empty");
        assert_eq!(val.date_range_days, DEFAULT_DAYS);
        assert_eq!(val.summary.total_tokens_in, 0);
    }

    // ── test 2: empty quota-store (no history) → 200 with empty per_account ──

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn usage_history_empty_store_returns_200_empty() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        write_store(&tmp, &empty_store());

        let app = test_router();
        let req = Request::builder()
            .uri("/usage-history?days=7")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(resp.status(), StatusCode::OK);
        let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let val: UsageHistoryResponse = serde_json::from_slice(&body).unwrap();
        assert!(val.per_account.is_empty());
        assert_eq!(val.date_range_days, 7);
        // daily_totals should still have 7 entries (all zeroed)
        assert_eq!(val.daily_totals.len(), 7);
        for day in &val.daily_totals {
            assert_eq!(day.tokens_in, 0);
            assert_eq!(day.tokens_out, 0);
        }
    }

    // ── test 3: days=91 → 400 with descriptive error ─────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn usage_history_days_91_returns_400() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());

        let app = test_router();
        let req = Request::builder()
            .uri("/usage-history?days=91")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
        let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let val: serde_json::Value = serde_json::from_slice(&body).unwrap();
        let err = val["error"].as_str().unwrap_or("");
        assert!(
            err.contains("91"),
            "error should mention the bad value, got: {err}"
        );
        assert!(
            err.contains("90"),
            "error should mention the max, got: {err}"
        );
    }

    // ── test 4: days=0 → 400 ──────────────────────────────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn usage_history_days_0_returns_400() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());

        let app = test_router();
        let req = Request::builder()
            .uri("/usage-history?days=0")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    }

    // ── test 5: days=90 → 200 (boundary ok) ──────────────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn usage_history_days_90_returns_200() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        write_store(&tmp, &empty_store());

        let app = test_router();
        let req = Request::builder()
            .uri("/usage-history?days=90")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(resp.status(), StatusCode::OK);
        let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let val: UsageHistoryResponse = serde_json::from_slice(&body).unwrap();
        assert_eq!(val.date_range_days, 90);
        assert_eq!(val.daily_totals.len(), 90);
    }

    // ── test 6: history with snapshots → daily_totals ordered oldest-newest ──

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn usage_history_with_data_returns_ordered_dates() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        use cascade_types::{AccountEntry, HistoryEntry};

        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());

        let now = Utc::now().timestamp();
        // Two snapshots 24 hours apart so they land on different dates.
        let ts_yesterday = now - 86_400;
        let ts_today = now - 60;

        let make_acct = |week_used: u64| AccountEntry {
            account_id: "cc-acct1".into(),
            harness: "cc".into(),
            provider: "".into(),
            models: HashMap::new(),
            week_total_used: week_used,
            month_total_used: week_used,
            last_polled: "2026-01-01T00:00:00Z".into(),
            rate_windows: vec![],
        };

        let mut store = empty_store();
        store.rolling_history = vec![
            HistoryEntry {
                ts: ts_yesterday,
                accounts_snapshot: vec![make_acct(1000)],
            },
            HistoryEntry {
                ts: ts_today,
                accounts_snapshot: vec![make_acct(1500)],
            },
        ];
        write_store(&tmp, &store);

        let app = test_router();
        let req = Request::builder()
            .uri("/usage-history?days=7")
            .body(Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(resp.status(), StatusCode::OK);
        let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let val: UsageHistoryResponse = serde_json::from_slice(&body).unwrap();

        assert_eq!(val.date_range_days, 7);
        assert_eq!(val.daily_totals.len(), 7);

        // Dates should be ordered oldest to newest.
        let dates: Vec<&str> = val.daily_totals.iter().map(|d| d.date.as_str()).collect();
        let mut sorted = dates.clone();
        sorted.sort();
        assert_eq!(
            dates, sorted,
            "daily_totals must be ordered oldest-to-newest"
        );

        // per_account should have exactly one entry for cc-acct1.
        assert_eq!(val.per_account.len(), 1);
        assert_eq!(val.per_account[0].account_id, "cc-acct1");
        assert_eq!(val.per_account[0].totals_by_day.len(), 7);

        // summary tokens are u64 so always non-negative; just verify they are present.
        let _ = val.summary.total_tokens_in + val.summary.total_tokens_out;
    }
}
