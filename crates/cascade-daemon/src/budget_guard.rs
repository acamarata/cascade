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
//!
//! Constraints:
//!   - Pure function — no I/O, no `Utc::now()`, no tokio.
//!   - Limits of `0` / `0.0` in `BudgetConfig` mean "disabled" (always Allow).
//!   - Missing store entries (account not found, no windows) → Allow (fail open).
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
}

// ── BudgetGuard ───────────────────────────────────────────────────────────────

/// Budget guard — checks per-window limits before a request is dispatched.
///
/// Constructed once and reused; holds the budget configuration from
/// `~/.cascade/config.toml § [budget]`.
pub struct BudgetGuard {
    /// Budget limits loaded from `[budget]` in config.toml.
    pub config: BudgetConfig,
}

impl BudgetGuard {
    /// Create a new guard from the given `BudgetConfig`.
    pub fn new(config: BudgetConfig) -> Self {
        Self { config }
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
    ///   - A limit of `0`/`0.0` in `BudgetConfig` means disabled — returns Allow.
    ///   - Missing account or missing windows → Allow (fail open).
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
            None => return BudgetResult::Allow, // unknown account → fail open
        };

        match provider {
            PROVIDER_CLAUDE_MAX => {
                let limit = self.config.claude_max_hourly_token_limit;
                if limit == 0 {
                    return BudgetResult::Allow;
                }
                // Find the 5hr rate window (used as a proxy for the hourly check;
                // the budget_guard intentionally uses the window the aggregator builds).
                let five_hr = account.rate_windows.iter().find(|w| w.label == "5hr");
                if let Some(w) = five_hr {
                    let projected = w.used.saturating_add(estimated_tokens);
                    if projected > limit {
                        return BudgetResult::DenyLimit {
                            window: "5hr".to_string(),
                            used: w.used,
                            limit,
                        };
                    }
                }
                BudgetResult::Allow
            }
            PROVIDER_OC_GO => {
                // Check hourly USD limit (5hr window).
                let hourly_limit = self.config.oc_go_hourly_usd_limit;
                if hourly_limit > 0.0 {
                    let five_hr = account.rate_windows.iter().find(|w| w.label == "5hr");
                    if let Some(w) = five_hr {
                        let spent = w.cost_usd.unwrap_or(0.0);
                        if spent + estimated_cost_usd > hourly_limit {
                            return BudgetResult::DenyCost {
                                spent_usd: spent,
                                limit_usd: hourly_limit,
                            };
                        }
                    }
                }

                // Check monthly USD limit.
                let monthly_limit = self.config.oc_go_monthly_usd_limit;
                if monthly_limit > 0.0 {
                    let monthly = account.rate_windows.iter().find(|w| w.label == "monthly");
                    if let Some(w) = monthly {
                        let spent = w.cost_usd.unwrap_or(0.0);
                        if spent + estimated_cost_usd > monthly_limit {
                            return BudgetResult::DenyCost {
                                spent_usd: spent,
                                limit_usd: monthly_limit,
                            };
                        }
                    }
                }

                BudgetResult::Allow
            }
            // All other providers: no budget limits defined yet → fail open.
            _ => BudgetResult::Allow,
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
}
