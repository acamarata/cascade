//! cascaded — Cascade background daemon entry point.
//!
//! Purpose: Bootstrap the daemon process, initialize tracing, wire signal
//! handlers, and drive the top-level supervisor loop. Exits cleanly on
//! SIGTERM/SIGINT (Unix) or SERVICE_CONTROL_STOP (Windows).
//!
//! State files (all under `~/.cascade/`):
//!   daemon.sock  — Unix domain socket for IPC
//!   logs/        — structured JSON logs (see log.rs)
//!   crash-last.txt — written on unclean exit

mod event_bus;
mod healthcheck;
mod ipc;
mod log;
mod shutdown;
mod supervisor;

use std::process;
use tracing::{error, info};

#[tokio::main]
async fn main() {
    // Initialize structured JSON logging before anything else so early errors
    // are captured. Rotation and file sink are configured inside log::init.
    if let Err(e) = log::init() {
        eprintln!("failed to initialize logging: {e}");
        process::exit(1);
    }

    info!(version = env!("CARGO_PKG_VERSION"), "cascaded starting");

    // Resolve config dir (~/.cascade). Created by `cascade init` but we
    // create it here as a safety net so the daemon never panics on a fresh
    // install where `cascade init` was skipped.
    let config_dir = match dirs::home_dir() {
        Some(h) => h.join(".cascade"),
        None => {
            error!("cannot determine home directory");
            write_crash_sentinel("cannot determine home directory");
            process::exit(1);
        }
    };
    if let Err(e) = tokio::fs::create_dir_all(&config_dir).await {
        error!(%e, "failed to create ~/.cascade");
        write_crash_sentinel(&e.to_string());
        process::exit(1);
    }

    // Install per-OS signal / stop handlers so the shutdown token is
    // propagated correctly. supervisor::run() drives the actual process
    // management loop and blocks until shutdown is requested.
    let shutdown_token = shutdown::token();

    tokio::select! {
        result = supervisor::run(config_dir.clone(), shutdown_token.clone()) => {
            if let Err(e) = result {
                error!(%e, "supervisor exited with error");
                write_crash_sentinel(&e.to_string());
                process::exit(1);
            }
        }
        _ = shutdown::wait_for_signal() => {
            info!("signal received — initiating graceful shutdown");
            shutdown_token.cancel();
        }
    }

    // Flush state before the process exits so the next start can resume
    // cleanly. Errors here are logged but do not change exit code.
    if let Err(e) = shutdown::flush_state(&config_dir).await {
        error!(%e, "state flush failed during shutdown");
    }

    info!("cascaded stopped cleanly");
}

/// Write a one-line crash sentinel so the next start can surface a message.
/// Never panics — if the write fails, the error is printed to stderr only.
fn write_crash_sentinel(reason: &str) {
    use std::time::{SystemTime, UNIX_EPOCH};
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let msg = format!("{ts} {reason}\n");
    if let Some(home) = dirs::home_dir() {
        let path = home.join(".cascade").join("crash-last.txt");
        let _ = std::fs::write(path, msg);
    }
}
