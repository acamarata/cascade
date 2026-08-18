//! Hard-stop budget guard for multi-source quota management.
//!
//! Purpose: check whether a pending request would push a provider account over
//! its configured per-window budget limits before the request is dispatched.
//! Returns `Allow` when the budget allows the request, or `Deny*` with the
//! specific limit that would be exceeded.
//!
//! This is a **synchronous pure function** — it reads the current [`QuotaStore`]
//! and the [`BudgetConfig`], performs arithmetic, and returns a [`BudgetResult`].
//! No I/O, no tokio, no mutation.
//!
//! Inputs:
//!   - `provider`             — `PROVIDER_*` constant string.
//!   - `account_id`           — stable account identifier.
//!   - `estimated_tokens`     — predicted token count for the request (0 for cost-only providers).
//!   - `estimated_cost_usd`   — predicted USD cost (0.0 for token-only providers).
//!   - `store`                — current aggregated [`QuotaStore`] snapshot.
//!   - `config`               — [`BudgetConfig`] specifying per-provider limits.
//!
//! Outputs: [`BudgetResult`] variant:
//!   - [`BudgetResult::Allow`] — request is within budget.
//!   - [`BudgetResult::DenyLimit`] — a token-based window limit would be exceeded.
//!   - [`BudgetResult::DenyCost`] — a cost-based limit would be exceeded.
//!   - [`BudgetResult::DenyUnknown`] — budget state could not be determined
//!     (fail-closed mode only; see Constraints).
//!
//! Constraints:
//!   - Pure function — no I/O, no `Utc::now()`, no tokio.
//!   - Limits of `0` / `0.0` in `BudgetConfig` mean "disabled" (always Allow,
//!     in BOTH modes — an explicitly disabled check is known config state,
//!     not unknown state).
//!   - Two modes (CFC-10): [`BudgetGuard::new`] is **fail-open** — missing
//!     store entries (account not found, no windows) and unknown providers
//!     return `Allow`. [`BudgetGuard::new_fail_closed`] is **fail-closed** for
//!     AUTONOMOUS dispatch paths (daemon scheduler, conductor fan-out) — the
//!     same conditions return `DenyUnknown` so an autonomous run never
//!     dispatches on a budget it could not determine. Interactive/manual
//!     callers keep the fail-open constructor; their behavior is unchanged.
//!
//! SPORT: MASTER-DAEMON.md → budget_guard module (E-P6-02 T-05)

use cascade_core::quota_store::QuotaStore;

use crate::config::BudgetConfig;

// ── BudgetResult ──────────────────────────────────────────────────────────────

/// Result of a `BudgetGuard::check` call.
#[derive(Debug, Clone, PartialEq)]
pub enum BudgetResult {
    /// The request is within all budget limits — dispatch is safe.
    Allow,

    /// A token-based window limit would be exceeded.
    DenyLimit {
        /// Label of the window that would be exceeded (e.g. `"5hr"`, `"hourly"`).
        window: String,
        /// Tokens used in this window so far.
        used: u64,
        /// The configured token limit.
        limit: u64,
    },

    /// A cost-based limit would be exceeded.
    DenyCost {
        /// USD already spent in the window.
        spent_usd: f64,
        /// The configured USD limit.
        limit_usd: f64,
    },

    /// Budget state could not be determined — unknown account, unknown
    /// provider, or a missing window whose limit is enabled — while the guard
    /// ran fail-closed (CFC-10). Kept distinct from `DenyLimit`/`DenyCost` so
    /// callers can tell "over budget" from "couldn't determine budget".
    DenyUnknown {
        /// Human-readable description of the unknown state.
        reason: String,
    },
}

// ── BudgetGuard ───────────────────────────────────────────────────────────────

/// Budget guard — checks per-window limits before a request is dispatched.
///
/// Constructed once and reused; holds the budget configuration from
/// `~/.cascade/config.toml § [budget]`. Two modes (CFC-10): [`BudgetGuard::new`]
/// fails OPEN (interactive/manual callers, historical behavior unchanged) and
/// [`BudgetGuard::new_fail_closed`] fails CLOSED (AUTONOMOUS dispatch paths —
/// daemon scheduler, conductor fan-out).
pub struct BudgetGuard {
    /// Budget limits loaded from `[budget]` in config.toml.
    pub config: BudgetConfig,
    /// Fail-closed mode (CFC-10): when `true`, unknown account / missing
    /// windows / unknown provider return [`BudgetResult::DenyUnknown`]
    /// instead of `Allow`. Set only by [`BudgetGuard::new_fail_closed`].
    fail_closed: bool,
}

impl BudgetGuard {
    /// Create a new **fail-open** guard from the given `BudgetConfig`.
    ///
    /// Unknown account, missing windows, and unknown providers all return
    /// `Allow` — the historical behavior, kept for interactive/manual checks.
    pub fn new(config: BudgetConfig) -> Self {
        Self {
            config,
            fail_closed: false,
        }
    }

    /// Create a new **fail-closed** guard (CFC-10) for AUTONOMOUS dispatch
    /// paths (daemon scheduler, conductor fan-out).
    ///
    /// Identical limit arithmetic to [`BudgetGuard::new`]; only the
    /// unknown-state outcomes differ — they return
    /// [`BudgetResult::DenyUnknown`] so an autonomous run never dispatches on
    /// a budget it could not determine.
    pub fn new_fail_closed(config: BudgetConfig) -> Self {
        Self {
            config,
            fail_closed: true,
        }
    }

    /// Map an unknown-budget-state condition onto the result for this guard's
    /// mode: `DenyUnknown` when fail-closed, `Allow` when fail-open.
    fn unknown_state(&self, reason: String) -> BudgetResult {
        if self.fail_closed {
            BudgetResult::DenyUnknown { reason }
        } else {
            BudgetResult::Allow
        }
    }

    /// Check whether `estimated_tokens` / `estimated_cost_usd` fit within the
    /// budget for `(provider, account_id)` using the current `store` snapshot.
    ///
    /// Inputs:
    ///   - `provider`           — `PROVIDER_*` string.
    ///   - `account_id`         — account to check.
    ///   - `estimated_tokens`   — predicted tokens (0 for cost-only checks).
    ///   - `estimated_cost_usd` — predicted USD cost (0.0 for token-only checks).
    ///   - `store`              — read-only current quota store.
    ///
    /// Outputs: [`BudgetResult`] — `Allow` or the first limit exceeded.
    ///
    /// Constraints:
    ///   - Pure synchronous — no I/O.
    ///   - A limit of `0`/`0.0` in `BudgetConfig` means disabled — returns Allow
    ///     in both modes (explicit config, not unknown state).
    ///   - Unknown account / missing windows / unknown provider → `Allow` when
    ///     fail-open (`new`), `DenyUnknown` when fail-closed (`new_fail_closed`,
    ///     CFC-10).
    pub fn check(
        &self,
        provider: &str,
        account_id: &str,
        estimated_tokens: u64,
        estimated_cost_usd: f64,
        store: &QuotaStore,
    ) -> BudgetResult {
        use cascade_types::quota_store::{PROVIDER_CLAUDE_MAX, PROVIDER_OC_GO};

        // Find the account entry.
        let account = match store
            .accounts
            .iter()
            .find(|a| a.provider == provider && a.account_id == account_id)
        {
            Some(a) => a,
            // Unknown account → fail open (fail-closed denies, CFC-10).
            None => {
                return self.unknown_state(format!(
                    "unknown account {provider}/{account_id}: no quota-store entry"
                ))
            }
        };

        match provider {
            PROVIDER_CLAUDE_MAX => {
                let limit = self.config.claude_max_hourly_token_limit;
                if limit == 0 {
                    // Disabled limit is explicit config — Allow in both modes.
                    return BudgetResult::Allow;
                }
                // Find the 5hr rate window (used as a proxy for the hourly check;
                // the budget_guard intentionally uses the window the aggregator builds).
                match account.rate_windows.iter().find(|w| w.label == "5hr") {
                    Some(w) => {
                        let projected = w.used.saturating_add(estimated_tokens);
                        if projected > limit {
                            BudgetResult::DenyLimit {
                                window: "5hr".to_string(),
                                used: w.used,
                                limit,
                            }
                        } else {
                            BudgetResult::Allow
                        }
                    }
                    // Window an enabled limit depends on is absent → fail open
                    // (fail-closed denies, CFC-10).
                    None => self.unknown_state(format!(
                        "missing 5hr window for {PROVIDER_CLAUDE_MAX} account {account_id}: token usage undeterminable"
                    )),
                }
            }
            PROVIDER_OC_GO => {
                // Check hourly USD limit (5hr window).
                let hourly_limit = self.config.oc_go_hourly_usd_limit;
                if hourly_limit > 0.0 {
                    match account.rate_windows.iter().find(|w| w.label == "5hr") {
                        Some(w) => {
                            let spent = w.cost_usd.unwrap_or(0.0);
                            if spent + estimated_cost_usd > hourly_limit {
                                return BudgetResult::DenyCost {
                                    spent_usd: spent,
                                    limit_usd: hourly_limit,
                                };
                            }
                        }
                        None => {
                            return self.unknown_state(format!(
                                "missing 5hr window for {PROVIDER_OC_GO} account {account_id}: hourly spend undeterminable"
                            ))
                        }
                    }
                }

                // Check monthly USD limit.
                let monthly_limit = self.config.oc_go_monthly_usd_limit;
                if monthly_limit > 0.0 {
                    match account.rate_windows.iter().find(|w| w.label == "monthly") {
                        Some(w) => {
                            let spent = w.cost_usd.unwrap_or(0.0);
                            if spent + estimated_cost_usd > monthly_limit {
                                return BudgetResult::DenyCost {
                                    spent_usd: spent,
                                    limit_usd: monthly_limit,
                                };
                            }
                        }
                        None => {
                            return self.unknown_state(format!(
                                "missing monthly window for {PROVIDER_OC_GO} account {account_id}: monthly spend undeterminable"
                            ))
                        }
                    }
                }

                BudgetResult::Allow
            }
            // All other providers: no budget limits defined yet → fail open
            // (fail-closed denies, CFC-10).
            _ => self.unknown_state(format!(
                "unknown provider {provider}: no budget limits defined"
            )),
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::quota_store::{AccountEntry, ModelUsage, QuotaStore, RateWindow};
    use cascade_types::quota_store::{
        PROVIDER_CLAUDE_MAX, PROVIDER_OC_GO, QUOTA_STORE_SCHEMA_VERSION,
    };
    use std::collections::HashMap;

    fn make_store(account: AccountEntry) -> QuotaStore {
        QuotaStore {
            schema_version: QUOTA_STORE_SCHEMA_VERSION,
            updated_at: "2026-06-02T12:00:00Z".to_string(),
            accounts: vec![account],
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        }
    }

    fn make_account_with_windows(
        account_id: &str,
        provider: &str,
        windows: Vec<RateWindow>,
    ) -> AccountEntry {
        AccountEntry {
            account_id: account_id.to_string(),
            harness: "cc".to_string(),
            provider: provider.to_string(),
            models: {
                let mut m = HashMap::new();
                m.insert(
                    "sonnet".to_string(),
                    ModelUsage {
                        used: 0,
                        limit: None,
                        reset_at: None,
                        pct_used: None,
                        cost_usd: None,
                    },
                );
                m
            },
            week_total_used: 0,
            month_total_used: 0,
            last_polled: "2026-06-02T12:00:00Z".to_string(),
            rate_windows: windows,
        }
    }

    fn token_window(label: &str, used: u64) -> RateWindow {
        RateWindow {
            window_secs: 18_000,
            label: label.to_string(),
            used,
            limit: None,
            reset_at: None,
            cost_usd: None,
        }
    }

    fn cost_window(label: &str, cost_usd: f64) -> RateWindow {
        RateWindow {
            window_secs: 18_000,
            label: label.to_string(),
            used: 0,
            limit: None,
            reset_at: None,
            cost_usd: Some(cost_usd),
        }
    }

    /// When limit is 0 (disabled), any token count is Allowed.
    #[test]
    fn allow_when_limit_disabled() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 0, // disabled
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new(config);

        let account = make_account_with_windows(
            "acct-a",
            PROVIDER_CLAUDE_MAX,
            vec![token_window("5hr", 90_000)],
        );
        let store = make_store(account);

        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 100_000, 0.0, &store);
        assert_eq!(result, BudgetResult::Allow);
    }

    /// When projected tokens exceed the limit, DenyLimit is returned.
    #[test]
    fn deny_when_projected_tokens_exceed_limit() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new(config);

        let account = make_account_with_windows(
            "acct-a",
            PROVIDER_CLAUDE_MAX,
            vec![token_window("5hr", 95_000)], // 95k used
        );
        let store = make_store(account);

        // 95k + 10k = 105k > 100k limit
        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 10_000, 0.0, &store);
        assert_eq!(
            result,
            BudgetResult::DenyLimit {
                window: "5hr".to_string(),
                used: 95_000,
                limit: 100_000,
            }
        );
    }

    /// OC-Go: when 5hr cost exceeds hourly limit, DenyCost is returned.
    #[test]
    fn deny_cost_when_oc_go_hourly_exceeded() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 0,
            oc_go_hourly_usd_limit: 5.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new(config);

        let account = make_account_with_windows(
            "oc-acct",
            PROVIDER_OC_GO,
            vec![
                cost_window("5hr", 4.80),      // $4.80 spent
                cost_window("monthly", 20.00), // not the checked window
            ],
        );
        let store = make_store(account);

        // $4.80 + $0.50 = $5.30 > $5.00 limit
        let result = guard.check(PROVIDER_OC_GO, "oc-acct", 0, 0.50, &store);
        assert_eq!(
            result,
            BudgetResult::DenyCost {
                spent_usd: 4.80,
                limit_usd: 5.0,
            }
        );
    }

    // ── CFC-10 (T-P7-E13-08): fail-open vs fail-closed on unknown state ──────

    /// Assert `result` is `DenyUnknown` whose reason contains `needle`.
    fn assert_deny_unknown(result: BudgetResult, needle: &str) {
        match result {
            BudgetResult::DenyUnknown { reason } => {
                assert!(
                    reason.contains(needle),
                    "reason {reason:?} lacks {needle:?}"
                );
            }
            other => panic!("expected DenyUnknown, got {other:?}"),
        }
    }

    /// Fail-open (default): unknown account still Allows.
    #[test]
    fn fail_open_allows_unknown_account() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new(config);

        let account = make_account_with_windows("acct-a", PROVIDER_CLAUDE_MAX, vec![]);
        let store = make_store(account);

        // "acct-ghost" has no entry in the store.
        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-ghost", 1_000, 0.0, &store);
        assert_eq!(result, BudgetResult::Allow);
    }

    /// Fail-closed: unknown account denies with DenyUnknown (CFC-10).
    #[test]
    fn fail_closed_denies_unknown_account() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        let account = make_account_with_windows("acct-a", PROVIDER_CLAUDE_MAX, vec![]);
        let store = make_store(account);

        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-ghost", 1_000, 0.0, &store);
        assert_deny_unknown(result, "unknown account");
    }

    /// Fail-open: missing 5hr window with an enabled limit still Allows.
    #[test]
    fn fail_open_allows_missing_windows() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new(config);

        // Account exists but carries no rate windows at all.
        let account = make_account_with_windows("acct-a", PROVIDER_CLAUDE_MAX, vec![]);
        let store = make_store(account);

        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 1_000, 0.0, &store);
        assert_eq!(result, BudgetResult::Allow);
    }

    /// Fail-closed: missing 5hr window on claude-max (enabled limit) denies.
    #[test]
    fn fail_closed_denies_missing_window_claude_max() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        let account = make_account_with_windows("acct-a", PROVIDER_CLAUDE_MAX, vec![]);
        let store = make_store(account);

        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 1_000, 0.0, &store);
        assert_deny_unknown(result, "missing 5hr window");
    }

    /// Fail-closed: missing 5hr window on oc-go with an enabled hourly limit denies.
    #[test]
    fn fail_closed_denies_missing_window_oc_go_hourly() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 0,
            oc_go_hourly_usd_limit: 5.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        // Only the monthly window is present; the 5hr window the hourly check
        // depends on is missing.
        let account = make_account_with_windows(
            "oc-acct",
            PROVIDER_OC_GO,
            vec![cost_window("monthly", 20.00)],
        );
        let store = make_store(account);

        let result = guard.check(PROVIDER_OC_GO, "oc-acct", 0, 0.50, &store);
        assert_deny_unknown(result, "missing 5hr window");
    }

    /// Fail-closed: missing monthly window on oc-go with an enabled monthly
    /// limit denies.
    #[test]
    fn fail_closed_denies_missing_window_oc_go_monthly() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 0,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 100.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        // Only the 5hr window is present; the monthly window is missing.
        let account =
            make_account_with_windows("oc-acct", PROVIDER_OC_GO, vec![cost_window("5hr", 1.00)]);
        let store = make_store(account);

        let result = guard.check(PROVIDER_OC_GO, "oc-acct", 0, 0.50, &store);
        assert_deny_unknown(result, "missing monthly window");
    }

    /// Fail-open: unknown provider still Allows.
    #[test]
    fn fail_open_allows_unknown_provider() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 5.0,
            oc_go_monthly_usd_limit: 100.0,
        };
        let guard = BudgetGuard::new(config);

        // Account entry exists, but its provider has no limits defined.
        let account = make_account_with_windows("acct-x", "mystery-provider", vec![]);
        let store = make_store(account);

        let result = guard.check("mystery-provider", "acct-x", 1_000, 1.0, &store);
        assert_eq!(result, BudgetResult::Allow);
    }

    /// Fail-closed: unknown provider denies with DenyUnknown (CFC-10).
    #[test]
    fn fail_closed_denies_unknown_provider() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 5.0,
            oc_go_monthly_usd_limit: 100.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        let account = make_account_with_windows("acct-x", "mystery-provider", vec![]);
        let store = make_store(account);

        let result = guard.check("mystery-provider", "acct-x", 1_000, 1.0, &store);
        assert_deny_unknown(result, "unknown provider");
    }

    /// Fail-closed denies only UNKNOWN state: an explicitly disabled limit
    /// (`0`) is known config and still Allows — even with no windows present.
    #[test]
    fn fail_closed_still_allows_disabled_limit() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 0, // disabled
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        let account = make_account_with_windows("acct-a", PROVIDER_CLAUDE_MAX, vec![]);
        let store = make_store(account);

        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 100_000, 0.0, &store);
        assert_eq!(result, BudgetResult::Allow);
    }

    /// Fail-closed keeps the normal arithmetic: within budget → Allow,
    /// over budget → DenyLimit (not DenyUnknown).
    #[test]
    fn fail_closed_allows_within_budget_and_denies_over() {
        let config = BudgetConfig {
            claude_max_hourly_token_limit: 100_000,
            oc_go_hourly_usd_limit: 0.0,
            oc_go_monthly_usd_limit: 0.0,
        };
        let guard = BudgetGuard::new_fail_closed(config);

        let account = make_account_with_windows(
            "acct-a",
            PROVIDER_CLAUDE_MAX,
            vec![token_window("5hr", 50_000)],
        );
        let store = make_store(account);

        // 50k + 10k = 60k ≤ 100k → Allow even in fail-closed mode.
        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 10_000, 0.0, &store);
        assert_eq!(result, BudgetResult::Allow);

        // 95k + 10k = 105k > 100k → DenyLimit (determinable state).
        let account = make_account_with_windows(
            "acct-a",
            PROVIDER_CLAUDE_MAX,
            vec![token_window("5hr", 95_000)],
        );
        let store = make_store(account);
        let result = guard.check(PROVIDER_CLAUDE_MAX, "acct-a", 10_000, 0.0, &store);
        assert_eq!(
            result,
            BudgetResult::DenyLimit {
                window: "5hr".to_string(),
                used: 95_000,
                limit: 100_000,
            }
        );
    }
}
