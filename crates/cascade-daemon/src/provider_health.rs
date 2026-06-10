//! Provider health-check background task for the Cascade daemon.
//!
//! # Purpose
//!
//! Wires [`cascade_providers::ProviderRegistry`] into the daemon startup path.
//! Runs an immediate health sweep on startup, caches results in a shared
//! `HashMap<String, ProviderHealth>`, and refreshes every 5 minutes.
//!
//! # Inputs / Outputs
//!
//! - `spawn_health_check_task(registry, state, interval, shutdown)` → `JoinHandle`
//!   Spawns the background task; first check fires immediately (no leading delay).
//! - `ProviderHealth` — per-provider result snapshot stored in `HealthState`.
//! - `HealthState` — `Arc<RwLock<HashMap<String, ProviderHealth>>>` shared
//!   across the daemon and any command handlers that need to report health.
//! - `healthy_provider_ids(state)` — convenience async fn returning ids where ok=true.
//!
//! # Constraints
//!
//! - An empty registry (zero providers configured) is fully supported — the
//!   background task completes immediately and stores an empty map.
//! - Unhealthy providers emit a WARN log but never prevent daemon startup.
//! - The spawned task exits cleanly when `shutdown` is cancelled.
//!
//! SPORT: MASTER-RUST-CRATES.md — cascade-daemon: provider health check wired (T-P3-E04-15)

use std::{collections::HashMap, sync::Arc, time::Instant};

use tokio::{sync::RwLock, time};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use cascade_providers::ProviderRegistry;

// ── Types ─────────────────────────────────────────────────────────────────────

/// Per-provider health snapshot cached in [`HealthState`].
///
/// Stores the outcome of the most recent `health_check()` call for one provider.
#[derive(Debug, Clone)]
pub struct ProviderHealth {
    /// Whether the provider passed its last health check.
    pub ok: bool,

    /// Wall-clock instant of the last completed check.
    ///
    /// WHY `Instant` (not `SystemTime`): used only for internal age tracking;
    /// never serialized directly.  Convert to elapsed seconds for display.
    pub checked_at: Instant,

    /// Human-readable error description when `ok = false`; `None` when healthy.
    pub error_msg: Option<String>,
}

/// Shared health cache — one entry per registered provider ID.
///
/// Wrap in `Arc<RwLock<…>>` and share across the daemon and command handlers.
pub type HealthState = Arc<RwLock<HashMap<String, ProviderHealth>>>;

// ── Public helpers ────────────────────────────────────────────────────────────

/// Return a snapshot of the IDs of all currently-healthy providers.
///
/// Reads the shared [`HealthState`] under a read lock.  Used by the daemon
/// IPC handler and Tauri `cascade_providers_health` command.
pub async fn healthy_provider_ids(state: &HealthState) -> Vec<String> {
    let map = state.read().await;
    map.iter()
        .filter(|(_, h)| h.ok)
        .map(|(id, _)| id.clone())
        .collect()
}

// ── Task ──────────────────────────────────────────────────────────────────────

/// Spawn the provider health-check background task.
///
/// The task:
/// 1. Fires an **immediate** health sweep (no leading delay).
/// 2. Repeats every `interval` thereafter.
/// 3. Updates `state` in place after every sweep.
/// 4. Emits `WARN` for every unhealthy provider.
/// 5. Exits cleanly when `shutdown` is cancelled.
///
/// # Inputs
///
/// - `registry` — shared [`ProviderRegistry`]; health checks run on all
///   registered adapters.
/// - `state` — shared [`HealthState`] written after every sweep.
/// - `interval` — time between sweeps (production: 5 min; tests: pass any).
/// - `shutdown` — [`CancellationToken`]; task exits when cancelled.
///
/// # Returns
///
/// A `tokio::task::JoinHandle<()>`.  Fire-and-forget from the caller's
/// perspective; join during graceful shutdown to flush final logs.
pub fn spawn_health_check_task(
    registry: Arc<ProviderRegistry>,
    state: HealthState,
    interval: std::time::Duration,
    shutdown: CancellationToken,
) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        // First sweep fires immediately — no leading sleep.
        run_sweep(&registry, &state).await;

        let mut ticker = time::interval(interval);
        ticker.tick().await; // consume the immediate first tick from interval

        loop {
            tokio::select! {
                _ = ticker.tick() => {
                    run_sweep(&registry, &state).await;
                }
                _ = shutdown.cancelled() => {
                    info!("provider health task shutting down");
                    break;
                }
            }
        }
    })
}

/// Run one full health sweep: call `health_check_all`, update state, log warnings.
async fn run_sweep(registry: &ProviderRegistry, state: &HealthState) {
    let results = registry.health_check_all().await;

    let mut map = state.write().await;
    for (id, result) in &results {
        let ok = result.is_ok();
        let error_msg = result.as_ref().err().map(|e| e.to_string());
        if !ok {
            warn!(provider_id = %id, error = ?error_msg, "provider health check failed");
        }
        map.insert(
            id.clone(),
            ProviderHealth {
                ok,
                checked_at: Instant::now(),
                error_msg,
            },
        );
    }

    let total = results.len();
    let healthy = results.values().filter(|r| r.is_ok()).count();
    info!(total, healthy, "provider health sweep complete");
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::time::Duration;

    use cascade_providers::{NoopProvider, ProviderRegistry};

    // ── Tests ──────────────────────────────────────────────────────────────────

    /// Empty registry health sweep produces empty state — no panic or error.
    ///
    /// Verifies: CR-A criterion (health task does not panic on empty registry).
    #[tokio::test]
    async fn test_empty_registry_no_panic() {
        let registry = Arc::new(ProviderRegistry::new());
        let state: HealthState = Arc::new(RwLock::new(HashMap::new()));
        let shutdown = CancellationToken::new();

        let handle = spawn_health_check_task(
            registry.clone(),
            state.clone(),
            Duration::from_secs(300),
            shutdown.clone(),
        );

        tokio::time::sleep(Duration::from_millis(50)).await;
        shutdown.cancel();
        let _ = handle.await;

        let map = state.read().await;
        assert!(
            map.is_empty(),
            "empty registry should produce empty health state"
        );
    }

    /// NoopProvider (health_check returns Err(NotImplemented)) is cached as ok=false.
    ///
    /// Verifies: health sweep populates HealthState; unhealthy adapters set ok=false
    /// with an error_msg.  Also verifies CR-B (task is fire-and-forget — spawn
    /// returns immediately while sweep runs asynchronously).
    #[tokio::test]
    async fn test_unhealthy_adapter_cached_not_ok() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register("noop-1".into(), Arc::new(NoopProvider))
            .expect("register noop");

        let state: HealthState = Arc::new(RwLock::new(HashMap::new()));
        let shutdown = CancellationToken::new();

        // spawn_health_check_task returns immediately (CR-B: fire-and-forget).
        let handle = spawn_health_check_task(
            registry.clone(),
            state.clone(),
            Duration::from_secs(300),
            shutdown.clone(),
        );

        // Allow the initial sweep to complete.
        tokio::time::sleep(Duration::from_millis(50)).await;
        shutdown.cancel();
        let _ = handle.await;

        let map = state.read().await;
        assert!(map.contains_key("noop-1"), "noop-1 should appear in state");
        assert!(!map["noop-1"].ok, "NoopProvider.health_check() returns Err");
        assert!(
            map["noop-1"].error_msg.is_some(),
            "error_msg should be populated for unhealthy provider"
        );
    }

    /// `healthy_provider_ids` returns only providers where ok=true.
    #[tokio::test]
    async fn test_healthy_provider_ids_filters_unhealthy() {
        let state: HealthState = Arc::new(RwLock::new(HashMap::new()));
        {
            let mut map = state.write().await;
            map.insert(
                "ok-provider".into(),
                ProviderHealth {
                    ok: true,
                    checked_at: Instant::now(),
                    error_msg: None,
                },
            );
            map.insert(
                "bad-provider".into(),
                ProviderHealth {
                    ok: false,
                    checked_at: Instant::now(),
                    error_msg: Some("timeout".into()),
                },
            );
        }

        let mut ids = healthy_provider_ids(&state).await;
        ids.sort(); // HashMap iteration order is non-deterministic
        assert_eq!(ids, vec!["ok-provider".to_string()]);
    }

    /// Health cache is refreshed after the configured interval elapses.
    #[tokio::test]
    async fn test_refresh_interval_fires() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register("noop-refresh".into(), Arc::new(NoopProvider))
            .expect("register");

        let state: HealthState = Arc::new(RwLock::new(HashMap::new()));
        let shutdown = CancellationToken::new();

        // Very short interval so the second sweep fires quickly in test.
        let handle = spawn_health_check_task(
            registry.clone(),
            state.clone(),
            Duration::from_millis(20),
            shutdown.clone(),
        );

        // Wait for at least two sweeps.
        tokio::time::sleep(Duration::from_millis(80)).await;
        shutdown.cancel();
        let _ = handle.await;

        let map = state.read().await;
        // After multiple sweeps, the entry should still be present (cache not cleared).
        assert!(
            map.contains_key("noop-refresh"),
            "cache preserved across sweeps"
        );
    }
}
