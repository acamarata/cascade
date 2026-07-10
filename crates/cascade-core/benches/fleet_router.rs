//! # fleet_router — micro-benchmarks for Router::select
//!
//! ## Purpose
//!
//! Measures `Router::select(task_class, payload)` latency at fleet sizes N=10
//! and N=100. The router reads an in-memory accounts registry (written to a
//! tempdir for each bench run) and runs the full routing-matrix logic including
//! sensitivity classification and quota headroom checks.
//!
//! ## What this measures
//!
//! - Per-route latency as the account pool grows (N=10 → N=100).
//! - Sensitivity classification overhead (public vs. sensitive payloads).
//! - All TaskClass variants across a realistic mixed-family registry.
//!
//! ## Thresholds
//!
//! `bench/scale-thresholds.json`:
//!   `fleet_routing_p99_n10_ms: 1` — p99 must be < 1 ms for N=10 accounts.
//!
//! ## Constraints
//!
//! - No network I/O. Registry is a synthetic in-memory fixture.
//! - Accounts registry is written to tempfile before each bench group.
//! - No `unwrap()` outside fixture helpers.
//!
//! ## Tickets
//!
//! perf-01

use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion};
use std::time::Duration;

use cascade_core::routing::{router::Router, router::RouterConfig, task_class::TaskClass};
use cascade_types::accounts::{
    AccessMethod, Account, AccountFamily, AccountRole, AccountsRegistry, ACCOUNTS_SCHEMA_VERSION,
};

// ── Registry fixture builders ─────────────────────────────────────────────────

fn make_account(id: &str, family: AccountFamily, role: AccountRole, priority: u8) -> Account {
    Account {
        id: id.to_string(),
        family,
        subscription: "bench".into(),
        access_methods: vec![AccessMethod::NativeCc],
        role,
        exhaustion_priority: priority,
        models: vec![],
        cli_available: true,
        key_count: 0,
        quota_account_id: None,
        notes: None,
    }
}

/// Build a synthetic registry with exactly `n` accounts.
///
/// Accounts are distributed across families to exercise the full routing-matrix
/// logic: Claude pooled accounts (half), then Gfp, Openai, Google, Opencode in
/// round-robin. This matches a realistic fleet composition at scale.
fn build_registry(n: usize) -> AccountsRegistry {
    let mut accounts = Vec::with_capacity(n + 1);

    // Always include one PrimaryT0 Claude.
    accounts.push(make_account(
        "claude-primary",
        AccountFamily::Claude,
        AccountRole::PrimaryT0,
        255,
    ));

    let families = [
        AccountFamily::Claude,
        AccountFamily::Gfp,
        AccountFamily::Openai,
        AccountFamily::Google,
        AccountFamily::Opencode,
    ];

    for i in 0..n {
        let family = families[i % families.len()].clone();
        let role = if family == AccountFamily::Gfp {
            AccountRole::Free
        } else {
            AccountRole::Pooled
        };
        let id = format!("acct-{i}");
        accounts.push(make_account(&id, family, role, (i % 250) as u8));
    }

    AccountsRegistry {
        schema_version: ACCOUNTS_SCHEMA_VERSION,
        updated_at: "2026-06-27T00:00:00Z".into(),
        accounts,
        model_matrix: vec![],
    }
}

/// Write `registry` to a tempfile and return `(Router, TempDir)`.
///
/// TempDir is returned alongside so it stays alive for the duration of the bench.
fn router_for(registry: &AccountsRegistry) -> (Router, tempfile::TempDir) {
    let dir = tempfile::tempdir().expect("tempdir");
    let path = dir.path().join("accounts.json");
    std::fs::write(&path, serde_json::to_vec(registry).expect("serialize"))
        .expect("write accounts");
    let router = Router::with_config(RouterConfig {
        accounts_registry_path: Some(path),
        quota_store_path: None,
        lane_timeout: Duration::from_secs(10),
    });
    (router, dir)
}

// ── Benchmark: fleet_routing_n10 ─────────────────────────────────────────────

fn bench_fleet_routing_n10(c: &mut Criterion) {
    let registry = build_registry(10);
    let (router, _dir) = router_for(&registry);

    let task_classes = [
        (TaskClass::BulkExec, "draft a long document"),
        (TaskClass::Cheap, "classify a tag"),
        (TaskClass::AdversarialReview, "review this code"),
        (TaskClass::InteractiveChat, "hello"),
        (TaskClass::Sensitive, "my disability rating is 70 percent"),
    ];

    let mut group = c.benchmark_group("fleet_routing");
    group.measurement_time(Duration::from_secs(8));
    group.warm_up_time(Duration::from_secs(2));

    for (task_class, payload) in &task_classes {
        let id = BenchmarkId::new(format!("n10/{task_class}"), "");
        group.bench_with_input(id, payload, |b, p| {
            b.iter(|| {
                let _ = router.select(*task_class, p);
            });
        });
    }

    group.finish();
}

// ── Benchmark: fleet_routing_n100 ────────────────────────────────────────────

fn bench_fleet_routing_n100(c: &mut Criterion) {
    let registry = build_registry(100);
    let (router, _dir) = router_for(&registry);

    let task_classes = [
        (TaskClass::BulkExec, "draft a long document"),
        (TaskClass::Cheap, "classify a tag"),
        (TaskClass::AdversarialReview, "review this code"),
    ];

    let mut group = c.benchmark_group("fleet_routing");
    group.measurement_time(Duration::from_secs(8));
    group.warm_up_time(Duration::from_secs(2));

    for (task_class, payload) in &task_classes {
        let id = BenchmarkId::new(format!("n100/{task_class}"), "");
        group.bench_with_input(id, payload, |b, p| {
            b.iter(|| {
                let _ = router.select(*task_class, p);
            });
        });
    }

    group.finish();
}

// ── criterion harness ─────────────────────────────────────────────────────────

criterion_group!(benches, bench_fleet_routing_n10, bench_fleet_routing_n100);
criterion_main!(benches);
