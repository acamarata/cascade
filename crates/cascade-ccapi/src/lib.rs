//! # cascade-ccapi
//!
//! **EXPERIMENTAL — OFF BY DEFAULT — MAY VIOLATE ANTHROPIC ToS.**
//!
//! An HTTP+SSE bridge that, when explicitly enabled via
//! `[experimental] cc_api_proxy = true` in `config.toml`, drives the
//! interactive Claude Code CLI to expose a `/v1/messages`-compatible API.
//!
//! ## Architecture
//!
//! ```text
//! HTTP POST /v1/messages
//!       │
//!       ▼
//!   Bridge (axum)           ←── token-bucket quota guard
//!       │
//!       ▼
//!   ProcessDriver trait
//!       │
//!       ├── MockDriver      ← always compiled, used in tests
//!       │
//!       └── LiveCcDriver    ← compiled only with feature = "live_cc"
//!                             spawns `claude` interactive CLI via PTY/pipe
//!                             NOT tested in CI
//! ```
//!
//! ## Default-off guarantee
//!
//! - [`CcApiConfig::enabled`] defaults to `false`; the bridge never starts
//!   unless both `[experimental] cc_api_proxy = true` AND the caller invokes
//!   `cascade ccapi start`.
//! - The `live_cc` cargo feature is not in `default = []`, so normal builds
//!   never compile or link the PTY driving code.
//!
//! ## Modules
//!
//! | Module | Purpose |
//! |--------|---------|
//! | [`driver`] | `ProcessDriver` trait + `MockDriver` |
//! | [`bridge`] | axum HTTP+SSE server |
//! | [`quota`] | Token-bucket rate limiter |
//! | [`auth`] | CC install/auth detection (wraps cascade-providers) |
//! | [`error`] | Crate-local error type |

pub mod auth;
pub mod bridge;
pub mod driver;
pub mod error;
pub mod quota;

pub use error::CcApiError;

// ── Driver factory ─────────────────────────────────────────────────────────────

/// Try to create the live CC driver.
///
/// Returns `Some(Arc<dyn ProcessDriver>)` when the `live_cc` feature is
/// compiled in (even then, it is a documented stub — see `driver::LiveCcDriver`).
/// Returns `None` when the feature is absent, letting callers handle the
/// "not available" case without using cfg in CLI code.
pub fn make_live_driver() -> Option<std::sync::Arc<dyn driver::ProcessDriver>> {
    #[cfg(feature = "live_cc")]
    {
        Some(std::sync::Arc::new(driver::LiveCcDriver))
    }
    #[cfg(not(feature = "live_cc"))]
    {
        None
    }
}

// ── Top-level convenience: run the bridge ──────────────────────────────────────

/// Start the CC API proxy bridge and serve until the process is killed.
///
/// This is the entry point called by `cascade ccapi start` so that
/// `cascade-cli` does not need to import `axum` directly.
///
/// # Arguments
/// - `driver` — the `ProcessDriver` to use (MockDriver or LiveCcDriver)
/// - `config` — bridge configuration (host, port, quota)
/// - `driver_name` — display name for `/v1/status`
///
/// # Errors
/// Returns `CcApiError::ServerError` if the listener fails to bind.
pub async fn run_bridge(
    driver: std::sync::Arc<dyn driver::ProcessDriver>,
    config: bridge::BridgeConfig,
    driver_name: impl Into<String>,
) -> Result<(), CcApiError> {
    let addr = config.socket_addr();
    let router = bridge::build_router(driver, &config, driver_name);
    let listener = tokio::net::TcpListener::bind(addr)
        .await
        .map_err(|e| CcApiError::ServerError(format!("bind {addr}: {e}")))?;
    axum::serve(listener, router)
        .await
        .map_err(|e| CcApiError::ServerError(e.to_string()))
}
