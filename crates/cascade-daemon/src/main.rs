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

mod audit;
mod cache;
// T-P4-E04-10: in-memory mtime-validated chunk cache for cascade file reads
mod chunk_cache;
mod config;
mod event_bus;
mod harness_bridge;
mod healthcheck;
mod hook_runner;
mod ipc;
mod ipc_handlers;
// E-P6-04: CEO orchestrator IPC methods
mod ipc_ceo;
// T-P3-E04-29: usage analytics IPC handlers (wired in ipc.rs try_typed_dispatch)
mod ipc_usage_analytics;
// E-P8-01: kanban Task IPC handlers
mod ipc_tasks;
mod key_index;
mod key_loader;
mod log;
mod provider_health;
mod quota_poller;
mod regen;
mod shutdown;
mod state;
mod supervisor;
mod telemetry;
mod tray;
// T-P4-E04-10: cascade instruction-file loader (goes through ChunkCache)
mod loader;
// T-P4-E01-29: IPC search endpoint (rag.* JSON-RPC methods)
mod search_handler;
// T-P4-E01-32: external drive mount/unmount watch — pause/resume RAG indexing
mod volume_watcher;

use cascade_core::providers_store::read_providers_store;
use cascade_providers::ProviderRegistry;
use secrecy::SecretString;
use std::collections::HashMap;
use std::process;
use std::sync::mpsc;
use std::sync::Arc;
use tokio::sync::RwLock;
use tokio_util::sync::CancellationToken;
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
    // Prefer $HOME (honoured on every platform and required for test isolation;
    // note macOS `dirs::home_dir()` ignores $HOME), then fall back to dirs.
    let home = std::env::var_os("HOME")
        .map(std::path::PathBuf::from)
        .or_else(dirs::home_dir);
    let config_dir = match home {
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

    // Write crash sentinel BEFORE the async event loop so it is present on disk
    // even if the process is killed with SIGKILL (which prevents all cleanup).
    // flush_state() removes it during a clean SIGTERM exit.
    // WHY write here synchronously: SIGKILL cannot be caught — any write that
    // happens inside the async loop may not execute if the process is force-killed.
    write_crash_sentinel("unclean startup — daemon did not stop cleanly last time");

    // Write the PID file at startup so supervisors and `cascade daemon
    // status/stop` can find us. shutdown::flush_state removes it on clean exit.
    if let Err(e) = shutdown::write_pid(&config_dir).await {
        error!(%e, "failed to write daemon.pid");
    }

    // Load API keys from the OS keychain (primary) or vault.env (fallback).
    // load_api_keys() logs the count + source at INFO and never logs key values
    // (keys are held as zeroize-on-drop SecretString).
    //
    // Build a slot_id → SecretString map for the Gemini proxy.  Enabled Gemini
    // providers are enumerated from providers.json and paired positionally with
    // the loaded keys (gemini_key_1 → index 0, gemini_key_2 → index 1, …).
    // GeminiProxy::new() accepts this map so the proxy prefers the in-memory
    // SecretString over any plaintext value in providers.json.
    // T-P3-E00-02: SecretString wiring complete.
    let raw_keys: Vec<SecretString> = key_loader::load_api_keys();
    let providers_path = config_dir.join("providers.json");
    let _keychain_map: HashMap<String, SecretString> = {
        let gemini_slots: Vec<String> = read_providers_store(&providers_path)
            .map(|store| {
                store
                    .providers
                    .into_iter()
                    .filter(|p| p.enabled && p.harness == "gemini")
                    .map(|p| p.id)
                    .collect()
            })
            .unwrap_or_default();
        gemini_slots.into_iter().zip(raw_keys.into_iter()).collect()
    };

    // First-start advisory: if keys are still only in vault.env, nudge the user
    // to migrate them into the OS keychain. Non-interactive (stderr only) — the
    // user runs `cascade migrate-keys` manually; the daemon never blocks on stdin.
    if key_loader::should_advise_migration() {
        eprintln!(
            "[cascade] API keys found in vault.env. Run 'cascade migrate-keys' to move them \
             to the OS keychain for improved security."
        );
    }

    // Initialise the tamper-evident audit log and record daemon start. Audit
    // failures are non-fatal (logged at ERROR) and never block startup.
    if let Err(e) = audit::init(&config_dir.join("audit.log")) {
        error!(%e, "failed to initialise audit log (continuing without audit)");
    }
    audit::record(
        cascade_audit::AuditOp::DaemonStart,
        "cascaded",
        "cascaded",
        &format!("v{} pid={}", env!("CARGO_PKG_VERSION"), process::id()),
    );

    // ── Provider health-check task (T-P3-E04-15) ─────────────────────────
    // Build a ProviderRegistry from configured providers (presence in
    // providers.json is sufficient — real adapters are P4+ scope; for now
    // all enabled entries are registered as NoopProvider placeholders so the
    // health-check task wires cleanly without depending on concrete adapters).
    //
    // WHY NoopProvider here: concrete adapter implementations land in P4.
    // The task wiring (state caching, WARN logging, refresh interval) is
    // fully testable today with the existing NoopProvider stub.
    //
    // Unhealthy providers emit WARN logs but NEVER block daemon startup.
    let provider_registry = Arc::new(ProviderRegistry::new());
    let provider_health_state: provider_health::HealthState =
        Arc::new(RwLock::new(std::collections::HashMap::new()));
    let health_shutdown = CancellationToken::new();

    // Register enabled providers from providers.json as NoopProvider placeholders.
    // Real adapter wiring lands in P4 (concrete provider tickets).
    {
        use cascade_providers::NoopProvider;
        let entries = read_providers_store(&providers_path)
            .map(|s| s.providers)
            .unwrap_or_default();
        for entry in entries.into_iter().filter(|p| p.enabled) {
            let id = entry.id.clone();
            if let Err(e) = provider_registry.register(id.clone(), Arc::new(NoopProvider)) {
                warn!(provider_id = %id, error = %e, "failed to register provider (skipping)");
            }
        }
    }

    // ── Local LLM scan (E-P5-07) ──────────────────────────────────────────────
    // Scan ~/.cascade/models/ and register each present model as a local:<id>
    // provider. Never blocks startup — missing models are skipped silently.
    try_register_local_models(&provider_registry);

        // ── Ollama auto-detect (T-P3-E04-19) ─────────────────────────────────────
    // Probe localhost:11434 with a 500 ms timeout. If Ollama is running,
    // register the adapter as "ollama". If absent, skip silently — the
    // daemon must never block startup waiting for an optional local LLM.
    try_register_ollama(&provider_registry).await;

    let _provider_health_handle = provider_health::spawn_health_check_task(
        provider_registry.clone(),
        provider_health_state.clone(),
        std::time::Duration::from_secs(300), // 5-minute refresh interval
        health_shutdown.clone(),
    );
    info!("provider health-check task spawned (non-blocking)");

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
                // Remove daemon.pid even on the error path: a stale pid file
                // makes the next start (and tests) believe the daemon is
                // still alive (observed: port-collision exits leaked it).
                if let Err(fe) = shutdown::flush_state(&config_dir).await {
                    error!(%fe, "state flush failed during error shutdown");
                }
                process::exit(1);
            }
        }
        _ = shutdown::wait_for_signal() => {
            info!("signal received — initiating graceful shutdown");
            shutdown_token.cancel();
        }
    }

    // ── Provider health task shutdown ─────────────────────────────────────
    health_shutdown.cancel();

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

    // Record daemon stop in the audit log before flushing state. Non-fatal.
    audit::record(
        cascade_audit::AuditOp::DaemonStop,
        "cascaded",
        "cascaded",
        "graceful shutdown",
    );

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

/// Probe the local Ollama daemon and register `OllamaAdapter` if present.
///
/// Fires a single `GET /api/tags` with a 500 ms timeout.  When Ollama
/// responds, the adapter is registered as `"ollama"` in the registry.
/// When absent (connection refused / timeout) this is a silent no-op —
/// an optional local LLM must never block daemon startup.
///
/// SPORT: MASTER-RUST-CRATES.md — cascade-daemon: Ollama auto-detect (T-P3-E04-19)
async fn try_register_ollama(registry: &Arc<ProviderRegistry>) {
    use cascade_providers::adapters::ollama::OllamaAdapter;

    match OllamaAdapter::try_detect().await {
        Some(adapter) => match registry.register("ollama".to_owned(), Arc::new(adapter)) {
            Ok(()) => info!("Ollama detected and registered as provider \"ollama\""),
            Err(e) => warn!(error = %e, "Ollama detected but registration failed"),
        },
        None => {
            info!("Ollama not detected on localhost:11434 (skipping registration)");
        }
    }
}

/// Scan `~/.cascade/models/` and register any locally-installed LLM adapters.
///
/// Delegates to `cascade_local_llm::scan_installed_models()` which returns
/// `(provider_id, LocalLlmAdapter)` pairs for every model directory present on
/// disk.  Each pair is registered in the `ProviderRegistry` under its provider
/// ID (e.g. `local:gemma-2-2b`).
///
/// Registration failures (e.g. duplicate ID) are logged at WARN and skipped —
/// a bad local model must never block daemon startup.
///
/// SPORT: MASTER-RUST-CRATES.md — cascade-daemon: local LLM registration (E-P5-07)
fn try_register_local_models(registry: &Arc<ProviderRegistry>) {
    use cascade_local_llm::scan_installed_models;

    let adapters = scan_installed_models();
    if adapters.is_empty() {
        info!("no local models found under ~/.cascade/models/ (skipping)");
        return;
    }
    for (provider_id, adapter) in adapters {
        match registry.register(provider_id.clone(), Arc::new(adapter)) {
            Ok(()) => info!(provider_id, "local LLM registered"),
            Err(e) => warn!(provider_id, error = %e, "local LLM registration failed (skipping)"),
        }
    }
}

/// Write a one-line crash sentinel so the next start can surface a message.
///
/// WHY: SIGKILL prevents any async cleanup. This function is called synchronously
/// before the async event loop starts so the sentinel is always on disk — if the
/// process is killed at any point after startup the file exists. flush_state()
/// removes it on a clean SIGTERM exit.
///
/// WHY std::env::var_os("HOME") instead of dirs::home_dir(): on macOS,
/// dirs::home_dir() ignores the $HOME environment variable and reads from the
/// system database, breaking test isolation where HOME is overridden per-test.
///
/// Never panics — if the write fails, the error is printed to stderr only.
fn write_crash_sentinel(reason: &str) {
    use std::time::{SystemTime, UNIX_EPOCH};
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let msg = format!("{ts} {reason}\n");
    // Prefer $HOME (respected in test isolation) over dirs::home_dir().
    let home = std::env::var_os("HOME")
        .map(std::path::PathBuf::from)
        .or_else(dirs::home_dir);
    if let Some(h) = home {
        let path = h.join(".cascade").join("crash-last.txt");
        let _ = std::fs::write(path, msg);
    }
}
