//! Quota aggregation — merge ordered poll snapshots into a [`QuotaStore`].
//!
//! Purpose: [`aggregate_quota`] is a **pure function** with no I/O.  It
//! accepts an ordered (oldest→newest) slice of [`QuotaState`] snapshots from
//! one or more accounts and produces the authoritative [`QuotaStore`] schema:
//!
//! - Per-account [`AccountEntry`] with current model usage, week/month totals,
//!   and per-source rate windows (5hr rolling, weekly, monthly, per-key-daily).
//! - Cross-account [`QuotaStore::week_totals`] / [`QuotaStore::month_totals`]
//!   per model.
//! - A trimmed [`QuotaStore::rolling_history`] capped at `max_history` entries.
//!
//! This module is reused by both the daemon poller (writes the store to disk)
//! and any future CLI re-aggregation command.
//!
//! Inputs:  `snapshots` — ordered oldest→newest [`QuotaState`] slice;
//!          `max_history` — max [`HistoryEntry`] records (0 = keep all).
//! Outputs: populated [`QuotaStore`] ready for atomic file write.
//! Constraints:
//!   - No `std::fs`, no `tokio::fs`, no network I/O — purely in-memory.
//!   - Week window: ISO week (Monday 00:00:00 UTC → Sunday 23:59:59 UTC)
//!     containing the latest snapshot's `ts`.
//!   - Month window: calendar month (1st 00:00:00 UTC → last day) containing
//!     the latest snapshot's `ts`.
//!   - 5hr window: rolling `ref_ts - 18000s` (not clock-aligned).
//!   - `Utc::now()` is called **only** for [`QuotaStore::updated_at`]; all
//!     window arithmetic uses snapshot `ts` values exclusively.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — aggregate_quota function (T-P2-E02-30)
//!        build_rate_windows helper (E-P6-02 T-02)

use std::collections::HashMap;

use cascade_types::quota_store::{
    AccountEntry, HistoryEntry, QuotaState, QuotaStore, RateWindow, PROVIDER_CLAUDE_MAX,
    PROVIDER_GFP, PROVIDER_GOOGLE_AGY, PROVIDER_OC_GO, PROVIDER_OPENAI_CODEX,
    QUOTA_STORE_SCHEMA_VERSION,
};
use chrono::{DateTime, Datelike, TimeZone, Utc};

// Window durations in seconds.
const FIVE_HOUR_SECS: u64 = 5 * 3_600; // 18 000 s
const WEEK_SECS: u64 = 7 * 24 * 3_600; // 604 800 s
const DAY_SECS: u64 = 24 * 3_600; // 86 400 s

// ── Public API ────────────────────────────────────────────────────────────────

/// Merge an ordered slice of raw [`QuotaState`] poll snapshots into an
/// aggregated [`QuotaStore`].
///
/// Inputs:
///   - `snapshots` — ordered oldest→newest poll results (may span multiple
///     accounts and multiple polling cycles).
///   - `max_history` — maximum number of [`HistoryEntry`] records retained in
///     [`QuotaStore::rolling_history`].  `0` means keep all.
///
/// Outputs: a fully populated [`QuotaStore`] with:
///   - `accounts` — one [`AccountEntry`] per distinct `account_id`, driven by
///     the **latest** snapshot for that account.
///   - `week_totals` / `month_totals` — cross-account per-model sums for the
///     current week/month window (relative to the latest snapshot's `ts`).
///   - `rolling_history` — one [`HistoryEntry`] per snapshot, trimmed to
///     `max_history` newest entries.
///
/// Constraints: pure function — no I/O, no wall-clock reads inside window
/// arithmetic (only `updated_at` uses `Utc::now()`).
pub fn aggregate_quota(snapshots: &[QuotaState], max_history: usize) -> QuotaStore {
    // Determine the reference timestamp for window calculations from the latest
    // snapshot.  If the slice is empty we fall back to Utc::now(), which is
    // safe because there is nothing to aggregate anyway.
    let latest_ts = snapshots
        .last()
        .map(|s| s.ts)
        .unwrap_or_else(|| Utc::now().timestamp());
    let ref_dt: DateTime<Utc> = Utc
        .timestamp_opt(latest_ts, 0)
        .single()
        .unwrap_or_else(Utc::now);

    let week_start = iso_week_start_utc(ref_dt);
    let month_start = month_start_utc(ref_dt);

    // ── Per-account aggregation ───────────────────────────────────────────────
    // Collect: for each account_id → latest snapshot (for current model state)
    // and the week/month totals computed from all snapshots in the window.
    let accounts = build_accounts(snapshots, week_start, month_start, latest_ts);

    // ── Cross-account week/month totals ───────────────────────────────────────
    let (week_totals, month_totals) =
        build_cross_account_totals(snapshots, week_start, month_start);

    // ── Rolling history ───────────────────────────────────────────────────────
    let rolling_history = build_history(snapshots, max_history);

    QuotaStore {
        schema_version: QUOTA_STORE_SCHEMA_VERSION,
        updated_at: Utc::now().to_rfc3339(),
        accounts,
        week_totals,
        month_totals,
        rolling_history,
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

/// Build one [`AccountEntry`] per distinct `account_id`.
///
/// For each account:
/// - `models` / `last_polled` come from the **latest** snapshot for that account.
/// - `week_total_used` / `month_total_used` are the sum of `used` across all
///   models from every snapshot whose `ts` falls inside the current window.
/// - `rate_windows` is populated via [`build_rate_windows`] using the provider
///   from the latest snapshot.
fn build_accounts(
    snapshots: &[QuotaState],
    week_start: DateTime<Utc>,
    month_start: DateTime<Utc>,
    ref_ts: i64,
) -> Vec<AccountEntry> {
    // Gather all distinct account_ids (preserving first-seen insertion order).
    let mut order: Vec<String> = Vec::new();
    for snap in snapshots {
        if !order.contains(&snap.account_id) {
            order.push(snap.account_id.clone());
        }
    }

    let mut entries: Vec<AccountEntry> = Vec::with_capacity(order.len());
    for account_id in &order {
        let account_snaps: Vec<&QuotaState> = snapshots
            .iter()
            .filter(|s| &s.account_id == account_id)
            .collect();

        // Latest snapshot drives the displayed model state and last_polled.
        let latest = account_snaps
            .last()
            .copied()
            .expect("account has at least one snapshot");

        let week_total_used = sum_used_in_window(&account_snaps, week_start);
        let month_total_used = sum_used_in_window(&account_snaps, month_start);
        let rate_windows = build_rate_windows(&account_snaps, &latest.provider, ref_ts);

        entries.push(AccountEntry {
            account_id: account_id.clone(),
            harness: latest.harness.clone(),
            provider: latest.provider.clone(),
            models: latest.models.clone(),
            week_total_used,
            month_total_used,
            last_polled: Utc
                .timestamp_opt(latest.ts, 0)
                .single()
                .map(|dt| dt.to_rfc3339())
                .unwrap_or_default(),
            rate_windows,
        });
    }
    entries
}

/// Build per-source rate windows for one account.
///
/// Purpose: compute rolling (5hr) and calendar (weekly, monthly) window usage
/// for each provider type. Dollar-metered providers (OC-Go) sum `cost_usd`
/// instead of token counts. GFP uses per-day request counts.
///
/// Inputs:
///   - `snaps` — all snapshots for a single account (ordered oldest→newest).
///   - `provider` — one of the `PROVIDER_*` constants.
///   - `ref_ts` — Unix epoch seconds for the reference "now" (latest snapshot ts).
///
/// Outputs: a `Vec<RateWindow>` containing windows appropriate for the provider.
/// Constraints: pure — no I/O, no wall-clock reads; all window boundaries
///              derived from `ref_ts`.
pub fn build_rate_windows(snaps: &[&QuotaState], provider: &str, ref_ts: i64) -> Vec<RateWindow> {
    let five_hour_start = ref_ts - FIVE_HOUR_SECS as i64;
    let week_start_ts = ref_ts - WEEK_SECS as i64;
    let day_start_ts = ref_ts - DAY_SECS as i64;

    match provider {
        PROVIDER_CLAUDE_MAX => {
            // 5hr rolling + ISO-weekly window (token-based).
            let five_hr_used = sum_used_since(snaps, five_hour_start);
            let weekly_used = sum_used_since(snaps, week_start_ts);
            vec![
                RateWindow {
                    window_secs: FIVE_HOUR_SECS,
                    label: "5hr".to_string(),
                    used: five_hr_used,
                    limit: None,
                    reset_at: None,
                    cost_usd: None,
                },
                RateWindow {
                    window_secs: WEEK_SECS,
                    label: "weekly".to_string(),
                    used: weekly_used,
                    limit: None,
                    reset_at: None,
                    cost_usd: None,
                },
            ]
        }
        PROVIDER_OPENAI_CODEX => {
            // 5hr rolling only (token-based).
            let five_hr_used = sum_used_since(snaps, five_hour_start);
            vec![RateWindow {
                window_secs: FIVE_HOUR_SECS,
                label: "5hr".to_string(),
                used: five_hr_used,
                limit: None,
                reset_at: None,
                cost_usd: None,
            }]
        }
        PROVIDER_GOOGLE_AGY => {
            // 5hr rolling (token-based; Ultra tier multiplier is caller-side).
            let five_hr_used = sum_used_since(snaps, five_hour_start);
            vec![RateWindow {
                window_secs: FIVE_HOUR_SECS,
                label: "5hr".to_string(),
                used: five_hr_used,
                limit: None,
                reset_at: None,
                cost_usd: None,
            }]
        }
        PROVIDER_OC_GO => {
            // $/5hr rolling + $/month calendar (cost_usd-based, not token counts).
            let five_hr_cost = sum_cost_since(snaps, five_hour_start);
            let monthly_cost = sum_cost_since(snaps, month_start_of(ref_ts));
            vec![
                RateWindow {
                    window_secs: FIVE_HOUR_SECS,
                    label: "5hr".to_string(),
                    used: 0,
                    limit: None,
                    reset_at: None,
                    cost_usd: Some(five_hr_cost),
                },
                RateWindow {
                    window_secs: 30 * DAY_SECS, // approximate 30-day month
                    label: "monthly".to_string(),
                    used: 0,
                    limit: None,
                    reset_at: None,
                    cost_usd: Some(monthly_cost),
                },
            ]
        }
        PROVIDER_GFP => {
            // Per-key request count over a rolling 24h window.
            let day_used = sum_used_since(snaps, day_start_ts);
            vec![RateWindow {
                window_secs: DAY_SECS,
                label: "daily".to_string(),
                used: day_used,
                limit: None,
                reset_at: None,
                cost_usd: None,
            }]
        }
        _ => {
            // Unknown provider — return a 5hr window as a best-effort default.
            let five_hr_used = sum_used_since(snaps, five_hour_start);
            vec![RateWindow {
                window_secs: FIVE_HOUR_SECS,
                label: "5hr".to_string(),
                used: five_hr_used,
                limit: None,
                reset_at: None,
                cost_usd: None,
            }]
        }
    }
}

/// Sum the `used` field of every model across all snapshots with `ts >= since_ts`.
///
/// Inputs:  `snaps` — snapshots for a single account;
///          `since_ts` — inclusive lower bound (Unix epoch seconds).
/// Outputs: total tokens used in the window.
fn sum_used_since(snaps: &[&QuotaState], since_ts: i64) -> u64 {
    snaps
        .iter()
        .filter(|s| s.ts >= since_ts)
        .map(|s| s.models.values().map(|m| m.used).sum::<u64>())
        .sum()
}

/// Sum the `cost_usd` field across all models in snapshots with `ts >= since_ts`.
///
/// Inputs:  `snaps` — snapshots for a single account;
///          `since_ts` — inclusive lower bound (Unix epoch seconds).
/// Outputs: total USD cost in the window (0.0 when no cost_usd is set).
fn sum_cost_since(snaps: &[&QuotaState], since_ts: i64) -> f64 {
    snaps
        .iter()
        .filter(|s| s.ts >= since_ts)
        .flat_map(|s| s.models.values())
        .map(|m| m.cost_usd.unwrap_or(0.0))
        .sum()
}

/// Return the Unix epoch seconds for midnight UTC on the 1st of the month
/// containing `ref_ts`.
///
/// Constraints: pure — no I/O, no Utc::now().
fn month_start_of(ref_ts: i64) -> i64 {
    let dt = Utc
        .timestamp_opt(ref_ts, 0)
        .single()
        .unwrap_or_else(Utc::now);
    month_start_utc(dt).timestamp()
}

/// Sum the `used` field of every model across all snapshots whose `ts` ≥ `window_start`.
///
/// Inputs:  `snaps` — snapshots for a single account (ordered oldest→newest);
///          `window_start` — inclusive lower bound for the window.
/// Outputs: total `used` tokens/requests within the window.
/// Constraints: each snapshot contributes the sum of all its model `used` values.
fn sum_used_in_window(snaps: &[&QuotaState], window_start: DateTime<Utc>) -> u64 {
    let window_start_ts = window_start.timestamp();
    snaps
        .iter()
        .filter(|s| s.ts >= window_start_ts)
        .map(|s| s.models.values().map(|m| m.used).sum::<u64>())
        .sum()
}

/// Build cross-account per-model totals for the current week and month.
///
/// Inputs:  all snapshots (any account); `week_start` / `month_start` bounds.
/// Outputs: two `HashMap<model_id, total_used>` — one for the week, one for the month.
fn build_cross_account_totals(
    snapshots: &[QuotaState],
    week_start: DateTime<Utc>,
    month_start: DateTime<Utc>,
) -> (HashMap<String, u64>, HashMap<String, u64>) {
    let week_start_ts = week_start.timestamp();
    let month_start_ts = month_start.timestamp();

    let mut week_totals: HashMap<String, u64> = HashMap::new();
    let mut month_totals: HashMap<String, u64> = HashMap::new();

    for snap in snapshots {
        for (model_id, usage) in &snap.models {
            if snap.ts >= week_start_ts {
                *week_totals.entry(model_id.clone()).or_insert(0) += usage.used;
            }
            if snap.ts >= month_start_ts {
                *month_totals.entry(model_id.clone()).or_insert(0) += usage.used;
            }
        }
    }

    (week_totals, month_totals)
}

/// Build the rolling history, trimmed to `max_history` newest entries.
///
/// Inputs:  `snapshots` — all poll snapshots (any account, oldest→newest);
///          `max_history` — cap (0 = keep all).
/// Outputs: each snapshot becomes one [`HistoryEntry`]; if `max_history > 0`
///          only the `max_history` newest are returned.
/// Constraints: pure transformation — no I/O.
fn build_history(snapshots: &[QuotaState], max_history: usize) -> Vec<HistoryEntry> {
    // Build one HistoryEntry per snapshot.  We store a single-element
    // accounts_snapshot containing just the account from this snapshot so that
    // the history faithfully records the raw poll data.
    let all: Vec<HistoryEntry> = snapshots
        .iter()
        .map(|s| {
            let entry = AccountEntry {
                account_id: s.account_id.clone(),
                harness: s.harness.clone(),
                provider: s.provider.clone(),
                models: s.models.clone(),
                week_total_used: 0, // raw snapshot; aggregation not needed in history
                month_total_used: 0,
                last_polled: Utc
                    .timestamp_opt(s.ts, 0)
                    .single()
                    .map(|dt| dt.to_rfc3339())
                    .unwrap_or_default(),
                rate_windows: vec![],
            };
            HistoryEntry {
                ts: s.ts,
                accounts_snapshot: vec![entry],
            }
        })
        .collect();

    if max_history == 0 || all.len() <= max_history {
        all
    } else {
        // Keep the `max_history` newest entries (the end of the vec).
        all.into_iter().rev().take(max_history).rev().collect()
    }
}

/// Return the Monday 00:00:00 UTC of the ISO week that contains `dt`.
///
/// Inputs:  `dt` — any UTC timestamp.
/// Outputs: start of that ISO week at midnight UTC.
/// Constraints: pure — no I/O, no Utc::now().
fn iso_week_start_utc(dt: DateTime<Utc>) -> DateTime<Utc> {
    // ISO weekday: Monday = 0, ..., Sunday = 6.
    let days_since_monday = dt.weekday().num_days_from_monday();
    let monday = dt.date_naive() - chrono::Duration::days(days_since_monday as i64);
    Utc.from_utc_datetime(&monday.and_hms_opt(0, 0, 0).expect("valid midnight"))
}

/// Return midnight UTC on the 1st of the calendar month containing `dt`.
///
/// Inputs:  `dt` — any UTC timestamp.
/// Outputs: first day of the month at 00:00:00 UTC.
/// Constraints: pure — no I/O, no Utc::now().
fn month_start_utc(dt: DateTime<Utc>) -> DateTime<Utc> {
    use chrono::NaiveDate;
    let first = NaiveDate::from_ymd_opt(dt.year(), dt.month(), 1).expect("valid date");
    Utc.from_utc_datetime(&first.and_hms_opt(0, 0, 0).expect("valid midnight"))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::quota_store::ModelUsage;
    use std::collections::HashMap;

    // ── Helpers ───────────────────────────────────────────────────────────────

    fn make_model_usage(used: u64) -> ModelUsage {
        ModelUsage {
            used,
            limit: Some(10_000),
            reset_at: Some("2026-07-01T00:00:00Z".to_string()),
            pct_used: Some((used as f32 / 10_000.0) * 100.0),
            cost_usd: None,
        }
    }

    fn make_model_usage_with_cost(used: u64, cost: f64) -> ModelUsage {
        ModelUsage {
            used,
            limit: None,
            reset_at: None,
            pct_used: None,
            cost_usd: Some(cost),
        }
    }

    fn snapshot(account_id: &str, harness: &str, ts: i64, model: &str, used: u64) -> QuotaState {
        snapshot_with_provider(account_id, harness, "", ts, model, used)
    }

    fn snapshot_with_provider(
        account_id: &str,
        harness: &str,
        provider: &str,
        ts: i64,
        model: &str,
        used: u64,
    ) -> QuotaState {
        let mut models = HashMap::new();
        models.insert(model.to_string(), make_model_usage(used));
        QuotaState {
            account_id: account_id.to_string(),
            harness: harness.to_string(),
            provider: provider.to_string(),
            ts,
            models,
        }
    }

    fn snapshot_with_cost(
        account_id: &str,
        provider: &str,
        ts: i64,
        model: &str,
        cost: f64,
    ) -> QuotaState {
        let mut models = HashMap::new();
        models.insert(model.to_string(), make_model_usage_with_cost(0, cost));
        QuotaState {
            account_id: account_id.to_string(),
            harness: "oc".to_string(),
            provider: provider.to_string(),
            ts,
            models,
        }
    }

    // ── single_account_one_snapshot ───────────────────────────────────────────

    /// One snapshot for one account must produce a correct AccountEntry, and
    /// the week/month totals must equal the single snapshot's model sum.
    #[test]
    fn single_account_one_snapshot() {
        // 2026-06-02T12:00:00Z — a Tuesday, well inside its week and month.
        let ts = 1_780_401_600_i64; // 2026-06-02 12:00:00 UTC
        let snaps = vec![snapshot("cc-acct1", "cc", ts, "claude-sonnet-4-6", 1_000)];

        let store = aggregate_quota(&snaps, 0);

        assert_eq!(store.schema_version, QUOTA_STORE_SCHEMA_VERSION);
        assert_eq!(store.accounts.len(), 1);

        let entry = &store.accounts[0];
        assert_eq!(entry.account_id, "cc-acct1");
        assert_eq!(entry.harness, "cc");
        assert_eq!(entry.models["claude-sonnet-4-6"].used, 1_000);
        assert_eq!(entry.week_total_used, 1_000, "week total should equal used");
        assert_eq!(
            entry.month_total_used, 1_000,
            "month total should equal used"
        );

        assert_eq!(store.week_totals["claude-sonnet-4-6"], 1_000);
        assert_eq!(store.month_totals["claude-sonnet-4-6"], 1_000);
        assert_eq!(store.rolling_history.len(), 1);
    }

    // ── multi_account_merge ───────────────────────────────────────────────────

    /// Two accounts with the same model — cross-account week_totals must sum both.
    #[test]
    fn multi_account_merge() {
        // Both snapshots at 2026-06-02T12:00:00Z (same week, same month).
        let ts = 1_780_401_600_i64;
        let snaps = vec![
            snapshot("cc-acct1", "cc", ts, "claude-sonnet-4-6", 1_000),
            snapshot("cc-acct2", "cc", ts, "claude-sonnet-4-6", 2_000),
        ];

        let store = aggregate_quota(&snaps, 0);

        assert_eq!(store.accounts.len(), 2);

        // Cross-account week_totals for the model must be the sum of both accounts.
        assert_eq!(
            store.week_totals["claude-sonnet-4-6"], 3_000,
            "week_totals should sum both accounts"
        );
        assert_eq!(
            store.month_totals["claude-sonnet-4-6"], 3_000,
            "month_totals should sum both accounts"
        );

        // Per-account totals.
        let acct1 = store
            .accounts
            .iter()
            .find(|a| a.account_id == "cc-acct1")
            .unwrap();
        let acct2 = store
            .accounts
            .iter()
            .find(|a| a.account_id == "cc-acct2")
            .unwrap();
        assert_eq!(acct1.week_total_used, 1_000);
        assert_eq!(acct2.week_total_used, 2_000);
    }

    // ── week_window_boundary ──────────────────────────────────────────────────

    /// A snapshot exactly at (or just after) the Monday 00:00:00 UTC week-start
    /// must be **included**; a snapshot one second before must be **excluded**.
    ///
    /// Uses fixed timestamps — no Utc::now() so the test is never flaky.
    ///
    /// Reference week: 2026-06-01 (Monday) 00:00:00 UTC = 1_780_272_000.
    #[test]
    fn week_window_boundary() {
        // Mon 2026-06-01 00:00:00 UTC = 1_780_272_000 (confirmed weekday=0)
        let monday_midnight: i64 = 1_780_272_000;
        let before_week: i64 = monday_midnight - 1; // Sun 2026-05-31 23:59:59 UTC — EXCLUDED
        let at_week_start: i64 = monday_midnight; // Mon 2026-06-01 00:00:00 UTC — INCLUDED

        // Reference snapshot is one hour later in the same week.
        let ref_ts: i64 = monday_midnight + 3_600; // Mon 2026-06-01 01:00:00 UTC — INCLUDED

        let snaps = vec![
            snapshot("acct1", "cc", before_week, "model-a", 500), // excluded from week
            snapshot("acct1", "cc", at_week_start, "model-a", 300), // included
            snapshot("acct1", "cc", ref_ts, "model-a", 200),      // included (latest)
        ];

        let store = aggregate_quota(&snaps, 0);

        let entry = &store.accounts[0];
        // at_week_start (300) + ref_ts (200) = 500; before_week (500) excluded.
        assert_eq!(
            entry.week_total_used, 500,
            "week total should include at_week_start (300) and ref_ts (200) = 500"
        );

        // Cross-account week_totals must also exclude the before_week snapshot.
        assert_eq!(
            store.week_totals["model-a"], 500,
            "cross-account week total should be 500"
        );
    }

    // ── month_rollover ────────────────────────────────────────────────────────

    /// Two snapshots in different months: only the snapshot in the current month
    /// (the month containing the latest ts) contributes to month_total_used.
    #[test]
    fn month_rollover() {
        // 2026-05-31T23:59:59Z (May) = 1_780_271_999
        let may_ts: i64 = 1_780_271_999;
        // 2026-06-01T00:00:00Z (June) = 1_780_272_000  ← latest → current month = June
        let june_ts: i64 = 1_780_272_000;

        let snaps = vec![
            snapshot("acct1", "cc", may_ts, "model-b", 800), // May — excluded from month
            snapshot("acct1", "cc", june_ts, "model-b", 200), // June — included
        ];

        let store = aggregate_quota(&snaps, 0);

        let entry = &store.accounts[0];
        assert_eq!(
            entry.month_total_used, 200,
            "only the June snapshot should count toward the current month"
        );
        assert_eq!(
            store.month_totals["model-b"], 200,
            "cross-account month total should be 200"
        );
    }

    // ── history_trim ──────────────────────────────────────────────────────────

    /// With 15 input snapshots and max_history=10, rolling_history must contain
    /// exactly 10 entries (the 10 newest).
    #[test]
    fn history_trim() {
        let base_ts: i64 = 1_780_394_400; // 2026-06-02T10:00:00Z
        let snaps: Vec<QuotaState> = (0..15)
            .map(|i| snapshot("acct1", "cc", base_ts + i * 60, "model-c", 100))
            .collect();

        let store = aggregate_quota(&snaps, 10);

        assert_eq!(
            store.rolling_history.len(),
            10,
            "history should be trimmed to max_history=10"
        );

        // The 10 entries must be the 10 newest (ts >= base_ts + 5*60).
        let oldest_kept = store.rolling_history[0].ts;
        assert_eq!(
            oldest_kept,
            base_ts + 5 * 60,
            "oldest retained entry must be the 6th snapshot (index 5)"
        );
    }

    // ── T-P6-E02-02: per-provider rate window tests ───────────────────────────

    /// 5hr rolling window boundary: snapshot exactly at ref_ts - 5h is included;
    /// snapshot one second before is excluded.
    #[test]
    fn five_hour_window_boundary() {
        let ref_ts: i64 = 1_780_401_600; // 2026-06-02T12:00:00Z
        let boundary = ref_ts - FIVE_HOUR_SECS as i64; // exactly 5h before
        let just_before = boundary - 1; // one second before — EXCLUDED

        let snaps: Vec<QuotaState> = vec![
            snapshot_with_provider(
                "acct1",
                "cc",
                PROVIDER_CLAUDE_MAX,
                just_before,
                "model",
                500,
            ),
            snapshot_with_provider("acct1", "cc", PROVIDER_CLAUDE_MAX, boundary, "model", 300),
            snapshot_with_provider("acct1", "cc", PROVIDER_CLAUDE_MAX, ref_ts, "model", 200),
        ];
        let snap_refs: Vec<&QuotaState> = snaps.iter().collect();

        let windows = build_rate_windows(&snap_refs, PROVIDER_CLAUDE_MAX, ref_ts);

        // Claude Max has 2 windows: 5hr + weekly.
        assert_eq!(windows.len(), 2, "claude-max must have 2 rate windows");
        let five_hr = windows.iter().find(|w| w.label == "5hr").unwrap();
        // boundary (300) + ref_ts (200) = 500; just_before (500) excluded.
        assert_eq!(
            five_hr.used, 500,
            "5hr window should include boundary and ref_ts only"
        );
        let weekly = windows.iter().find(|w| w.label == "weekly").unwrap();
        // All three snapshots fall within 7 days, so all are included.
        assert_eq!(
            weekly.used, 1_000,
            "weekly window should include all 3 snaps"
        );
    }

    /// OC-Go rate windows sum cost_usd, not token counts.
    #[test]
    fn oc_go_cost_window() {
        let ref_ts: i64 = 1_780_401_600;
        let five_hours_ago = ref_ts - FIVE_HOUR_SECS as i64;
        let six_hours_ago = five_hours_ago - 3_600; // outside 5hr window

        let snaps: Vec<QuotaState> = vec![
            snapshot_with_cost("oc1", PROVIDER_OC_GO, six_hours_ago, "gpt-4o", 0.50),
            snapshot_with_cost("oc1", PROVIDER_OC_GO, five_hours_ago, "gpt-4o", 1.20),
            snapshot_with_cost("oc1", PROVIDER_OC_GO, ref_ts, "gpt-4o", 0.80),
        ];
        let snap_refs: Vec<&QuotaState> = snaps.iter().collect();

        let windows = build_rate_windows(&snap_refs, PROVIDER_OC_GO, ref_ts);

        // OC-Go has 2 windows: 5hr + monthly.
        assert_eq!(windows.len(), 2, "oc-go must have 2 rate windows");
        let five_hr = windows.iter().find(|w| w.label == "5hr").unwrap();
        // five_hours_ago (1.20) + ref_ts (0.80) = 2.00; six_hours_ago (0.50) excluded.
        let cost = five_hr.cost_usd.expect("cost_usd must be set for oc-go");
        assert!(
            (cost - 2.0_f64).abs() < 1e-9,
            "5hr cost should be 2.00, got {cost}"
        );
    }

    /// Multi-provider aggregation: claude-max and codex accounts in one store.
    #[test]
    fn multi_provider_rate_windows() {
        let ref_ts: i64 = 1_780_401_600;

        let snaps = vec![
            snapshot_with_provider("cc1", "cc", PROVIDER_CLAUDE_MAX, ref_ts, "sonnet", 1_000),
            snapshot_with_provider("cx1", "oc", PROVIDER_OPENAI_CODEX, ref_ts, "gpt-4o", 500),
        ];

        let store = aggregate_quota(&snaps, 0);

        let cc = store
            .accounts
            .iter()
            .find(|a| a.account_id == "cc1")
            .unwrap();
        let cx = store
            .accounts
            .iter()
            .find(|a| a.account_id == "cx1")
            .unwrap();

        // Claude Max → 2 windows (5hr + weekly); Codex → 1 window (5hr).
        assert_eq!(cc.rate_windows.len(), 2, "claude-max must have 2 windows");
        assert_eq!(cx.rate_windows.len(), 1, "codex must have 1 window");
        assert_eq!(cc.rate_windows[0].label, "5hr");
        assert_eq!(cc.rate_windows[1].label, "weekly");
        assert_eq!(cx.rate_windows[0].label, "5hr");
    }

    /// GFP accounts get a per-day request-count window.
    #[test]
    fn gfp_per_key_window() {
        let ref_ts: i64 = 1_780_401_600;
        let yesterday = ref_ts - DAY_SECS as i64 - 1; // outside daily window
        let today = ref_ts - 3_600; // inside daily window

        let snaps: Vec<QuotaState> = vec![
            snapshot_with_provider("gfp1", "oc", PROVIDER_GFP, yesterday, "gemini", 100),
            snapshot_with_provider("gfp1", "oc", PROVIDER_GFP, today, "gemini", 200),
            snapshot_with_provider("gfp1", "oc", PROVIDER_GFP, ref_ts, "gemini", 150),
        ];
        let snap_refs: Vec<&QuotaState> = snaps.iter().collect();

        let windows = build_rate_windows(&snap_refs, PROVIDER_GFP, ref_ts);

        assert_eq!(windows.len(), 1, "gfp must have 1 window (daily)");
        assert_eq!(windows[0].label, "daily");
        // today (200) + ref_ts (150) = 350; yesterday excluded.
        assert_eq!(windows[0].used, 350, "gfp daily window should be 350");
    }
}
