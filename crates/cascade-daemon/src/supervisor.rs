//! Per-OS supervision setup for the cascaded daemon.
//!
//! Purpose: Install and manage the OS-level service watchdog that keeps the
//! daemon alive across crashes and reboots. Three backends:
//!   - macOS  : LaunchAgent (`~/Library/LaunchAgents/dev.cascade.daemon.plist`)
//!   - Linux  : systemd user unit (`~/.config/systemd/user/cascade.service`)
//!   - Windows: Windows Service via `windows-service-rs`
//!
//! The `run()` function is called from main.rs and drives the inner event
//! loop (IPC + health + event bus) until the shutdown token fires.
//!
//! Constraints:
//!   - All install paths are user-session level — no admin elevation.
//!   - Install is idempotent: state-check first, skip if already installed.
//!     This prevents the OS admin prompt hygiene violation described in GCI.

#[cfg(feature = "gemini-proxy")]
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

#[cfg(feature = "gemini-proxy")]
use secrecy::SecretString;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use cascade_core::HookStore;
use cascade_types::hook::HookEvent;

use crate::config::Config;
use crate::{
    event_bus::EventBus, healthcheck::HealthState, hook_runner::HookRunner, ipc::IpcServer,
};

/// Drives the daemon's main loop. Starts the IPC server, health poller,
/// event bus, hook engine, quota poller, file watcher, and providers scan,
/// then blocks until `shutdown` fires.
///
/// WHY all startup wiring lives here: supervisor::run() is the single entry
/// point called from main.rs. Centralising startup here keeps main.rs thin and
/// allows integration tests that spawn the real binary to observe all startup
/// artifacts (providers.json, quota-state.json, events.db cascade.changed events,
/// DaemonStart hook log lines) without needing test-only binary flags.
pub async fn run(
    config_dir: PathBuf,
    shutdown: CancellationToken,
    provider_registry: Arc<cascade_providers::ProviderRegistry>,
    #[cfg(feature = "gemini-proxy")] gemini_keychain_map: HashMap<String, SecretString>,
) -> Result<(), DaemonError> {
    let start_time = std::time::Instant::now();
    info!(config_dir = %config_dir.display(), "supervisor starting");

    // ── Load config ───────────────────────────────────────────────────────────
    // Config::load returns Config::default() when config.toml is absent —
    // safe to call unconditionally.
    let config = Config::load(&config_dir).unwrap_or_default();

    let health = HealthState::new(start_time);
    let bus = EventBus::new(config_dir.clone()).await?;

    // Honour daemon.socket_path from config (empty = use default daemon.sock).
    let socket_override: Option<PathBuf> = if config.daemon.socket_path.is_empty() {
        None
    } else {
        Some(PathBuf::from(config.daemon.socket_path.as_str()))
    };
    let mut ipc = IpcServer::new_with_socket(
        config_dir.clone(),
        health.clone(),
        bus.clone(),
        Arc::clone(&provider_registry),
        socket_override,
    )
    .await?;
    // D10: wire the daemon-level shutdown token so DaemonStop IPC requests actually
    // cancel the running process rather than just acknowledging the request.
    ipc.set_daemon_shutdown(shutdown.clone());

    // ── HookStore + HookRunner: seed hooks from config.toml [[hooks]] ────────
    // Create an in-memory SQLite HookStore, seed it from the config's [[hooks]]
    // array, then fire the DaemonStart event so configured hooks run at startup.
    //
    // WHY in-memory: hook definitions are loaded from config.toml each restart;
    // there is no need for persistence between restarts in P2.
    let hook_store = match HookStore::in_memory() {
        Ok(store) => {
            if !config.hooks.is_empty() {
                // load_from_config upserts all [[hooks]] entries with tier = "config".
                match store.load_from_config(config.hooks.clone(), "config") {
                    Ok(names) => {
                        info!(count = names.len(), "hooks loaded from config.toml");
                    }
                    Err(e) => {
                        warn!(%e, "failed to load some hooks from config.toml");
                    }
                }
            }
            Arc::new(store)
        }
        Err(e) => {
            warn!(%e, "failed to create HookStore — hooks disabled");
            // Create an empty store so HookRunner can still be constructed.
            Arc::new(HookStore::in_memory().unwrap_or_else(|_| {
                // Absolute fallback: create an empty store.
                // SAFETY: in_memory() only fails if SQLite is unavailable
                // (extremely unlikely). The second call is the same code path;
                // if it also fails we propagate the error.
                panic!("HookStore::in_memory failed twice — SQLite unavailable")
            }))
        }
    };
    let hook_runner = HookRunner::new(hook_store, bus.clone());
    // Fire DaemonStart hooks — any LogMessage hooks in config.toml [[hooks]] with
    // event = "DaemonStart" will emit their message to the log here.
    hook_runner.fire(&HookEvent::DaemonStart).await;

    // ── Providers.json: write at startup ─────────────────────────────────────
    // detect_harness_accounts + write_providers_store writes providers.json to
    // config_dir. Even with no harnesses installed the file is written (empty
    // providers array) so the test assertion that the file exists passes.
    //
    // WHY run synchronously before IPC: the widget, CLI, and onboarding wizard
    // expect providers.json to be present as soon as the daemon is running.
    {
        use cascade_core::auth_detector::detect_harness_accounts;
        use cascade_core::providers_store::{
            merge_providers, read_providers_store, write_providers_store, ProvidersStore,
            PROVIDERS_STORE_SCHEMA_VERSION,
        };
        use chrono::Utc;

        // Prefer $HOME (respected in test isolation) over dirs::home_dir().
        let home_dir = std::env::var_os("HOME")
            .map(PathBuf::from)
            .or_else(dirs::home_dir)
            .unwrap_or_else(|| config_dir.parent().unwrap_or(&config_dir).to_path_buf());

        let providers_path = config_dir.join("providers.json");
        let detected = detect_harness_accounts(&home_dir);
        info!(
            count = detected.len(),
            "harness accounts detected at startup"
        );

        let mut store = read_providers_store(&providers_path).unwrap_or_else(|_| ProvidersStore {
            schema_version: PROVIDERS_STORE_SCHEMA_VERSION,
            updated_at: Utc::now().to_rfc3339(),
            providers: Vec::new(),
        });
        merge_providers(&mut store.providers, &detected);
        store.updated_at = Utc::now().to_rfc3339();

        if let Err(e) = write_providers_store(&providers_path, &store) {
            warn!(%e, "failed to write providers.json at startup");
        } else {
            info!(path = %providers_path.display(), "providers.json written at startup");
        }
    }

    // ── Quota poller ─────────────────────────────────────────────────────────
    // Spawn the quota poller as a fire-and-forget Tokio task. It polls
    // localhost:3761/health every interval_secs and writes quota-state.json.
    // On proxy unavailable it writes an error-state JSON — the file always exists.
    // Gated on gfp: quota polling is part of the GFP provisioning surface.
    #[cfg(feature = "gfp")]
    if config.quota_poller.enabled {
        use crate::quota_poller::QuotaPoller;
        use crate::state::DaemonState;
        use std::sync::Mutex;

        let daemon_state = Arc::new(Mutex::new(DaemonState::new()));
        let qp_config = config.quota_poller.clone();
        let qs_config = config.quota_store.clone();
        let qp_dir = config_dir.clone();
        let qp_bus = bus.clone();
        let qp_shutdown = shutdown.clone();

        tokio::spawn(async move {
            if let Err(e) = QuotaPoller::run(
                qp_config,
                qs_config,
                qp_dir,
                qp_bus,
                daemon_state,
                qp_shutdown,
            )
            .await
            {
                warn!(%e, "quota poller exited with error");
            }
        });
    }

    // ── Fleet poller (E-P6-03 v1.1) ──────────────────────────────────────────
    // Aggregates per-source quota snapshots into quota-store.json every
    // interval_secs. Runs unconditionally (not gated on the `gfp` feature)
    // because the fleet poller supports multiple providers beyond GFP.
    if config.fleet.enabled {
        use crate::fleet_poller::FleetPoller;
        let fp_config = config.fleet.clone();
        let fp_dir = config_dir.clone();
        let fp_shutdown = shutdown.clone();
        tokio::spawn(async move {
            FleetPoller::run(fp_config, fp_dir, fp_shutdown).await;
        });
    }

    // ── nSentry multi-stream sync subsystem ──────────────────────────────────
    // Driven by ~/.cascade/nsentry-sync.yaml. No-op if the file is absent.
    // Scripts are installed to ~/.cascade/nsentry/scripts/ at startup.
    {
        use crate::nsentry_sync;
        let ns_yaml = config_dir.join("nsentry-sync.yaml");
        let ns_status = config_dir.join("nsentry-sync-status.json");
        let ns_scripts = config_dir.join("nsentry").join("scripts");
        let ns_shutdown = shutdown.clone();
        nsentry_sync::spawn(ns_yaml, ns_status, ns_scripts, ns_shutdown);
        info!("nsentry: sync subsystem spawned (no-op if nsentry-sync.yaml absent)");
    }

    // ── File watcher: publish cascade.changed events ──────────────────────────
    // Poll config_dir/CASCADE.md for modification time changes every debounce_ms.
    // When a change is detected, publish a "cascade.changed" event to the bus.
    // WHY polling: the notify crate is not in the workspace dependencies for P2;
    // tokio fs::metadata polling is sufficient for the integration test which
    // sets debounce_ms=50 ms in test-config.toml.
    {
        let watch_path = config_dir.join("CASCADE.md");
        let watch_bus = bus.clone();
        let debounce = Duration::from_millis(config.watcher.debounce_ms);
        let watch_shutdown = shutdown.clone();

        tokio::spawn(async move {
            let mut last_mtime: Option<std::time::SystemTime> = None;

            // Read initial mtime if file exists.
            if let Ok(meta) = tokio::fs::metadata(&watch_path).await {
                last_mtime = meta.modified().ok();
            }

            loop {
                tokio::select! {
                    _ = tokio::time::sleep(debounce) => {}
                    _ = watch_shutdown.cancelled() => { break; }
                }

                match tokio::fs::metadata(&watch_path).await {
                    Ok(meta) => {
                        let mtime = meta.modified().ok();
                        if mtime != last_mtime && last_mtime.is_some() {
                            // File changed — publish the event.
                            if let Err(e) = watch_bus
                                .publish(
                                    "cascade.changed",
                                    serde_json::json!({ "path": watch_path.to_string_lossy() }),
                                )
                                .await
                            {
                                warn!(%e, "failed to publish cascade.changed event");
                            } else {
                                info!(path = %watch_path.display(), "cascade.changed event published");
                                // D3: trigger compat-file regeneration on any .cascade/
                                // directory change. Spawned as a non-blocking task so the
                                // watcher loop is never stalled by the compat_gen I/O.
                                let regen_path = watch_path.clone();
                                tokio::spawn(async move {
                                    if let Err(e) =
                                        crate::regen::handle_cascade_change(&regen_path).await
                                    {
                                        warn!(error = %e, "regen: compat-file regeneration failed");
                                    }
                                });
                            }
                        }
                        last_mtime = mtime;
                    }
                    Err(_) => {
                        // File doesn't exist yet — reset mtime tracking.
                        last_mtime = None;
                    }
                }
            }
        });
    }

    // ── Gemini GP proxy + Anthropic-compat adapter ───────────────────────────
    // Gated on the `gemini-proxy` feature (external-network surface, opt-in).
    // GeminiProxy listens on 127.0.0.1:3761; AnthropicCompatServer listens on
    // 127.0.0.1:3762 and forwards translated requests to the GP proxy.
    #[cfg(feature = "gemini-proxy")]
    {
        use crate::proxy::{AnthropicCompatServer, GeminiProxy};
        use std::net::SocketAddr;

        let providers_path = config_dir.join("providers.json");

        // GeminiProxy (port 3761).
        let gp_bind: SocketAddr = "127.0.0.1:3761".parse().expect("valid addr");
        let gp_shutdown = shutdown.clone();
        let gp_bus = bus.clone();
        let gp_proxy = GeminiProxy::new(
            config.proxy.clone(),
            providers_path,
            gp_bus,
            gp_bind,
            gp_shutdown,
            gemini_keychain_map,
        );
        tokio::spawn(async move {
            if let Err(e) = gp_proxy.run().await {
                warn!(error = %e, "gemini_proxy exited with error");
            }
        });

        // AnthropicCompatServer (port 3762) — forwards to GP proxy on 3761.
        let ac_bind: SocketAddr = "127.0.0.1:3762".parse().expect("valid addr");
        let ac_shutdown = shutdown.clone();
        let ac_server =
            AnthropicCompatServer::new(ac_bind, "http://127.0.0.1:3761".to_string(), ac_shutdown);
        tokio::spawn(async move {
            if let Err(e) = ac_server.run().await {
                warn!(error = %e, "anthropic_compat exited with error");
            }
        });
    }

    // ── Scheduler ────────────────────────────────────────────────────────────────
    if config.scheduler.enabled {
        use crate::scheduler::Scheduler;
        use cascade_core::task_store::TaskStore;
        use std::sync::Mutex;

        // Persistent store: `<config_dir>/scheduler.db` (mirrors the
        // `<config_dir>/registry.db` / `<config_dir>/quota-state.json`
        // convention used elsewhere in this file — one named SQLite/JSON
        // file per subsystem, directly under the daemon's config_dir).
        // WHY not ":memory:": an in-memory DB loses every scheduled task on
        // daemon restart, silently dropping user-created schedules.
        // `cascade_db::open_configured` applies the canonical WAL/busy-timeout
        // PRAGMA set and creates `config_dir` if missing.
        let sched_db_path = config_dir.join("scheduler.db");
        let sched_conn = cascade_db::open_configured(&sched_db_path)
            .expect("scheduler: failed to open persistent scheduler.db");
        sched_conn
            .execute_batch(
                "CREATE TABLE IF NOT EXISTS scheduled_tasks (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL,
                    schedule_json TEXT NOT NULL,
                    command TEXT NOT NULL,
                    args_json TEXT NOT NULL,
                    enabled INTEGER NOT NULL DEFAULT 1,
                    created_at TEXT NOT NULL,
                    last_run TEXT,
                    next_run TEXT,
                    status_json TEXT NOT NULL
                );",
            )
            .expect("scheduler: failed to create scheduled_tasks table");

        let sched_conn_arc = Arc::new(Mutex::new(sched_conn));
        let task_store = Arc::new(TaskStore::new(sched_conn_arc));

        // No default/placeholder tasks are seeded here (previously two
        // no-op `"true"` command seeds — removed; no real intended command
        // was specified for an index-rescan or memory-consolidate job, and
        // shipping inert seeds masks the fact that nothing actually runs).
        // Users/operators register real scheduled tasks via the scheduler
        // IPC/API surface; the store starts empty and persists whatever is
        // added across restarts.

        let sched_config = config.scheduler.clone();
        let sched_shutdown = shutdown.clone();
        let scheduler = Scheduler::new(sched_config, task_store, sched_shutdown);
        tokio::spawn(scheduler.run());
        info!(path = %sched_db_path.display(), "scheduler: spawned with persistent store");
    }

    // ── RAG subsystem ─────────────────────────────────────────────────────────────
    // Gated on cascade_types::CascadeConfig::rag.enabled (default: true since rag-11).
    // Spawns: AutoRagWatcher → IndexingPipeline, VolumeWatcher → VolumeIndexGuard.
    // Bootstrap scan: ProjectScanner over effective_projects_dirs() on first boot,
    //   idempotent via ProjectRegistry (SQLite upsert; already-indexed = no-op).
    //
    // WHY all three components are co-located here: they share the IndexManager
    // and must be wired in dependency order (IndexManager → watcher + guard + pipeline).
    // Separating them across multiple run() calls would complicate teardown ordering.
    {
        use cascade_types::config::CascadeConfig;
        let cascade_cfg = CascadeConfig::default();

        if cascade_cfg.rag.enabled {
            use crate::discovery::{ProjectRegistry, ProjectScanner};
            use crate::indexer::IndexingPipeline;
            use crate::rag_watcher::{AutoRagWatcher, WatcherConfig};
            use crate::volume_watcher::{IndexManager, VolumeIndexGuard, VolumeWatcher};
            use cascade_rag::workers::{WorkerPool, WorkerPoolConfig};

            // ── Bootstrap: open (or create) the global index manager ─────────
            let index_root = config_dir.join("index");
            match IndexManager::open(&index_root).await {
                Err(e) => {
                    warn!(%e, "rag: failed to open IndexManager — RAG subsystem disabled for this session");
                }
                Ok(index_mgr) => {
                    // ── Bootstrap scan: discover projects on startup ──────────
                    // ProjectRegistry is SQLite-backed; upsert is idempotent.
                    // Files already in the index will not be re-ingested.
                    let project_roots = cascade_cfg.effective_projects_dirs();
                    {
                        let registry_path = config_dir.clone();
                        let roots_clone = project_roots.clone();
                        tokio::task::spawn_blocking(move || {
                            match ProjectRegistry::open(&registry_path) {
                                Err(e) => {
                                    warn!(%e, "rag: bootstrap scan — failed to open ProjectRegistry");
                                }
                                Ok(mut registry) => {
                                    let scanner = ProjectScanner::new(roots_clone);
                                    let records = scanner.scan(&mut registry);
                                    info!(
                                        count = records.len(),
                                        "rag: bootstrap scan complete — projects discovered"
                                    );
                                }
                            }
                        });
                    }

                    // ── Embed model: lazy holder — instant mock now, real ONNX
                    //    swapped in by a background task so startup is never
                    //    blocked by the model download/load. ───────────────────
                    //
                    // CASCADE_OFFLINE_MODELS=1 skips the background download task.
                    // This backs the AutoRagWatcher/VolumeIndexGuard pipeline;
                    // ipc.rs's RagSearchHandler uses the SAME shared instance so
                    // the ~4 GB ONNX model is downloaded/warmed only once per
                    // process (previously each site built its own copy — the log
                    // showed two "initialising BGE-M3" per daemon start).
                    let lazy_embed = cascade_rag::embed::LazyEmbedModel::shared();
                    if std::env::var("CASCADE_OFFLINE_MODELS").is_err() {
                        lazy_embed.spawn_load();
                    }
                    let embed: Arc<dyn cascade_rag::embed::EmbedModel> = lazy_embed;

                    // ── WorkerPool ────────────────────────────────────────────
                    let pool =
                        Arc::new(WorkerPool::new(WorkerPoolConfig::default(), embed.clone()));
                    // Register the pool so `rag.queue_depth` IPC reports live
                    // back-pressure to `cascade status` (T-P7-E07-02).
                    ipc.set_worker_pool(Arc::clone(&pool)).await;

                    // ── VolumeWatcher + VolumeIndexGuard ─────────────────────
                    let (vol_watcher, vol_rx) = VolumeWatcher::new(
                        project_roots.clone(),
                        VolumeWatcher::DEFAULT_POLL_INTERVAL,
                    );
                    let vol_guard =
                        VolumeIndexGuard::new(project_roots.clone(), Arc::clone(&index_mgr));
                    let vol_shutdown = shutdown.clone();
                    vol_watcher.spawn(vol_shutdown.clone());
                    vol_guard.spawn(vol_rx, vol_shutdown);
                    info!("rag: VolumeWatcher + VolumeIndexGuard spawned");

                    // ── AutoRagWatcher + IndexingPipeline ─────────────────────
                    // Use config_dir as the project root for the watcher so the
                    // global .cascade/ memory/planning dirs are always watched.
                    //
                    // E2-S3: pre-create ~/.cascade/context-sync so the watcher
                    // (which only registers dirs that exist at start) picks up
                    // post-middleware digest appends — that FS event IS the
                    // index-refresh nudge. Best-effort: an empty dir either way.
                    if let Err(e) = crate::context_sync::ensure_context_sync_dir(&config_dir) {
                        warn!(%e, "rag: could not pre-create context-sync dir (context-sync digests will not be watched)");
                    }
                    let watcher_cfg = WatcherConfig::new(config_dir.clone());
                    let rag_shutdown = shutdown.clone();
                    match AutoRagWatcher::start(watcher_cfg, rag_shutdown.clone()) {
                        Err(e) => {
                            warn!(%e, "rag: AutoRagWatcher failed to start — file watching disabled");
                        }
                        Ok((_watcher, signal_rx)) => {
                            let pipeline = IndexingPipeline::new(
                                signal_rx,
                                pool,
                                Arc::clone(&index_mgr),
                                embed,
                                rag_shutdown,
                            );
                            tokio::spawn(pipeline.run());
                            info!("rag: AutoRagWatcher + IndexingPipeline spawned");
                        }
                    }
                }
            }
        }
    }

    // ── CC lifecycle hooks ────────────────────────────────────────────────────────
    // Install the Stop harvest hook and PostToolUse security scan hook into
    // ~/.claude/settings.json.  Both writes are idempotent and fire-and-forget.
    // The former mcp_registration::register() (MCP server self-registration) has
    // been removed — it required mcp.sock which nothing creates (D5).
    {
        use crate::mcp_registration;
        tokio::spawn(async move {
            mcp_registration::register_cc_stop_hook().await;
            mcp_registration::register_security_hook().await;
        });
        info!("cc_hooks: stop-harvest and security-scan hooks scheduled");
    }

    // ── AutomationRunner ─────────────────────────────────────────────────────────
    // Wired to the real ProviderRegistry via RegistryRouter (auto-02).
    // The runner is kept on the stack and will be used by the scheduler and
    // IPC automation dispatch once those surfaces wire into it (auto-03+).
    //
    // Lazy: no I/O happens here. The first actual automation step triggers
    // RegistryRouter::step(), which calls pick_for_chat() on the live registry.
    // If no provider is registered the step returns ProviderFailed (graceful).
    {
        use crate::automation_router::{RegistryRouter, SafeToolInvoker, SafetyGate};
        use cascade_agents::automation::{AutomationRunner, NoopSink};
        use cascade_agents::chain::ChainExecutor;
        use cascade_agents::executor::AgentExecutor;

        let real_router = Arc::new(RegistryRouter::new(Arc::clone(&provider_registry)));
        // Wrap SafeToolInvoker in SafetyGate so all three safety layers
        // (deny-list, path sandbox, real audit log) are already enforced on
        // this daemon-wide AutomationRunner. SafeToolInvoker is today an
        // inert "not yet implemented" stub (every call returns a fixed
        // error string and performs no I/O) so there is no active exploit
        // path, but wrapping it now means the safety gate is in place
        // BEFORE any real tool implementation is ever wired in here. The
        // sandbox root is "." because no per-session project base dir
        // exists in this scope yet — the live runner (when activated) will
        // receive a real project root per automation task and be
        // re-wrapped at that point.
        let safe_invoker = Arc::new(SafetyGate::new(
            Arc::new(SafeToolInvoker),
            ".",
            "cascade-daemon-automation-runner",
        ));

        let agent_exec = Arc::new(
            AgentExecutor::builder()
                .provider_router(
                    Arc::clone(&real_router) as Arc<dyn cascade_agents::executor::ProviderRouter>
                )
                .tool_invoker(
                    Arc::clone(&safe_invoker) as Arc<dyn cascade_agents::executor::ToolInvoker>
                )
                .build(),
        );
        let chain_exec = Arc::new(
            ChainExecutor::builder()
                .provider_router(
                    Arc::clone(&real_router) as Arc<dyn cascade_agents::executor::ProviderRouter>
                )
                .tool_invoker(
                    Arc::clone(&safe_invoker) as Arc<dyn cascade_agents::executor::ToolInvoker>
                )
                .agent_executor(Arc::clone(&agent_exec))
                .build(),
        );

        let _automation_runner = Arc::new(
            AutomationRunner::builder()
                .chain_executor(chain_exec)
                .agent_executor(agent_exec)
                .sink(Arc::new(NoopSink))
                .build(),
        );
        info!("automation_runner: instantiated with RegistryRouter (real provider path — auto-02)");
    }

    // ── Main event loop ───────────────────────────────────────────────────────
    tokio::select! {
        result = ipc.serve(shutdown.clone()) => {
            result?;
        }
        _ = shutdown.cancelled() => {}
    }

    bus.flush().await?;
    info!("supervisor stopped");
    Ok(())
}

// ── OS-specific install helpers ───────────────────────────────────────────

/// Install the daemon as an OS service. Idempotent.
///
/// Inputs: `daemon_bin` — absolute path to the `cascaded` binary.
/// Outputs: unit of state change (plist loaded / unit enabled / service registered).
/// Constraints: never prompts for admin password.
// CLI surface — called by `cascade install` subcommand.
#[allow(dead_code)]
pub async fn install(daemon_bin: &Path) -> Result<InstallResult, DaemonError> {
    #[cfg(target_os = "macos")]
    return install_macos(daemon_bin).await;
    #[cfg(target_os = "linux")]
    return install_linux(daemon_bin).await;
    #[cfg(target_os = "windows")]
    return install_windows(daemon_bin).await;
    #[allow(unreachable_code)]
    Err(DaemonError::UnsupportedPlatform)
}

/// Uninstall the OS service. Idempotent.
// CLI surface — called by `cascade uninstall` subcommand.
#[allow(dead_code)]
pub async fn uninstall() -> Result<(), DaemonError> {
    #[cfg(target_os = "macos")]
    return uninstall_macos().await;
    #[cfg(target_os = "linux")]
    return uninstall_linux().await;
    #[cfg(target_os = "windows")]
    return uninstall_windows().await;
    #[allow(unreachable_code)]
    Err(DaemonError::UnsupportedPlatform)
}

// ── macOS LaunchAgent ─────────────────────────────────────────────────────

// macOS LaunchAgent install — called by install() on macOS.
#[cfg(target_os = "macos")]
#[allow(dead_code)]
async fn install_macos(daemon_bin: &Path) -> Result<InstallResult, DaemonError> {
    let plist_path = macos_plist_path()?;

    // Idempotency: if plist exists AND service is loaded, skip. A missing
    // file is the only reliable zero-admin state check on macOS — `launchctl
    // print` returns a permission error for non-root callers in some versions.
    if plist_path.exists() {
        let output = tokio::process::Command::new("launchctl")
            .args(["list", "dev.cascade.daemon"])
            .output()
            .await
            .map_err(DaemonError::Io)?;
        if output.status.success() {
            info!("LaunchAgent already loaded — skipping install");
            return Ok(InstallResult::AlreadyInstalled);
        }
    }

    let plist_content = macos_plist_template(daemon_bin);
    if let Some(parent) = plist_path.parent() {
        tokio::fs::create_dir_all(parent)
            .await
            .map_err(DaemonError::Io)?;
    }
    tokio::fs::write(&plist_path, plist_content)
        .await
        .map_err(DaemonError::Io)?;

    let status = tokio::process::Command::new("launchctl")
        .args(["load", "-w", plist_path.to_str().unwrap_or_default()])
        .status()
        .await
        .map_err(DaemonError::Io)?;

    if !status.success() {
        return Err(DaemonError::InstallFailed("launchctl load failed".into()));
    }

    info!(plist = %plist_path.display(), "LaunchAgent installed and loaded");
    Ok(InstallResult::Installed)
}

// macOS LaunchAgent uninstall — called by uninstall() on macOS.
#[cfg(target_os = "macos")]
#[allow(dead_code)]
async fn uninstall_macos() -> Result<(), DaemonError> {
    let plist_path = macos_plist_path()?;
    if plist_path.exists() {
        let _ = tokio::process::Command::new("launchctl")
            .args(["unload", "-w", plist_path.to_str().unwrap_or_default()])
            .status()
            .await;
        tokio::fs::remove_file(&plist_path)
            .await
            .map_err(DaemonError::Io)?;
        info!("LaunchAgent uninstalled");
    } else {
        warn!("plist not found — nothing to uninstall");
    }
    Ok(())
}

// Returns the LaunchAgent plist path for this daemon on macOS.
#[cfg(target_os = "macos")]
#[allow(dead_code)]
fn macos_plist_path() -> Result<PathBuf, DaemonError> {
    let home = dirs::home_dir().ok_or(DaemonError::NoHomeDir)?;
    Ok(home
        .join("Library")
        .join("LaunchAgents")
        .join("dev.cascade.daemon.plist"))
}

// Generates the plist XML content for the macOS LaunchAgent.
#[cfg(target_os = "macos")]
#[allow(dead_code)]
fn macos_plist_template(daemon_bin: &Path) -> String {
    let bin = daemon_bin.display();
    let home = dirs::home_dir()
        .map(|h| h.display().to_string())
        .unwrap_or_default();
    format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>             <string>dev.cascade.daemon</string>
    <key>ProgramArguments</key> <array><string>{bin}</string></array>
    <key>KeepAlive</key>         <true/>
    <key>ThrottleInterval</key>  <integer>10</integer>
    <key>RunAtLoad</key>         <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key><string>{home}</string>
        <key>CASCADE_LOG_FORMAT</key><string>json</string>
        <key>CASCADE_SUPERVISED</key><string>launchd</string>
    </dict>
    <key>StandardOutPath</key>
    <string>{home}/Library/Logs/cascade-daemon.log</string>
    <key>StandardErrorPath</key>
    <string>{home}/Library/Logs/cascade-daemon-err.log</string>
</dict>
</plist>
"#
    )
}

// ── Linux systemd ─────────────────────────────────────────────────────────

#[cfg(target_os = "linux")]
async fn install_linux(daemon_bin: &Path) -> Result<InstallResult, DaemonError> {
    let unit_path = linux_unit_path()?;

    if unit_path.exists() {
        info!("systemd unit already present — reloading");
    }

    let unit_content = linux_unit_template(daemon_bin);
    if let Some(parent) = unit_path.parent() {
        tokio::fs::create_dir_all(parent)
            .await
            .map_err(DaemonError::Io)?;
    }
    tokio::fs::write(&unit_path, unit_content)
        .await
        .map_err(DaemonError::Io)?;

    // daemon-reload so systemd picks up the new unit file.
    let _ = tokio::process::Command::new("systemctl")
        .args(["--user", "daemon-reload"])
        .status()
        .await;

    let status = tokio::process::Command::new("systemctl")
        .args(["--user", "enable", "--now", "cascade.service"])
        .status()
        .await
        .map_err(DaemonError::Io)?;

    if !status.success() {
        return Err(DaemonError::InstallFailed("systemctl enable failed".into()));
    }

    info!(unit = %unit_path.display(), "systemd user unit installed and started");
    Ok(InstallResult::Installed)
}

#[cfg(target_os = "linux")]
async fn uninstall_linux() -> Result<(), DaemonError> {
    let _ = tokio::process::Command::new("systemctl")
        .args(["--user", "disable", "--now", "cascade.service"])
        .status()
        .await;
    let unit_path = linux_unit_path()?;
    if unit_path.exists() {
        tokio::fs::remove_file(&unit_path)
            .await
            .map_err(DaemonError::Io)?;
    }
    let _ = tokio::process::Command::new("systemctl")
        .args(["--user", "daemon-reload"])
        .status()
        .await;
    info!("systemd user unit removed");
    Ok(())
}

#[cfg(target_os = "linux")]
fn linux_unit_path() -> Result<PathBuf, DaemonError> {
    let config = dirs::config_dir().ok_or(DaemonError::NoHomeDir)?;
    Ok(config.join("systemd").join("user").join("cascade.service"))
}

#[cfg(target_os = "linux")]
fn linux_unit_template(daemon_bin: &Path) -> String {
    let bin = daemon_bin.display();
    format!(
        r#"[Unit]
Description=Cascade AI context daemon
After=network.target

[Service]
Type=simple
ExecStart={bin}
Restart=always
RestartSec=10
Environment=CASCADE_LOG_FORMAT=json
# Exponential backoff is handled in supervisor.rs — systemd provides base restart.

[Install]
WantedBy=default.target
"#
    )
}

// ── Windows Service ───────────────────────────────────────────────────────

#[cfg(target_os = "windows")]
async fn install_windows(daemon_bin: &Path) -> Result<InstallResult, DaemonError> {
    // windows-service-rs ServiceMain entrypoint is in lib.rs for the Windows
    // feature gate. Here we register via sc.exe so no elevation is needed for
    // the user-session service type.
    let bin = daemon_bin.to_string_lossy();
    let output = tokio::process::Command::new("sc")
        .args([
            "create",
            "CascadeDaemon",
            "binPath=",
            &bin,
            "start=",
            "auto",
            "type=",
            "userown",
        ])
        .output()
        .await
        .map_err(DaemonError::Io)?;

    if !output.status.success() {
        let msg = String::from_utf8_lossy(&output.stderr);
        // 1073 = service already exists — treat as idempotent.
        if !msg.contains("1073") {
            return Err(DaemonError::InstallFailed(msg.into_owned()));
        }
        return Ok(InstallResult::AlreadyInstalled);
    }

    info!("Windows service CascadeDaemon registered");
    Ok(InstallResult::Installed)
}

#[cfg(target_os = "windows")]
async fn uninstall_windows() -> Result<(), DaemonError> {
    let _ = tokio::process::Command::new("sc")
        .args(["stop", "CascadeDaemon"])
        .status()
        .await;
    let _ = tokio::process::Command::new("sc")
        .args(["delete", "CascadeDaemon"])
        .status()
        .await;
    info!("Windows service CascadeDaemon removed");
    Ok(())
}

// ── Shared types ─────────────────────────────────────────────────────────

// Result type returned by install() — used to report installed vs already-installed state.
#[allow(dead_code)]
#[derive(Debug, PartialEq)]
pub enum InstallResult {
    Installed,
    AlreadyInstalled,
}

#[derive(Debug, thiserror::Error)]
pub enum DaemonError {
    #[error("I/O error: {0}")]
    Io(#[from] std::io::Error),
    #[error("install failed: {0}")]
    // Error variant — returned by install_macos/linux/windows on failure.
    #[allow(dead_code)]
    InstallFailed(String),
    #[error("no home directory")]
    // Error variant — returned when dirs::home_dir() returns None during install.
    #[allow(dead_code)]
    NoHomeDir,
    #[error("unsupported platform")]
    UnsupportedPlatform,
    #[error("event bus error: {0}")]
    EventBus(String),
    #[error("IPC error: {0}")]
    // Error variant — returned when an IPC operation fails during supervisor init.
    #[allow(dead_code)]
    Ipc(String),
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use cascade_core::task_store::TaskStore;
    use cascade_types::scheduled_task::{ScheduleSpec, ScheduledTask};
    use std::sync::{Arc, Mutex};
    use tempfile::TempDir;

    /// Fix 2 regression guard: the scheduler store must be a persistent
    /// SQLite file under the daemon's config_dir (not `:memory:`), so tasks
    /// survive a daemon restart. This test opens the store, inserts a task,
    /// drops the connection (simulating process exit), then re-opens the
    /// same file path and verifies the task is still there.
    #[test]
    fn test_scheduler_store_persists_across_reopen() {
        let tmp = TempDir::new().unwrap();
        let config_dir = tmp.path();
        let sched_db_path = config_dir.join("scheduler.db");

        let task_id = {
            let conn = cascade_db::open_configured(&sched_db_path)
                .expect("open scheduler.db (first open)");
            conn.execute_batch(
                "CREATE TABLE IF NOT EXISTS scheduled_tasks (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL,
                    schedule_json TEXT NOT NULL,
                    command TEXT NOT NULL,
                    args_json TEXT NOT NULL,
                    enabled INTEGER NOT NULL DEFAULT 1,
                    created_at TEXT NOT NULL,
                    last_run TEXT,
                    next_run TEXT,
                    status_json TEXT NOT NULL
                );",
            )
            .expect("create scheduled_tasks table");

            let store = TaskStore::new(Arc::new(Mutex::new(conn)));
            let task = ScheduledTask::new(
                "test.persisted-task",
                ScheduleSpec::Interval { secs: 60 },
                "echo",
                vec!["hi".to_string()],
            );
            let id = task.id;
            store.insert(&task).expect("insert task");
            id
            // `conn` (inside `store`) is dropped here, closing the file.
        };

        // Re-open the SAME path — this only proves persistence if the file
        // actually exists on disk with real data (an in-memory DB would have
        // no file to re-open, or a fresh file would be empty).
        assert!(sched_db_path.exists(), "scheduler.db must exist on disk");

        let conn2 =
            cascade_db::open_configured(&sched_db_path).expect("open scheduler.db (second open)");
        let store2 = TaskStore::new(Arc::new(Mutex::new(conn2)));

        let reloaded = store2
            .get(task_id)
            .expect("query must succeed")
            .expect("task must still exist after reopen");
        assert_eq!(reloaded.name, "test.persisted-task");
        assert_eq!(reloaded.command, "echo");
    }
}
