//! Graceful shutdown for cascaded.
//!
//! Purpose: Provide a `CancellationToken` that fires on SIGTERM/SIGINT
//! (Unix) or SERVICE_CONTROL_STOP (Windows). All long-running tokio tasks
//! accept the token and exit their loops when it fires.
//!
//! State flush: `flush_state` writes `~/.cascade/daemon.pid` (removed) and
//! `~/.cascade/last-stop.txt` (timestamp) so the CLI can distinguish a clean
//! stop from a crash (crash-last.txt written in main.rs for unclean exits).

use std::path::Path;
use tokio_util::sync::CancellationToken;
use tracing::info;

/// Create a new CancellationToken. Pass to `wait_for_signal()` and to all
/// long-running tasks so they share the same cancellation root.
pub fn token() -> CancellationToken {
    CancellationToken::new()
}

/// Resolve once a termination signal is received. Returns immediately if
/// `tokio::signal` is unavailable (e.g., in tests).
pub async fn wait_for_signal() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{signal, SignalKind};
        let mut sigterm =
            signal(SignalKind::terminate()).expect("failed to install SIGTERM handler");
        let mut sigint = signal(SignalKind::interrupt()).expect("failed to install SIGINT handler");
        tokio::select! {
            _ = sigterm.recv() => { info!("SIGTERM received"); }
            _ = sigint.recv()  => { info!("SIGINT received"); }
        }
    }
    #[cfg(windows)]
    {
        tokio::signal::ctrl_c()
            .await
            .expect("failed to install Ctrl-C handler");
        info!("Ctrl-C received");
    }
    #[cfg(not(any(unix, windows)))]
    {
        // Non-blocking fallback for test environments.
        futures::future::pending::<()>().await;
    }
}

/// Write clean-stop sentinel files and remove the PID file.
///
/// Inputs: `config_dir` — `~/.cascade` path.
/// Outputs: writes `last-stop.txt`; removes `daemon.pid`; removes `crash-last.txt`.
/// Constraints: best-effort; errors are logged but not fatal.
///
/// WHY remove crash-last.txt: the crash sentinel is written synchronously at
/// daemon startup (before the async loop) so it is always present if the process
/// is killed. On a clean SIGTERM exit we remove it to distinguish crashes from
/// intentional stops.
pub async fn flush_state(config_dir: &Path) -> Result<(), std::io::Error> {
    // Remove PID file — signals to CLI that daemon stopped cleanly.
    let pid_path = config_dir.join("daemon.pid");
    if pid_path.exists() {
        let _ = tokio::fs::remove_file(&pid_path).await;
    }

    // Remove crash sentinel — written at startup, absent after a clean stop.
    let crash_path = config_dir.join("crash-last.txt");
    if crash_path.exists() {
        let _ = tokio::fs::remove_file(&crash_path).await;
    }

    // Write clean-stop timestamp.
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let stop_path = config_dir.join("last-stop.txt");
    tokio::fs::write(&stop_path, format!("{ts}\n")).await?;

    info!("state flushed");
    Ok(())
}

/// Write the current PID to `~/.cascade/daemon.pid` at startup.
/// The CLI reads this to check whether the daemon is running.
pub async fn write_pid(config_dir: &Path) -> Result<(), std::io::Error> {
    let pid = std::process::id();
    let pid_path = config_dir.join("daemon.pid");
    tokio::fs::write(pid_path, format!("{pid}\n")).await
}
