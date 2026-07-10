//! Multi-provider quota IPC tests (E-P6-02 T-06).
//!
//! Tests the new `budget_check`, `get_rotation_advice`, and `handle_budget_check`
//! logic against in-memory fixtures without spawning the real daemon binary.
//! Wire tests for `update_quota_state` require a live socket and are integration
//! tests in the `quota_store_wire_test.rs` pattern; here we test the handler
//! logic directly via the library surface.

use cascade_core::quota_store::{QuotaStore, QUOTA_STORE_SCHEMA_VERSION};
use cascade_daemon::{
    config::BudgetConfig,
    ipc_handlers::{handle_budget_check, handle_get_rotation_advice, BudgetCheckParams},
};
use cascade_types::quota_store::{
    AccountEntry, ModelUsage, RateWindow, PROVIDER_CLAUDE_MAX, PROVIDER_OC_GO,
};
use std::collections::HashMap;

// ── Helpers ───────────────────────────────────────────────────────────────────

fn empty_store() -> QuotaStore {
    QuotaStore {
        schema_version: QUOTA_STORE_SCHEMA_VERSION,
        updated_at: "2026-06-02T12:00:00Z".to_string(),
        accounts: vec![],
        week_totals: HashMap::new(),
        month_totals: HashMap::new(),
        rolling_history: vec![],
    }
}

fn store_with_accounts(accounts: Vec<AccountEntry>) -> QuotaStore {
    QuotaStore {
        schema_version: QUOTA_STORE_SCHEMA_VERSION,
        updated_at: "2026-06-02T12:00:00Z".to_string(),
        accounts,
        week_totals: HashMap::new(),
        month_totals: HashMap::new(),
        rolling_history: vec![],
    }
}

fn make_account_token(
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

fn make_account_cost(
    account_id: &str,
    provider: &str,
    five_hr_cost: f64,
    monthly_cost: f64,
) -> AccountEntry {
    AccountEntry {
        account_id: account_id.to_string(),
        harness: "oc".to_string(),
        provider: provider.to_string(),
        models: HashMap::new(),
        week_total_used: 0,
        month_total_used: 0,
        last_polled: "2026-06-02T12:00:00Z".to_string(),
        rate_windows: vec![
            RateWindow {
                window_secs: 18_000,
                label: "5hr".to_string(),
                used: 0,
                limit: None,
                reset_at: None,
                cost_usd: Some(five_hr_cost),
            },
            RateWindow {
                window_secs: 30 * 86_400,
                label: "monthly".to_string(),
                used: 0,
                limit: None,
                reset_at: None,
                cost_usd: Some(monthly_cost),
            },
        ],
    }
}

// ── T1: Multi-provider store read fixture ─────────────────────────────────────

/// A store with both claude-max and oc-go accounts serialises and deserialises
/// correctly preserving the `provider` field.
#[test]
fn multi_provider_store_round_trip() {
    let store = store_with_accounts(vec![
        make_account_token("cc-1", PROVIDER_CLAUDE_MAX, 1_000, None),
        make_account_cost("oc-1", PROVIDER_OC_GO, 1.50, 20.0),
    ]);

    let json = serde_json::to_string(&store).expect("serialise");
    let back: QuotaStore = serde_json::from_str(&json).expect("deserialise");

    assert_eq!(back.accounts.len(), 2);
    let cc = back
        .accounts
        .iter()
        .find(|a| a.account_id == "cc-1")
        .unwrap();
    let oc = back
        .accounts
        .iter()
        .find(|a| a.account_id == "oc-1")
        .unwrap();
    assert_eq!(cc.provider, PROVIDER_CLAUDE_MAX);
    assert_eq!(oc.provider, PROVIDER_OC_GO);
    assert_eq!(oc.rate_windows.len(), 2);
    let five_hr = oc.rate_windows.iter().find(|w| w.label == "5hr").unwrap();
    let cost = five_hr.cost_usd.expect("oc-go 5hr must have cost_usd");
    assert!((cost - 1.50).abs() < 1e-9);
}

// ── T2: get_rotation_advice ───────────────────────────────────────────────────

/// `get_rotation_advice` picks the account with lower 5hr usage.
#[test]
fn get_rotation_advice_selects_least_used() {
    let store = store_with_accounts(vec![
        make_account_token("cc-high", PROVIDER_CLAUDE_MAX, 8_000, None),
        make_account_token("cc-low", PROVIDER_CLAUDE_MAX, 2_000, None),
    ]);

    let advice = handle_get_rotation_advice(PROVIDER_CLAUDE_MAX, &store);
    assert_eq!(advice["account_id"], "cc-low");
    assert_eq!(advice["exhausted"], false);
}

/// `get_rotation_advice` returns `exhausted: true` when no accounts exist.
#[test]
fn get_rotation_advice_exhausted_when_no_accounts() {
    let store = empty_store();
    let advice = handle_get_rotation_advice(PROVIDER_CLAUDE_MAX, &store);
    assert!(advice["account_id"].is_null());
    assert_eq!(advice["exhausted"], true);
}

// ── T3: budget_check allow ────────────────────────────────────────────────────

/// `budget_check` allows a request when all limits are disabled (0).
#[test]
fn budget_check_allow_when_limits_disabled() {
    let config = BudgetConfig {
        claude_max_hourly_token_limit: 0, // disabled
        oc_go_hourly_usd_limit: 0.0,
        oc_go_monthly_usd_limit: 0.0,
    };
    let store = store_with_accounts(vec![make_account_token(
        "cc-1",
        PROVIDER_CLAUDE_MAX,
        99_000,
        None,
    )]);

    let params = BudgetCheckParams {
        provider: PROVIDER_CLAUDE_MAX.to_string(),
        account_id: "cc-1".to_string(),
        estimated_tokens: 50_000,
        estimated_cost_usd: 0.0,
    };

    let result = handle_budget_check(params, &config, &store);
    assert_eq!(result["allow"], true);
    assert!(result["reason"].is_null());
}

// ── T4: budget_check deny-limit ───────────────────────────────────────────────

/// `budget_check` returns `allow: false` when projected tokens exceed the limit.
#[test]
fn budget_check_deny_limit_when_over() {
    let config = BudgetConfig {
        claude_max_hourly_token_limit: 100_000,
        oc_go_hourly_usd_limit: 0.0,
        oc_go_monthly_usd_limit: 0.0,
    };
    let store = store_with_accounts(vec![make_account_token(
        "cc-1",
        PROVIDER_CLAUDE_MAX,
        95_000, // 95k used
        None,
    )]);

    let params = BudgetCheckParams {
        provider: PROVIDER_CLAUDE_MAX.to_string(),
        account_id: "cc-1".to_string(),
        estimated_tokens: 10_000, // 95k + 10k = 105k > 100k
        estimated_cost_usd: 0.0,
    };

    let result = handle_budget_check(params, &config, &store);
    assert_eq!(result["allow"], false);
    let reason = result["reason"].as_str().expect("reason must be a string");
    assert!(
        reason.contains("token limit exceeded"),
        "reason should mention token limit: {reason}"
    );
}

// ── T5: budget_check deny-cost ────────────────────────────────────────────────

/// `budget_check` returns `allow: false` when projected OC-Go cost exceeds limit.
#[test]
fn budget_check_deny_cost_when_oc_go_over() {
    let config = BudgetConfig {
        claude_max_hourly_token_limit: 0,
        oc_go_hourly_usd_limit: 5.0,
        oc_go_monthly_usd_limit: 0.0,
    };
    let store = store_with_accounts(vec![make_account_cost("oc-1", PROVIDER_OC_GO, 4.90, 10.0)]);

    let params = BudgetCheckParams {
        provider: PROVIDER_OC_GO.to_string(),
        account_id: "oc-1".to_string(),
        estimated_tokens: 0,
        estimated_cost_usd: 0.50, // $4.90 + $0.50 = $5.40 > $5.00
    };

    let result = handle_budget_check(params, &config, &store);
    assert_eq!(result["allow"], false);
    let reason = result["reason"].as_str().expect("reason must be a string");
    assert!(
        reason.contains("cost limit exceeded"),
        "reason should mention cost limit: {reason}"
    );
}
