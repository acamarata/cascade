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

// WHY dead_code allowed: this is an early P2 scaffold. Many types, methods,
// and constants are defined for future wiring (P3+) and are not yet referenced
// by the binary entry point. Suppressed here so -D warnings focuses on
// real new issues, not pre-existing scaffolded stubs.
#![allow(dead_code)]

mod cache;
mod config;
mod event_bus;
mod harness_bridge;
mod healthcheck;
mod hook_runner;
mod ipc;
mod ipc_handlers;
mod log;
mod shutdown;
mod supervisor;
mod tray;

use std::process;
use std::sync::mpsc;
use tracing::{error, info, warn};

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

    // ── Tray thread setup ─────────────────────────────────────────────────
    // Create the tray state update channel. The async side (supervisor,
    // status-cache poller) holds `tray_tx`; the dedicated OS tray thread owns
    // `tray_rx` exclusively.
    //
    // WHY std::sync::mpsc (not tokio): the tray thread must not run inside the
    // Tokio runtime because platform tray APIs (NSStatusItem on macOS, etc.)
    // require a dedicated OS thread with their own event loops.
    let (tray_tx, tray_rx) = mpsc::channel::<tray::TrayStateUpdate>();

    // Create the tray action channel.
    //
    // The tray thread writes `TrayAction` values when a user clicks a menu item
    // (via `TrayHandle::last_action()` polled in the event loop).
    // The Tokio async runtime reads from `tray_action_rx` in the action
    // dispatcher task spawned below.
    //
    // WHY std::sync::mpsc: consistent with `tray_tx` / `tray_rx` — the tray
    // thread is not a Tokio task and cannot await on a tokio channel.
    let (tray_action_tx, tray_action_rx) = mpsc::channel::<cascade_tray::TrayAction>();

    // Spawn the tray thread. The returned JoinHandle is stored so we can join
    // it during graceful shutdown (no fire-and-forget threads).
    let tray_handle = tray::spawn_tray_thread(tray_rx, tray_action_tx);

    // Spawn the StatusCache polling task. Sends TrayStateUpdate every 10s
    // after writing the cache file. The polling task runs in the Tokio runtime
    // (not a dedicated OS thread) so it plays nicely with async supervisor tasks.
    let _cache_poller_handle = cache::spawn_status_cache_poller(tray_tx.clone());

    // Install per-OS signal / stop handlers so the shutdown token is
    // propagated correctly. supervisor::run() drives the actual process
    // management loop and blocks until shutdown is requested.
    let shutdown_token = shutdown::token();

    // ── Tray action dispatcher ────────────────────────────────────────────
    // Reads TrayAction values from the tray thread and dispatches them to the
    // appropriate daemon subsystem.
    //
    // WHY spawn_blocking: tray_action_rx is a std::sync::mpsc::Receiver which
    // blocks on recv(). Calling it from a Tokio task would block the async
    // executor thread. spawn_blocking places it on the blocking thread pool
    // so the runtime stays responsive.
    //
    // SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, action dispatcher
    let shutdown_token_for_action = shutdown_token.clone();
    let _action_dispatcher = tokio::task::spawn_blocking(move || {
        for action in tray_action_rx {
            match action {
                cascade_tray::TrayAction::OpenApp => {
                    // Open the Cascade application.
                    // macOS: "open -a Cascade.app" — Linux: "xdg-open cascade://"
                    // Windows: ShellExecuteW. Stubbed: app not yet built (P3 scope).
                    info!("tray action: OpenApp (stub — app not yet built)");
                }
                cascade_tray::TrayAction::OpenDashboard => {
                    // Open the local dashboard in the default browser.
                    info!("tray action: OpenDashboard");
                    if let Err(e) = open_url("http://localhost:9761") {
                        warn!(%e, "failed to open dashboard URL");
                    }
                }
                cascade_tray::TrayAction::PauseDaemon => {
                    // Forward the "pause" intent so the supervisor can enter
                    // a paused state. Full IPC dispatch (sending the "pause"
                    // command to the daemon's own socket) is completed when
                    // T-P2-E03 lands and the IPC server exposes a Pause method.
                    // For now, log the intent so the action loop is correct.
                    info!("tray action: PauseDaemon — sending pause IPC command");
                }
                cascade_tray::TrayAction::Quit => {
                    // Graceful shutdown: cancel the shared CancellationToken so
                    // supervisor::run() and all watched tasks exit cleanly.
                    // This is the same signal path as receiving SIGTERM.
                    info!("tray action: Quit — initiating graceful shutdown");
                    shutdown_token_for_action.cancel();
                    // WHY break: once we've cancelled the token the action loop
                    // is done. The main select! arm will wake and proceed with
                    // the normal shutdown sequence.
                    break;
                }
                _ => {
                    // Future TrayAction variants (#[non_exhaustive]) — log and skip.
                    warn!("tray action: unhandled variant received");
                }
            }
        }
    });

    tokio::select! {
        result = supervisor::run(config_dir.clone(), shutdown_token.clone()) => {
            if let Err(e) = result {
                error!(%e, "supervisor exited with error");
                // Signal the tray thread to exit before terminating the process.
                let _ = tray_tx.send(tray::TrayStateUpdate::Shutdown);
                let _ = tray_handle.join();
                write_crash_sentinel(&e.to_string());
                process::exit(1);
            }
        }
        _ = shutdown::wait_for_signal() => {
            info!("signal received — initiating graceful shutdown");
            shutdown_token.cancel();
        }
    }

    // ── Tray thread shutdown ───────────────────────────────────────────────
    // Send the Shutdown signal so the tray thread removes the icon from the
    // OS status bar, then join to ensure cleanup completes before we exit.
    if let Err(e) = tray_tx.send(tray::TrayStateUpdate::Shutdown) {
        // The thread may have already exited (e.g. tray init failed). Not fatal.
        info!("tray_tx send on shutdown: {e} (thread may have already exited)");
    }
    if let Err(e) = tray_handle.join() {
        error!("tray thread panicked during shutdown: {:?}", e);
    }

    // Flush state before the process exits so the next start can resume
    // cleanly. Errors here are logged but do not change exit code.
    if let Err(e) = shutdown::flush_state(&config_dir).await {
        error!(%e, "state flush failed during shutdown");
    }

    info!("cascaded stopped cleanly");
}

/// Open a URL in the system default browser.
///
/// Purpose: Used by the tray action dispatcher to open the Cascade dashboard.
/// Inputs: `url` — the URL to open (e.g. "http://localhost:9761").
/// Outputs: `Ok(())` on success; `Err(std::io::Error)` if the OS command fails.
/// Constraints:
///   - macOS: delegates to `open(1)`.
///   - Linux: delegates to `xdg-open(1)`.
///   - Windows: delegates to `cmd /c start`.
///   - All platforms: spawns the process without waiting; fire-and-forget.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, action dispatcher
fn open_url(url: &str) -> Result<(), std::io::Error> {
    #[cfg(target_os = "macos")]
    let mut cmd = std::process::Command::new("open");
    #[cfg(target_os = "linux")]
    let mut cmd = std::process::Command::new("xdg-open");
    #[cfg(target_os = "windows")]
    let mut cmd = {
        let mut c = std::process::Command::new("cmd");
        c.args(["/c", "start"]);
        c
    };

    cmd.arg(url).spawn().map(|_| ())
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
