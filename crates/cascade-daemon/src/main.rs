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
// T-P3-E04-26: provider IPC command handlers
mod ipc_providers;
// T-P3-E04-27: token usage accumulator
mod usage;
// T-P3-E04-29: usage analytics IPC handlers (wired in ipc.rs try_typed_dispatch)
mod ipc_usage_analytics;
// E-P8-01: kanban Task IPC handlers
mod ipc_tasks;
// E-P6-02 T-04: round-robin account rotation selector
mod rotation_selector;
// E-P6-02 T-05: hard-stop per-window budget guard
mod budget_guard;
// E-P6-03 v1.2: fleet poller loop — periodic quota-store.json refresh
mod fleet_poller;
// live Claude Max usage fetcher (fetch_claude_usage + parse_usage_response)
mod claude_usage;
mod key_index;
mod key_loader;
mod log;
#[cfg(feature = "gfp")]
mod project_poller;
mod provider_health;
#[cfg(feature = "gfp")]
mod quota_poller;
// ram-guardian: OOM-prevention subsystem — memory sampling + conservative
// stray rustc/vitest reaper. Runs unconditionally alongside other daemon
// tasks (local-only, no external network surface, no feature gate needed).
mod ram_guardian;
// disk-guardian: boot-volume-exhaustion prevention subsystem — free-space
// sampling + conservative stray build-artifact reaper (isolated cargo
// target dirs, finished agent worktrees). Sibling to ram_guardian; same
// unconditional wiring rationale.
mod disk_guardian;
#[cfg(feature = "gemini-proxy")]
mod proxy;
mod regen;
mod shutdown;
mod state;
mod supervisor;
mod telemetry;
mod tray;
// T-P4-E04-10: cascade instruction-file loader (goes through ChunkCache)
mod loader;
// A1 (vNEXT Phase A): models/models.yaml drift check — embeds the canonical
// provider-family list at compile time and warns (non-fatal) if the live
// providers.json provider set disagrees with it.
mod model_drift;
// T-P4-E01-29: IPC search endpoint (rag.* JSON-RPC methods)
mod search_handler;
// T-P4-E01-32: external drive mount/unmount watch — pause/resume RAG indexing
mod volume_watcher;
// T-P4-E01-26: auto-RAG file watcher (notify-based)
mod rag_watcher;
// T-P4-E04-03: parallel indexing pipeline (WorkerPool → IndexManager)
mod indexer;
// T-P4-E01-13: project discovery (scanner + registry)
mod discovery;
// rag-11: MCP self-registration trigger point (frame-01 stub)
mod mcp_registration;
// Shared test-only helpers (ENV_TEST_LOCK) — mirrors the lib.rs declaration
// so modules compiled into BOTH targets resolve crate::test_support in the
// bin's test target too.
#[cfg(test)]
pub(crate) mod test_support;
// auto-01: background automation scheduler
mod scheduler;
// auto-02: real ProviderRouter + SafeToolInvoker for AutomationRunner
mod automation_router;
// nsentry-sync: daemon-owned multi-stream nSentry sync subsystem
mod nsentry_sync;
// continuity: reset-time triggers to auto-resume Claude Code sessions once a
// usage-cap window clears (E2-S1)
mod continuity;
mod failover;
// E2-S3: POST-processing middleware — background response digest →
// ~/.cascade/context-sync JSONL (the rag_watcher index-refresh nudge)
mod context_sync;
// T-P4-E04-12/13/14/16: delta bundle format, snapshot layout, Ed25519
// verification, full-bundle fallback install, and the update_check/apply/auto
// IPC handlers (wired in ipc.rs try_typed_dispatch).
mod updates;

use cascade_core::providers_store::read_providers_store;
use cascade_keychain::Keychain;
use cascade_providers::{ProviderAdapter, ProviderRegistry};
use clap::Parser;
#[cfg(feature = "gemini-proxy")]
use secrecy::SecretString;
#[cfg(feature = "gemini-proxy")]
use std::collections::HashMap;
use std::process;
use std::sync::mpsc;
use std::sync::Arc;
use tokio::sync::RwLock;
use tokio_util::sync::CancellationToken;
use tracing::{error, info, warn};

/// Cascade background daemon.
///
/// `color = Never` and an explicit `term_width` disable clap's terminal-
/// capability probing entirely — a background daemon has no controlling
/// terminal to format for, and that probe (via the `is-terminal`/
/// `terminal_size` machinery on the error-formatting path only, not the
/// `--version` fast path) can hang indefinitely on certain nested/piped fd
/// inheritance chains (observed when spawned as a subprocess-of-a-
/// subprocess under the Rust test harness).
#[derive(Debug, Parser)]
#[command(
    name = "cascaded",
    about = "Run the Cascade background daemon",
    version,
    color = clap::ColorChoice::Never,
    term_width = 0
)]
struct Cli {}

fn main() {
    let _cli = Cli::parse();

    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .unwrap_or_else(|e| {
            eprintln!("failed to initialize tokio runtime: {e}");
            process::exit(1);
        });

    runtime.block_on(run_daemon());
}

async fn run_daemon() {
    // Resolve config dir (~/.cascade) synchronously so we can read config.toml
    // before initializing the logging subscriber (tracing_subscriber can only be
    // initialized once; we want to honour daemon.log_level / daemon.log_format).
    // Prefer $HOME (honoured on every platform and required for test isolation;
    // note macOS `dirs::home_dir()` ignores $HOME), then fall back to dirs.
    let home = std::env::var_os("HOME")
        .map(std::path::PathBuf::from)
        .or_else(dirs::home_dir);
    let config_dir = match home {
        Some(h) => h.join(".cascade"),
        None => {
            eprintln!("cascaded: cannot determine home directory");
            process::exit(1);
        }
    };

    // Load config now (sync, falls back to Default when config.toml is absent)
    // so log::init_with_config can honour daemon.log_level + daemon.log_format.
    let early_cfg = config::Config::load(&config_dir).unwrap_or_default();

    // Initialize structured JSON logging — honours [daemon] log_level / log_format.
    // CASCADE_LOG_LEVEL env still wins over the config value (see log.rs).
    if let Err(e) = log::init_with_config(
        Some(&early_cfg.daemon.log_level),
        Some(&early_cfg.daemon.log_format),
    ) {
        eprintln!("failed to initialize logging: {e}");
        process::exit(1);
    }

    info!(version = env!("CARGO_PKG_VERSION"), "cascaded starting");

    // Created by `cascade init` but we create it here as a safety net so the
    // daemon never panics on a fresh install where `cascade init` was skipped.
    if let Err(e) = tokio::fs::create_dir_all(&config_dir).await {
        error!(%e, "failed to create ~/.cascade");
        write_crash_sentinel(&e.to_string());
        process::exit(1);
    }

    // Gate OTLP telemetry export on the config flag. init_tracing(None) is a
    // no-op; only call with an endpoint when both enabled=true and an endpoint
    // is configured. The provider must live until the end of main to flush
    // any pending spans on clean shutdown.
    let _otel_provider = {
        let cfg = config::Config::load(&config_dir).unwrap_or_default();
        if cfg.telemetry.enabled {
            telemetry::init_tracing(cfg.telemetry.endpoint.as_deref())
        } else {
            None
        }
    };

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

    let providers_path = config_dir.join("providers.json");
    // Build a slot_id → SecretString map for the Gemini proxy.  Enabled Gemini
    // providers are enumerated from providers.json and paired positionally with
    // the loaded keys (gemini_key_1 → index 0, gemini_key_2 → index 1, …).
    // GeminiProxy::new() accepts this map so the proxy prefers the in-memory
    // SecretString over any plaintext value in providers.json.
    // T-P3-E00-02: SecretString wiring complete.
    // Gated on gemini-proxy: load_api_keys() and the keychain map are only
    // needed when the reverse-proxy surface is compiled in.
    #[cfg(feature = "gemini-proxy")]
    let gemini_keychain_map: HashMap<String, SecretString> = {
        let raw_keys: Vec<SecretString> = key_loader::load_api_keys_from_path(&providers_path);
        let mut gemini_slots: Vec<String> = read_providers_store(&providers_path)
            .map(|store| {
                store
                    .providers
                    .into_iter()
                    .filter(|p| {
                        p.enabled && p.harness == "gemini" && p.id.starts_with("gemini-free-")
                    })
                    .map(|p| p.id)
                    .collect()
            })
            .unwrap_or_default();
        gemini_slots.sort_by_key(|id| gemini_free_slot_number(id));
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
    // Build the runtime ProviderRegistry from configured providers. Real
    // API-key providers are reconstructed with the same adapter constructor path
    // used by the IPC add-provider flow; unsupported entries remain visible as
    // NoopProvider placeholders but are excluded from the health sweep registry.
    // Unhealthy real providers emit WARN logs but NEVER block daemon startup.
    let provider_registry = Arc::new(ProviderRegistry::new());
    let provider_health_registry = Arc::new(ProviderRegistry::new());
    let provider_health_state: provider_health::HealthState =
        Arc::new(RwLock::new(std::collections::HashMap::new()));
    let health_shutdown = CancellationToken::new();

    {
        let entries = read_providers_store(&providers_path)
            .map(|s| s.providers)
            .unwrap_or_default();
        let keychain = cascade_keychain::platform_keychain();

        // E1-S6: count routable GP slots BEFORE `entries` is consumed below —
        // the pool-backed chat adapter is only registered when at least one
        // exists (same filter as `RoutingTable::new`).
        #[cfg(feature = "gemini-proxy")]
        let routable_gp_slots = entries
            .iter()
            .filter(|p| p.enabled && p.harness == "gemini" && p.local_ok)
            .count();

        // A1 (vNEXT Phase A): snapshot the live harness set BEFORE `entries`
        // is consumed below, then compare it against the embedded
        // models/models.yaml canonical provider-family set. Cheap (in-memory
        // string parse, no I/O), non-fatal, non-blocking — logs at most one
        // WARN line and never affects startup on drift or parse failure.
        let live_harnesses: Vec<&str> = entries
            .iter()
            .filter(|p| p.enabled)
            .map(|p| p.harness.as_str())
            .collect();
        model_drift::check_and_warn(live_harnesses);

        for entry in entries.iter().filter(|p| p.enabled) {
            register_provider_store_entry(
                &provider_registry,
                &provider_health_registry,
                keychain.as_ref(),
                entry,
            );
        }
        register_connected_provider_entries(
            &provider_registry,
            &provider_health_registry,
            keychain.as_ref(),
            &config_dir,
        );

        // ── GP-first chat adapter (E1-S6) ─────────────────────────────────
        // Register a REAL pool-proxy GeminiAdapter (:3761 rotating pool, 429
        // cooldown handled by the proxy) under the reserved id the chat
        // GP-preference targets (`selection::GP_CHAT_PROVIDER_ID`). Without
        // this the preference would be a silent no-op: providers.json entries
        // above are NoopProvider placeholders under their own ids
        // ("gemini-free-N"), and no adapter ever answers to the reserved id.
        // Gated on the gemini-proxy feature — the :3761 pool only runs then.
        #[cfg(feature = "gemini-proxy")]
        if routable_gp_slots > 0 {
            use cascade_providers::adapters::gemini::{GeminiAdapter, GeminiConfig};
            let gp_id = cascade_core::selection::GP_CHAT_PROVIDER_ID.to_string();
            let adapter: Arc<dyn ProviderAdapter + Send + Sync> =
                Arc::new(GeminiAdapter::new(GeminiConfig::proxy()));
            if register_startup_adapter(
                &provider_registry,
                &provider_health_registry,
                gp_id.clone(),
                adapter,
            ) {
                info!(
                    provider_id = %gp_id,
                    slots = routable_gp_slots,
                    "registered GP pool-backed chat adapter"
                );
            }
        }
    }

    // ── Local LLM scan (E-P5-07) ──────────────────────────────────────────────
    // Scan ~/.cascade/models/ and register each present model as a local:<id>
    // provider. Never blocks startup — missing models are skipped silently.
    try_register_local_models(&provider_registry, &provider_health_registry);

    // ── Ollama auto-detect (T-P3-E04-19) ─────────────────────────────────────
    // Probe localhost:11434 with a 500 ms timeout. If Ollama is running,
    // register the adapter as "ollama". If absent, skip silently — the
    // daemon must never block startup waiting for an optional local LLM.
    try_register_ollama(&provider_registry, &provider_health_registry).await;

    let _provider_health_handle = provider_health::spawn_health_check_task(
        provider_health_registry.clone(),
        provider_health_state.clone(),
        std::time::Duration::from_secs(300), // 5-minute refresh interval
        health_shutdown.clone(),
    );
    info!("provider health-check task spawned (non-blocking)");

    // ── RAM Guardian task ─────────────────────────────────────────────────
    // OOM-prevention: samples free memory every 30s and, only when memory is
    // tight, reaps orphaned rustc/vitest strays (reparented + old). See
    // ram_guardian.rs module docs for the full safety contract. Uses its own
    // CancellationToken (mirrors health_shutdown) so it can be cancelled
    // independently of the top-level shutdown_token created below.
    let ram_guardian_shutdown = CancellationToken::new();
    let _ram_guardian_handle =
        ram_guardian::spawn(config_dir.clone(), ram_guardian_shutdown.clone());
    info!("ram_guardian task spawned (non-blocking)");

    // ── Disk Guardian task ────────────────────────────────────────────────
    // Boot-volume-exhaustion prevention: samples free disk space on the
    // scratch root (system temp dir) every 30s and, only when space is
    // tight, reaps stray isolated cargo target dirs / finished agent
    // worktrees (old + pattern-matched). See disk_guardian.rs module docs
    // for the full safety contract. Uses its own CancellationToken (mirrors
    // ram_guardian_shutdown) so it can be cancelled independently of the
    // top-level shutdown_token created below.
    let disk_guardian_shutdown = CancellationToken::new();
    let _disk_guardian_handle =
        disk_guardian::spawn(config_dir.clone(), disk_guardian_shutdown.clone());
    info!("disk_guardian task spawned (non-blocking)");

    // ── Continuity watcher task ─────────────────────────────────────────────
    // Watches ~/.cascade/continuity/*.json and fires due intents once the
    // named account's quota window clears. Uses its own CancellationToken
    // (mirrors ram_guardian_shutdown) so it can be cancelled independently of
    // the top-level shutdown_token created below.
    let continuity_shutdown = CancellationToken::new();
    let _continuity_handle = continuity::spawn(continuity_shutdown.clone());
    info!("continuity task spawned (non-blocking)");

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
                    // Open the Cascade application: try ~/Applications/Cascade.app
                    // first (the installed product location), then fall back to
                    // opening ~/.cascade/accounts/ in Finder so the user sees their
                    // account data even when the app is not yet installed.
                    #[cfg(target_os = "macos")]
                    {
                        let home = dirs::home_dir().unwrap_or_default();
                        let app_path = home.join("Applications").join("Cascade.app");
                        if app_path.exists() {
                            if let Err(e) = std::process::Command::new("open")
                                .arg("-a")
                                .arg(&app_path)
                                .spawn()
                            {
                                warn!(%e, app = %app_path.display(), "tray: OpenApp: failed to open Cascade.app");
                            } else {
                                info!(app = %app_path.display(), "tray: OpenApp: opened Cascade.app");
                            }
                        } else {
                            // App not installed — open accounts dir as fallback.
                            let fallback = home.join(".cascade").join("accounts");
                            if let Err(e) =
                                std::process::Command::new("open").arg(&fallback).spawn()
                            {
                                warn!(%e, path = %fallback.display(), "tray: OpenApp: fallback open failed");
                            } else {
                                info!(path = %fallback.display(), "tray: OpenApp: opened accounts dir (app not installed)");
                            }
                        }
                    }
                    #[cfg(not(target_os = "macos"))]
                    {
                        warn!("tray: OpenApp: not implemented on this platform");
                    }
                }
                cascade_tray::TrayAction::OpenDashboard => {
                    // Open the local dashboard in the default browser.
                    info!("tray action: OpenDashboard");
                    if let Err(e) = open_url("http://localhost:9761") {
                        warn!(%e, "failed to open dashboard URL");
                    }
                }
                cascade_tray::TrayAction::PauseDaemon => {
                    // Pause is not yet implemented — the daemon has no pause
                    // primitive and the IPC server has no Pause method.
                    // Log honestly rather than claiming success.
                    warn!("tray action: PauseDaemon — pause not implemented; request ignored");
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

    // ── Dashboard / chat HTTP server (127.0.0.1:9761) ────────────────────────
    // Serves the app's `/api/chat` (+ management API). This was NEVER spawned —
    // only the :3761/:3762 proxies were — so Cascade.app chat showed "Load
    // failed" / "Disconnected". Referenced via the lib crate path
    // (`cascade_daemon::dashboard`) because the binary does not re-compile the
    // dashboard module tree; the fleet provider registry is injected so
    // `/api/chat` can route personal/chat requests to a provider.
    {
        use std::net::SocketAddr;
        let dash_bind: SocketAddr = "127.0.0.1:9761".parse().expect("valid loopback addr");
        match cascade_daemon::dashboard::Dashboard::new(
            &config_dir,
            dash_bind,
            shutdown_token.clone(),
        ) {
            Ok(dashboard) => {
                let dashboard = dashboard.with_provider_registry(provider_registry.clone());
                tokio::spawn(async move {
                    if let Err(e) = dashboard.run().await {
                        error!(%e, "dashboard/chat server (:9761) exited with error");
                    }
                });
                info!("dashboard/chat server spawned on 127.0.0.1:9761");
            }
            Err(e) => error!(%e, "failed to construct dashboard/chat server on :9761"),
        }
    }

    #[cfg(feature = "gemini-proxy")]
    let supervisor_future = supervisor::run(
        config_dir.clone(),
        shutdown_token.clone(),
        provider_registry.clone(),
        gemini_keychain_map,
    );
    #[cfg(not(feature = "gemini-proxy"))]
    let supervisor_future = supervisor::run(
        config_dir.clone(),
        shutdown_token.clone(),
        provider_registry.clone(),
    );

    tokio::select! {
        result = supervisor_future => {
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

    // ── RAM Guardian task shutdown ─────────────────────────────────────────
    ram_guardian_shutdown.cancel();

    // ── Disk Guardian task shutdown ─────────────────────────────────────────
    disk_guardian_shutdown.cancel();

    // ── Continuity task shutdown ────────────────────────────────────────────
    continuity_shutdown.cancel();

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

const IPC_PROVIDER_KEYCHAIN_SERVICE: &str = "dev.cascade";

#[cfg(feature = "gemini-proxy")]
fn gemini_free_slot_number(id: &str) -> u32 {
    id.strip_prefix("gemini-free-")
        .and_then(|n| n.parse::<u32>().ok())
        .unwrap_or(u32::MAX)
}

fn register_provider_store_entry(
    registry: &Arc<ProviderRegistry>,
    health_registry: &Arc<ProviderRegistry>,
    keychain: &dyn Keychain,
    entry: &cascade_core::providers_store::ProviderEntry,
) {
    let id = entry.id.clone();

    if !entry.local_ok {
        register_noop_placeholder(registry, id, "provider is disabled for local daemon use");
        return;
    }

    if entry.auth_kind != "ApiKey" {
        register_noop_placeholder(registry, id, "provider auth kind has no startup adapter");
        return;
    }

    let Some(api_key) = provider_key_for_entry(keychain, entry) else {
        register_noop_placeholder(registry, id, "provider API key was not found");
        return;
    };

    let adapter = cascade_daemon::ipc_providers::build_adapter_for_id(&id, &api_key);
    register_startup_adapter(registry, health_registry, id, adapter);
}

fn provider_key_for_entry(
    keychain: &dyn Keychain,
    entry: &cascade_core::providers_store::ProviderEntry,
) -> Option<String> {
    if entry.harness == "gemini" && entry.account_id.starts_with("AIza") {
        return Some(entry.account_id.clone());
    }

    let candidates = [
        (IPC_PROVIDER_KEYCHAIN_SERVICE, entry.id.as_str()),
        (
            cascade_providers::PROVIDER_KEYCHAIN_SERVICE,
            entry.id.as_str(),
        ),
        (IPC_PROVIDER_KEYCHAIN_SERVICE, entry.account_id.as_str()),
        (
            cascade_providers::PROVIDER_KEYCHAIN_SERVICE,
            entry.account_id.as_str(),
        ),
    ];

    candidates
        .iter()
        .find_map(|(service, account)| keychain.get_key(service, account).ok())
}

fn register_connected_provider_entries(
    registry: &Arc<ProviderRegistry>,
    health_registry: &Arc<ProviderRegistry>,
    keychain: &dyn Keychain,
    config_dir: &std::path::Path,
) {
    let cascade_dir = config_dir.to_path_buf();
    for provider in cascade_providers::list_providers_at(keychain, Some(&cascade_dir)) {
        match keychain.get_key(cascade_providers::PROVIDER_KEYCHAIN_SERVICE, &provider.id) {
            Ok(api_key) => {
                let adapter =
                    cascade_daemon::ipc_providers::build_adapter_for_id(&provider.id, &api_key);
                register_startup_adapter(registry, health_registry, provider.id, adapter);
            }
            Err(_) => {
                register_noop_placeholder(
                    registry,
                    provider.id,
                    "connected provider keychain entry was not found",
                );
            }
        }
    }
}

fn register_startup_adapter(
    registry: &Arc<ProviderRegistry>,
    health_registry: &Arc<ProviderRegistry>,
    id: String,
    adapter: Arc<dyn ProviderAdapter + Send + Sync>,
) -> bool {
    let is_noop = adapter.provider_info().id == "noop";
    if is_noop {
        warn!(
            provider_id = %id,
            "provider registered as NoopProvider and excluded from health sweep"
        );
    }

    if let Err(e) = registry.register(id.clone(), Arc::clone(&adapter)) {
        warn!(provider_id = %id, error = %e, "failed to register provider (skipping)");
        return false;
    }

    if !is_noop {
        if let Err(e) = health_registry.register(id.clone(), adapter) {
            warn!(
                provider_id = %id,
                error = %e,
                "failed to register provider in health registry (skipping health check)"
            );
        }
    }

    true
}

fn register_noop_placeholder(registry: &Arc<ProviderRegistry>, id: String, reason: &'static str) {
    warn!(
        provider_id = %id,
        reason,
        "provider registered as NoopProvider and excluded from health sweep"
    );
    let adapter: Arc<dyn ProviderAdapter + Send + Sync> = Arc::new(cascade_providers::NoopProvider);
    if let Err(e) = registry.register(id.clone(), adapter) {
        warn!(provider_id = %id, error = %e, "failed to register provider (skipping)");
    }
}

/// Probe the local Ollama daemon and register `OllamaAdapter` if present.
///
/// Fires a single `GET /api/tags` with a 500 ms timeout.  When Ollama
/// responds, the adapter is registered as `"ollama"` in the registry.
/// When absent (connection refused / timeout) this is a silent no-op —
/// an optional local LLM must never block daemon startup.
///
/// SPORT: MASTER-RUST-CRATES.md — cascade-daemon: Ollama auto-detect (T-P3-E04-19)
async fn try_register_ollama(
    registry: &Arc<ProviderRegistry>,
    health_registry: &Arc<ProviderRegistry>,
) {
    use cascade_providers::adapters::ollama::OllamaAdapter;

    match OllamaAdapter::try_detect().await {
        Some(adapter) => {
            let adapter: Arc<dyn ProviderAdapter + Send + Sync> = Arc::new(adapter);
            if register_startup_adapter(registry, health_registry, "ollama".to_owned(), adapter) {
                info!("Ollama detected and registered as provider \"ollama\"");
            }
        }
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
/// When the `local-llm` feature is disabled this is a no-op — candle/gemm are
/// not compiled and no local model paths are scanned.
///
/// SPORT: MASTER-RUST-CRATES.md — cascade-daemon: local LLM registration (E-P5-07)
#[cfg(feature = "local-llm")]
fn try_register_local_models(
    registry: &Arc<ProviderRegistry>,
    health_registry: &Arc<ProviderRegistry>,
) {
    use cascade_local_llm::scan_installed_models;

    let adapters = scan_installed_models();
    if adapters.is_empty() {
        info!("no local models found under ~/.cascade/models/ (skipping)");
        return;
    }
    for (provider_id, adapter) in adapters {
        let adapter: Arc<dyn ProviderAdapter + Send + Sync> = Arc::new(adapter);
        if register_startup_adapter(registry, health_registry, provider_id.clone(), adapter) {
            info!(provider_id, "local LLM registered");
        }
    }
}

/// No-op stub used when the `local-llm` feature is not enabled.
#[cfg(not(feature = "local-llm"))]
fn try_register_local_models(
    _registry: &Arc<ProviderRegistry>,
    _health_registry: &Arc<ProviderRegistry>,
) {
    info!("local LLM support not built in (build with --features local-llm to enable)");
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
