//! Round-robin account rotation selector for multi-source quota management.
//!
//! Purpose: given a provider and the current [`QuotaStore`], select the
//! account with the most remaining headroom. When an account is exhausted,
//! signal a `quota.exhausted` event on the bus so downstream consumers can
//! react without polling.
//!
//! Inputs:
//!   - `store`    — current [`QuotaStore`] (read-only); may be stale by
//!                  seconds but is always the daemon's last-written snapshot.
//!   - `provider` — one of the `PROVIDER_*` constants from `cascade_types`.
//!   - `bus`      — [`SharedBus`] for publishing `quota.exhausted` events.
//!
//! Outputs:
//!   - `select_account` returns `Some(account_id)` when a non-exhausted
//!     account exists; `None` when all accounts for the provider are at
//!     or over their 5hr window limit.
//!   - `signal_exhausted` publishes a `quota.exhausted` JSON event with the
//!     provider and a human-readable reason string.
//!
//! Constraints:
//!   - `select_account` is synchronous and pure — it does NOT mutate the store.
//!   - `signal_exhausted` is async (publishes to the event bus) but never
//!     panics; failures are logged as warnings.
//!   - `quota.exhausted` event kind: documented in `event_bus.rs`.
//!
//! SPORT: MASTER-DAEMON.md → rotation_selector module (E-P6-02 T-04)

use cascade_core::quota_store::QuotaStore;

use crate::event_bus::SharedBus;

// ── RotationSelector ──────────────────────────────────────────────────────────

/// Rotation selector — picks the least-used account for a given provider.
///
/// Holds a reference to the event bus so it can signal exhaustion events
/// without the caller managing bus access.
pub struct RotationSelector {
    /// Event bus handle for `quota.exhausted` signals.
    pub bus: SharedBus,
}

impl RotationSelector {
    /// Construct a new selector backed by `bus`.
    pub fn new(bus: SharedBus) -> Self {
        Self { bus }
    }

    /// Select the account with the lowest 5hr window usage for `provider`.
    ///
    /// Inputs:
    ///   - `provider` — one of the `PROVIDER_*` constants (e.g. `"claude-max"`).
    ///   - `store`    — current aggregated quota store (read-only snapshot).
    ///
    /// Outputs:
    ///   - `Some(account_id)` — the account whose 5hr `used` is lowest.
    ///   - `None` — no accounts found for this provider, or all are over their
    ///     5hr window `limit` (if a limit is set).
    ///
    /// Constraints: pure — no I/O, no mutation of the store.
    pub fn select_account(provider: &str, store: &QuotaStore) -> Option<String> {
        // Filter accounts to those matching the requested provider.
        let candidates: Vec<_> = store
            .accounts
            .iter()
            .filter(|a| a.provider == provider)
            .collect();

        if candidates.is_empty() {
            return None;
        }

        // For each account find its 5hr window usage.
        // If the account has a 5hr window with a limit set, skip accounts
        // that are at or over their limit.
        let best = candidates
            .iter()
            .filter(|a| {
                // Keep accounts that are NOT over their 5hr limit.
                let five_hr = a.rate_windows.iter().find(|w| w.label == "5hr");
                match five_hr {
                    Some(w) => match w.limit {
                        Some(limit) => w.used < limit,
                        None => true, // no limit set → not exhausted
                    },
                    None => true, // no 5hr window recorded → assume not exhausted
                }
            })
            .min_by_key(|a| {
                // Sort by 5hr used (lowest = most headroom).
                let five_hr = a.rate_windows.iter().find(|w| w.label == "5hr");
                five_hr.map(|w| w.used).unwrap_or(0)
            });

        best.map(|a| a.account_id.clone())
    }

    /// Publish a `quota.exhausted` event for `provider` on the event bus.
    ///
    /// Purpose: let subscribers (fleet widget, rotation logic) react to
    /// provider exhaustion without polling the quota store repeatedly.
    ///
    /// Inputs:
    ///   - `provider` — the exhausted provider string.
    ///   - `bus`      — event bus reference (not `&self.bus` so callers can
    ///                  pass any bus handle, useful for testing).
    ///
    /// Outputs: the `quota.exhausted` event is persisted in events.db.
    ///          Failures are logged as warnings; this never panics.
    ///
    /// # Event kind: `quota.exhausted`
    ///
    /// Published on the event bus when all accounts for a provider are at or
    /// over their 5hr window limit. Consumers should treat this as advisory;
    /// the quota store is the authoritative source of truth.
    pub async fn signal_exhausted(provider: &str, bus: &SharedBus) {
        let payload = serde_json::json!({
            "provider": provider,
            "reason": "all accounts at or over 5hr window limit",
        });
        if let Err(e) = bus.publish("quota.exhausted", payload).await {
            tracing::warn!(
                provider,
                error = %e,
                "rotation_selector: failed to publish quota.exhausted event"
            );
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::quota_store::{AccountEntry, ModelUsage, QuotaStore, RateWindow};
    use cascade_types::quota_store::{PROVIDER_CLAUDE_MAX, QUOTA_STORE_SCHEMA_VERSION};
    use std::collections::HashMap;

    fn make_store_with_accounts(accounts: Vec<AccountEntry>) -> QuotaStore {
        QuotaStore {
            schema_version: QUOTA_STORE_SCHEMA_VERSION,
            updated_at: "2026-06-02T12:00:00Z".to_string(),
            accounts,
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        }
    }

    fn make_account(
        account_id: &str,
        provider: &str,
        five_hr_used: u64,
        five_hr_limit: Option<u64>,
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
                        used: five_hr_used,
                        limit: None,
                        reset_at: None,
                        pct_used: None,
                        cost_usd: None,
                    },
                );
                m
            },
            week_total_used: five_hr_used,
            month_total_used: five_hr_used,
            last_polled: "2026-06-02T12:00:00Z".to_string(),
            rate_windows: vec![RateWindow {
                window_secs: 18_000,
                label: "5hr".to_string(),
                used: five_hr_used,
                limit: five_hr_limit,
                reset_at: None,
                cost_usd: None,
            }],
        }
    }

    /// With two accounts for the same provider, the one with lower 5hr usage
    /// is selected.
    #[test]
    fn selects_lowest_5hr_usage_account() {
        let store = make_store_with_accounts(vec![
            make_account("acct-a", PROVIDER_CLAUDE_MAX, 8_000, None),
            make_account("acct-b", PROVIDER_CLAUDE_MAX, 3_000, None), // lower usage
        ]);

        let selected = RotationSelector::select_account(PROVIDER_CLAUDE_MAX, &store);
        assert_eq!(selected, Some("acct-b".to_string()));
    }

    /// When all accounts for a provider are over their 5hr limit, returns None.
    #[test]
    fn returns_none_when_all_exhausted() {
        let store = make_store_with_accounts(vec![
            make_account("acct-a", PROVIDER_CLAUDE_MAX, 10_000, Some(10_000)), // at limit
            make_account("acct-b", PROVIDER_CLAUDE_MAX, 12_000, Some(10_000)), // over limit
        ]);

        let selected = RotationSelector::select_account(PROVIDER_CLAUDE_MAX, &store);
        assert_eq!(selected, None, "all accounts exhausted should return None");
    }

    /// When the store has no accounts for the requested provider, returns None.
    #[test]
    fn returns_none_for_unknown_provider() {
        let store = make_store_with_accounts(vec![make_account(
            "acct-a",
            PROVIDER_CLAUDE_MAX,
            1_000,
            None,
        )]);

        let selected = RotationSelector::select_account("unknown-provider", &store);
        assert_eq!(selected, None);
    }
}
