//! `cascade ccapi` — manage the CC API proxy bridge (E-P9-06).
//!
//! # Purpose
//! Control the EXPERIMENTAL Claude Code API proxy. The bridge is **disabled
//! by default** and must be explicitly enabled in `config.toml` before any
//! subcommand other than `status` will start the bridge.
//!
//! # Subcommands
//!
//! | Subcommand | Behaviour when flag=OFF | Behaviour when flag=ON |
//! |---|---|---|
//! | `status` | Reports "disabled" + points to doc | Reports not-running |
//! | `start` | Prints disabled message + exits 1 | Starts bridge on 127.0.0.1:7190 |
//! | `stop` | Prints disabled message + exits 1 | Stops bridge if running |
//!
//! # Safety
//! The bridge is never auto-started. It requires both:
//! 1. `[experimental] cc_api_proxy = true` in `~/.cascade/config.toml`.
//! 2. An explicit `cascade ccapi start` invocation.
//!
//! # Risk doc
//! `.github/docs/cc-api-proxy-beta.md`
//!
//! # Constraints
//! - Default-off enforced by config check before any start action.
//! - No background daemon integration — the bridge runs in the foreground
//!   (Ctrl-C to stop) or the user passes `--detach` (TBD future).

use async_trait::async_trait;
use clap::{Args, Subcommand};

use cascade_ccapi::{
    auth,
    bridge::BridgeConfig,
    driver::MockDriver,
    make_live_driver,
    quota::QuotaGuard,
    run_bridge,
};
use cascade_types::error::{CascadeError, Result};
use std::sync::Arc;

use super::Command;

// ── Risk warning text ─────────────────────────────────────────────────────────

const DISABLED_MSG: &str = "\
cascade ccapi is DISABLED (experimental, off by default).

This feature drives the interactive Claude Code CLI as an HTTP API proxy.
It may violate the Anthropic Claude Code Terms of Service and can break
without notice when Claude Code is updated.

To enable (at your own risk):
  1. Read .github/docs/cc-api-proxy-beta.md in full.
  2. Add the following to ~/.cascade/config.toml:

     [experimental]
     cc_api_proxy = true

  3. Re-run: cascade ccapi start";

const RISK_REMINDER: &str =
    "WARNING: CC API proxy is EXPERIMENTAL. It may violate Anthropic ToS. \
     See .github/docs/cc-api-proxy-beta.md";

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade ccapi`.
#[derive(Debug, Args)]
pub struct CcApiArgs {
    #[command(subcommand)]
    pub subcommand: CcApiSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum CcApiSubcmd {
    /// Report the CC API proxy status (enabled/disabled, running/stopped).
    Status(CcApiStatusArgs),
    /// Start the CC API proxy bridge (requires cc_api_proxy=true in config).
    Start(CcApiStartArgs),
    /// Stop the CC API proxy bridge.
    Stop(CcApiStopArgs),
}

#[derive(Debug, Args)]
pub struct CcApiStatusArgs {
    /// Output as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Debug, Args)]
pub struct CcApiStartArgs {
    /// TCP port (default: 7190).
    #[arg(long, default_value = "7190")]
    pub port: u16,
    /// Requests per minute quota (default: 20).
    #[arg(long, default_value = "20")]
    pub rpm: u32,
    /// Use MockDriver instead of live CC (for local testing without real CC).
    #[arg(long, hide = true)]
    pub mock: bool,
}

#[derive(Debug, Args)]
pub struct CcApiStopArgs;

// ── Dispatch ──────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for CcApiArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            CcApiSubcmd::Status(a) => a.run().await,
            CcApiSubcmd::Start(a) => a.run().await,
            CcApiSubcmd::Stop(a) => a.run().await,
        }
    }
}

// ── cc_api_proxy config flag ──────────────────────────────────────────────────

/// Check if the cc_api_proxy experimental flag is enabled in config.
///
/// Reads `~/.cascade/config.toml`. If the file doesn't exist or fails to
/// parse, returns `false` (safe default).
fn is_cc_api_proxy_enabled() -> bool {
    let Some(home) = dirs::home_dir() else { return false };
    let config_path = home.join(".cascade").join("config.toml");
    let Ok(contents) = std::fs::read_to_string(&config_path) else {
        return false;
    };
    let Ok(config) = toml::from_str::<cascade_types::config::CascadeConfig>(&contents) else {
        return false;
    };
    config.experimental.cc_api_proxy
}

// ── Status ────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for CcApiStatusArgs {
    async fn run(&self) -> Result<()> {
        let enabled = is_cc_api_proxy_enabled();
        let cc_status = auth::detect();

        if self.json {
            let json = serde_json::json!({
                "cc_api_proxy_enabled": enabled,
                "cc_installed": cc_status.installed,
                "cc_authenticated": cc_status.authenticated,
                "cc_account_hint": cc_status.account_hint,
                "bridge_running": false,   // TODO: PID check in future iteration
                "risk_doc": ".github/docs/cc-api-proxy-beta.md"
            });
            println!("{}", serde_json::to_string_pretty(&json).unwrap());
        } else {
            println!("CC API Proxy: {}", if enabled { "ENABLED (experimental)" } else { "disabled" });
            println!("Claude Code : {}", if cc_status.installed { "installed" } else { "not found" });
            println!("CC auth     : {}", if cc_status.authenticated { "authenticated" } else { "not authenticated" });
            if let Some(hint) = &cc_status.account_hint {
                println!("CC account  : {hint}");
            }
            println!("Bridge      : not running");
            if !enabled {
                println!();
                println!("{DISABLED_MSG}");
            }
        }
        Ok(())
    }
}

// ── Start ─────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for CcApiStartArgs {
    async fn run(&self) -> Result<()> {
        // Guard: feature must be explicitly enabled.
        if !self.mock && !is_cc_api_proxy_enabled() {
            eprintln!("{DISABLED_MSG}");
            return Err(CascadeError::Other(
                "cc_api_proxy is disabled; enable it in config.toml first".into(),
            ));
        }

        eprintln!("{RISK_REMINDER}");

        // Check CC install + auth (skip for mock mode).
        if !self.mock {
            let cc_status = auth::detect();
            if !cc_status.installed {
                return Err(CascadeError::Other(
                    "Claude Code CLI (`claude`) is not installed".into(),
                ));
            }
            if !cc_status.authenticated {
                return Err(CascadeError::Other(
                    "Claude Code is not authenticated; run `claude` interactively first".into(),
                ));
            }
        }

        // Build the driver.
        // When --mock is set (for local testing), use MockDriver.
        // Otherwise, try to get the LiveCcDriver (only available with feature=live_cc).
        // make_live_driver() returns None when live_cc is not compiled in.
        let driver: Arc<dyn cascade_ccapi::driver::ProcessDriver> = if self.mock {
            eprintln!("Using MockDriver (--mock flag set)");
            Arc::new(MockDriver::with_response(
                "[mock response] CC API proxy is running in mock mode",
            ))
        } else {
            match make_live_driver() {
                Some(d) => d,
                None => {
                    eprintln!(
                        "ERROR: live_cc feature is not compiled in.\n\
                         The real PTY driver requires `--features live_cc`.\n\
                         Use `--mock` for local testing.\n\
                         See crates/cascade-ccapi/src/driver.rs § LiveCcDriver for status."
                    );
                    return Err(CascadeError::Other(
                        "live_cc feature not enabled; rebuild cascade-ccapi with --features live_cc or use --mock"
                            .into(),
                    ));
                }
            }
        };

        let driver_label = if self.mock { "MockDriver" } else { "LiveCcDriver" };
        let config = BridgeConfig {
            host: "127.0.0.1".into(),
            port: self.port,
            quota: QuotaGuard::new(self.rpm),
        };

        let addr = config.socket_addr();
        println!("cascade ccapi bridge listening on http://{addr}");
        println!("  POST /v1/messages  — Anthropic Messages API (SSE stream)");
        println!("  GET  /v1/status    — bridge health");
        println!("  Quota: {rpm} req/min", rpm = self.rpm);
        println!("  Press Ctrl-C to stop.");

        run_bridge(driver, config, driver_label)
            .await
            .map_err(|e| CascadeError::Other(format!("bridge error: {e}")))?;

        Ok(())
    }
}

// ── Stop ──────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for CcApiStopArgs {
    async fn run(&self) -> Result<()> {
        if !is_cc_api_proxy_enabled() {
            eprintln!("{DISABLED_MSG}");
            return Err(CascadeError::Other(
                "cc_api_proxy is disabled".into(),
            ));
        }
        // The bridge runs in the foreground — there is no separate PID to stop.
        // Future: check a PID file written by `cascade ccapi start --detach`.
        println!("No background CC API proxy bridge is running.");
        println!("(The bridge runs in the foreground. Use Ctrl-C to stop it.)");
        Ok(())
    }
}
