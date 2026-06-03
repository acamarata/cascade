//! Gemini proxy quota poller — async Rust replacement for the legacy shell poller.
//!
//! Purpose: Poll the Gemini pool proxy (`http://localhost:3761/health`) every
//! `interval_secs` seconds, write the result to `quota-state.json` in the
//! cascade config directory, and publish a `quota.polled` event on the bus.
//! Replaces the shell-based quota polling logic that lived in claw-fleet.
//!
//! Inputs:
//!   - `QuotaPollerConfig` — interval + proxy URL
//!   - `config_dir: PathBuf` — where to write `quota-state.json`
//!   - `bus: SharedBus` — event bus for `quota.polled` events
//!   - `shutdown: CancellationToken` — graceful-stop signal
//!
//! Outputs:
//!   - `quota-state.json` written every interval (even on error — error state has `keys_total=0`)
//!   - One `quota.polled` event published to the bus per successful write
//!
//! Constraints:
//!   - reqwest::Client is constructed ONCE and reused across all poll iterations.
//!   - 10-second per-request timeout; non-200 / timeout / parse errors are
//!     logged as warnings and result in an error-state JSON write — daemon
//!     never crashes due to poller failure.
//!   - tokio::spawn fire-and-forget: supervisor drops the handle; shutdown token
//!     drives the exit, not task completion.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — quota_poller module row (T-P2-E02-09)

use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use crate::config::QuotaPollerConfig;
use crate::config::QuotaStoreConfig;
use crate::event_bus::SharedBus;
use crate::state::DaemonState;
use crate::supervisor::DaemonError;

// ── Types ─────────────────────────────────────────────────────────────────────

/// Parsed shape of the `/health` response from the Gemini proxy.
///
/// Inputs:  JSON body from `GET {proxy_url}/health`
/// Outputs: written verbatim to `quota-state.json`; also the bus event payload.
/// Constraints: all fields are unsigned (no negative quota counts).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct QuotaState {
    /// Total number of Gemini API keys configured in the proxy pool.
    pub keys_total: u32,
    /// Number of keys currently responding without rate-limit errors.
    pub keys_healthy: u32,
    /// Total requests made today across the pool (rolling 24-h window).
    pub requests_today: u32,
    /// Number of keys currently rate-limited (HTTP 429 from Gemini).
    pub rate_limited_keys: u32,
    /// Unix timestamp (seconds) of the last successful health check.
    pub last_checked: i64,
    /// Optional error note written when the proxy is unreachable.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl QuotaState {
    /// Construct an error-state `QuotaState` (all counts zero, error field set).
    fn error_state(reason: impl Into<String>) -> Self {
        Self {
            keys_total: 0,
            keys_healthy: 0,
            requests_today: 0,
            rate_limited_keys: 0,
            last_checked: now_secs(),
            error: Some(reason.into()),
        }
    }
}

// ── Poller ────────────────────────────────────────────────────────────────────

/// Async quota poller task.
///
/// Constructed as a zero-size unit struct; all state is passed into `run()`.
/// Callers spawn `QuotaPoller::run(...)` with `tokio::spawn` (fire-and-forget).
pub struct QuotaPoller;

impl QuotaPoller {
    /// Run the quota poller loop until `shutdown` is cancelled.
    ///
    /// Inputs:
    ///   - `config`                — `QuotaPollerConfig` (interval_secs, proxy_url)
    ///   - `quota_store_config`    — `QuotaStoreConfig` (max_snapshots, path)
    ///   - `config_dir`            — directory where `quota-state.json` is written
    ///   - `bus`                   — shared event bus for `quota.polled` events
    ///   - `daemon_state`          — shared daemon runtime state (quota snapshots)
    ///   - `shutdown`              — cancellation token; loop exits when fired
    ///
    /// Outputs: `Ok(())` always (errors are logged, not propagated — fire-and-forget).
    ///
    /// Constraints:
    ///   - reqwest::Client is built once before the loop (not per-request).
    ///   - Each request has a 10-second timeout.
    ///   - On any error: write error-state JSON, log warn, continue looping.
    ///   - Shutdown: checked via `tokio::select!` before and after each sleep.
    pub async fn run(
        config: QuotaPollerConfig,
        quota_store_config: QuotaStoreConfig,
        config_dir: PathBuf,
        bus: SharedBus,
        daemon_state: Arc<Mutex<DaemonState>>,
        shutdown: CancellationToken,
    ) -> Result<(), DaemonError> {
        // Build the HTTP client once; reuse for every poll iteration.
        let client = match reqwest::Client::builder()
            .timeout(Duration::from_secs(10))
            .build()
        {
            Ok(c) => c,
            Err(e) => {
                warn!(error = %e, "quota_poller: failed to build HTTP client — poller disabled");
                return Ok(());
            }
        };

        let health_url = format!("{}/health", config.proxy_url.trim_end_matches('/'));
        let interval = Duration::from_secs(config.interval_secs);
        let state_path = config_dir.join("quota-state.json");

        info!(
            proxy_url = %config.proxy_url,
            interval_secs = config.interval_secs,
            "quota_poller: starting"
        );

        loop {
            // Poll the proxy and obtain a QuotaState (error-state on failure).
            let state = poll_once(&client, &health_url).await;

            // Write quota-state.json (best-effort; log on error).
            write_state(&state_path, &state).await;

            // Publish event to the bus (best-effort; log on error).
            if let Err(e) = bus
                .publish(
                    "quota.polled",
                    serde_json::to_value(&state).unwrap_or(serde_json::Value::Null),
                )
                .await
            {
                warn!(error = %e, "quota_poller: failed to publish quota.polled event");
            }

            // Record quota snapshot persistently (best-effort; log on error).
            let state_json = serde_json::to_value(&state).unwrap_or(serde_json::Value::Null);
            match bus.record_quota_snapshot(&state_json).await {
                Ok(id) => info!(id, "quota snapshot persisted"),
                Err(e) => warn!(error = %e, "failed to persist quota snapshot"),
            }

            // Push snapshot to the daemon's in-memory ring buffer and aggregate into persistent store.
            let state_json = serde_json::to_value(&state).unwrap_or(serde_json::Value::Null);
            {
                let mut state_guard = daemon_state.lock().unwrap();
                state_guard.push_snapshot(state_json.clone(), quota_store_config.max_snapshots);
                drop(state_guard);
            }

            // Note: The persistent quota store is written by the EventBus in record_quota_snapshot().
            // The daemon_state snapshot_ring holds raw JSON Values for in-memory ring buffering.
            // We don't re-aggregate here; the authoritative quota-store.json is maintained by the
            // EventBus quota_snapshots table. This quota_poller just publishes events and maintains
            // the in-memory ring for the IPC API.

            // Sleep for the configured interval, but wake immediately on shutdown.
            tokio::select! {
                _ = tokio::time::sleep(interval) => {}
                _ = shutdown.cancelled() => {
                    info!("quota_poller: shutdown signal — exiting");
                    break;
                }
            }
        }

        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Perform a single GET /health request and return either the parsed
/// `QuotaState` or an error-state `QuotaState` if the request fails.
///
/// Never panics; always returns a usable `QuotaState`.
async fn poll_once(client: &reqwest::Client, health_url: &str) -> QuotaState {
    match client.get(health_url).send().await {
        Err(e) => {
            warn!(url = health_url, error = %e, "quota_poller: request failed");
            QuotaState::error_state(e.to_string())
        }
        Ok(resp) => {
            let status = resp.status();
            if !status.is_success() {
                warn!(url = health_url, status = %status, "quota_poller: non-200 response");
                return QuotaState::error_state(format!("HTTP {status}"));
            }
            match resp.json::<QuotaState>().await {
                Ok(state) => state,
                Err(e) => {
                    warn!(url = health_url, error = %e, "quota_poller: JSON parse failed");
                    QuotaState::error_state(format!("parse error: {e}"))
                }
            }
        }
    }
}

/// Write `QuotaState` as JSON to `path`. Logs on error, never panics.
async fn write_state(path: &PathBuf, state: &QuotaState) {
    let json = match serde_json::to_vec_pretty(state) {
        Ok(b) => b,
        Err(e) => {
            warn!(error = %e, "quota_poller: failed to serialize QuotaState");
            return;
        }
    };
    if let Err(e) = tokio::fs::write(path, &json).await {
        warn!(path = %path.display(), error = %e, "quota_poller: failed to write quota-state.json");
    }
}

/// Current Unix timestamp in seconds.
fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // Helper: build a minimal EventBus backed by an in-memory SQLite for tests.
    async fn test_bus(config_dir: &std::path::Path) -> SharedBus {
        use crate::config::Config;
        use crate::event_bus::EventBus;
        let cfg = Config::default();
        EventBus::new(config_dir.to_path_buf(), &cfg)
            .await
            .expect("test bus creation")
    }

    /// Unit test: poller hits the mock server, writes quota-state.json, and the
    /// file contains the expected shape from the mock response.
    ///
    /// Acceptance: "Unit test passes with mock HTTP response" (T-P2-E02-09).
    #[tokio::test]
    async fn quota_poller_writes_state_from_mock_response() {
        let tmp = TempDir::new().unwrap();
        let config_dir = tmp.path().to_path_buf();

        // Spin up a mock HTTP server.
        let mock_server = MockServer::start().await;

        // Register a 200 response with known quota data.
        let body = serde_json::json!({
            "keys_total": 28,
            "keys_healthy": 25,
            "requests_today": 142,
            "rate_limited_keys": 3,
            "last_checked": 1717000000i64
        });
        Mock::given(method("GET"))
            .and(path("/health"))
            .respond_with(ResponseTemplate::new(200).set_body_json(&body))
            .mount(&mock_server)
            .await;

        let bus = test_bus(&config_dir).await;
        let shutdown = CancellationToken::new();
        let shutdown_clone = shutdown.clone();

        let config = QuotaPollerConfig {
            enabled: true,
            interval_secs: 3600, // Long interval — we cancel immediately after one poll.
            proxy_url: mock_server.uri(),
        };

        let state_path = config_dir.join("quota-state.json");

        // Set up a mock quota store configuration
        let quota_store_config = QuotaStoreConfig {
            path: config_dir
                .join("quota-store.json")
                .to_str()
                .unwrap()
                .to_string(),
            max_snapshots: 100,
        };
        let daemon_state = Arc::new(Mutex::new(DaemonState::new()));

        // Run one iteration: poll fires, then we cancel before the sleep completes.
        let handle = tokio::spawn(async move {
            QuotaPoller::run(
                config,
                quota_store_config,
                config_dir,
                bus,
                daemon_state,
                shutdown_clone,
            )
            .await
        });

        // Give the poller a moment to perform the first request and write the file.
        tokio::time::sleep(Duration::from_millis(500)).await;
        shutdown.cancel();
        handle
            .await
            .expect("task did not panic")
            .expect("run returned Ok");

        // Assert quota-state.json exists and has the correct shape.
        assert!(
            state_path.exists(),
            "quota-state.json was not written by the poller"
        );
        let raw = std::fs::read_to_string(&state_path).expect("read quota-state.json");
        let parsed: QuotaState = serde_json::from_str(&raw).expect("parse quota-state.json");

        assert_eq!(parsed.keys_total, 28, "keys_total mismatch");
        assert_eq!(parsed.keys_healthy, 25, "keys_healthy mismatch");
        assert_eq!(parsed.requests_today, 142, "requests_today mismatch");
        assert_eq!(parsed.rate_limited_keys, 3, "rate_limited_keys mismatch");
        assert_eq!(parsed.last_checked, 1717000000, "last_checked mismatch");
        assert!(
            parsed.error.is_none(),
            "unexpected error field: {:?}",
            parsed.error
        );
    }

    /// Unit test: when the proxy is not running (connection refused), the poller
    /// writes an error-state JSON with keys_total=0 and an error note — daemon
    /// must NOT crash.
    ///
    /// Acceptance: "When proxy is not running, quota-state.json is written with
    /// keys_total=0 and an error note" (T-P2-E02-09).
    #[tokio::test]
    async fn quota_poller_writes_error_state_when_proxy_unreachable() {
        let tmp = TempDir::new().unwrap();
        let config_dir = tmp.path().to_path_buf();

        let bus = test_bus(&config_dir).await;
        let shutdown = CancellationToken::new();
        let shutdown_clone = shutdown.clone();

        // Use a port that nothing is listening on.
        let config = QuotaPollerConfig {
            enabled: true,
            interval_secs: 3600,
            proxy_url: "http://127.0.0.1:19761".into(), // deliberately unreachable
        };

        let state_path = config_dir.join("quota-state.json");

        // Set up a mock quota store configuration
        let quota_store_config = QuotaStoreConfig {
            path: config_dir
                .join("quota-store.json")
                .to_str()
                .unwrap()
                .to_string(),
            max_snapshots: 100,
        };
        let daemon_state = Arc::new(Mutex::new(DaemonState::new()));

        let handle = tokio::spawn(async move {
            QuotaPoller::run(
                config,
                quota_store_config,
                config_dir,
                bus,
                daemon_state,
                shutdown_clone,
            )
            .await
        });

        tokio::time::sleep(Duration::from_millis(500)).await;
        shutdown.cancel();
        handle
            .await
            .expect("task did not panic")
            .expect("run returned Ok");

        assert!(
            state_path.exists(),
            "quota-state.json should be written even on proxy failure"
        );
        let raw = std::fs::read_to_string(&state_path).expect("read quota-state.json");
        let parsed: QuotaState = serde_json::from_str(&raw).expect("parse quota-state.json");

        assert_eq!(parsed.keys_total, 0, "keys_total should be 0 on error");
        assert!(
            parsed.error.is_some(),
            "error field should be set when proxy is unreachable"
        );
    }

    /// Unit test: QuotaState round-trips through JSON correctly.
    #[test]
    fn quota_state_serializes_correctly() {
        let state = QuotaState {
            keys_total: 10,
            keys_healthy: 8,
            requests_today: 50,
            rate_limited_keys: 2,
            last_checked: 1000,
            error: None,
        };
        let json = serde_json::to_string(&state).unwrap();
        let back: QuotaState = serde_json::from_str(&json).unwrap();
        assert_eq!(state, back);
        // error field must not appear when None (skip_serializing_if)
        assert!(
            !json.contains("error"),
            "error key should be absent when None"
        );
    }
}
