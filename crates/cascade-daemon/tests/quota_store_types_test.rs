//! Compile-time + round-trip verification that `cascade_types::QuotaStore` is
//! accessible from a sibling crate (acceptance criterion for T-P2-E02-29).
//!
//! This test intentionally lives in cascade-daemon (not cascade-core) to prove
//! the re-export works across the crate boundary.

use cascade_core::{read_quota_store, write_quota_store};
use cascade_types::{
    AccountEntry, HistoryEntry, ModelUsage, QuotaStore, QUOTA_STORE_SCHEMA_VERSION,
};
use std::collections::HashMap;
use tempfile::TempDir;

/// Confirm `use cascade_types::QuotaStore` and friends compile + work
/// across the cascade-daemon crate boundary.
#[test]
fn quota_store_types_accessible_from_sibling_crate() {
    // Build a store using the re-exported types.
    let mut models = HashMap::new();
    models.insert(
        "claude-sonnet-4-6".to_string(),
        ModelUsage {
            used: 500,
            limit: Some(5_000),
            reset_at: Some("2026-08-01T00:00:00Z".to_string()),
            pct_used: Some(10.0),
        },
    );
    let account = AccountEntry {
        account_id: "test-account".to_string(),
        harness: "cc".to_string(),
        models,
        week_total_used: 500,
        month_total_used: 1_500,
        last_polled: "2026-06-02T10:00:00Z".to_string(),
    };
    let history = HistoryEntry {
        ts: 1_748_865_600,
        accounts_snapshot: vec![account.clone()],
    };
    let store = QuotaStore {
        schema_version: QUOTA_STORE_SCHEMA_VERSION,
        updated_at: "2026-06-02T10:00:00Z".to_string(),
        accounts: vec![account],
        week_totals: HashMap::new(),
        month_totals: HashMap::new(),
        rolling_history: vec![history],
    };

    // Round-trip via cascade-core I/O functions.
    let dir = TempDir::new().expect("tempdir");
    let path = dir.path().join("quota-store.json");

    write_quota_store(&path, &store).expect("write");
    let read_back = read_quota_store(&path).expect("read");

    assert_eq!(
        store, read_back,
        "round-trip across sibling-crate boundary must preserve all fields"
    );
    assert_eq!(read_back.schema_version, QUOTA_STORE_SCHEMA_VERSION);
}
