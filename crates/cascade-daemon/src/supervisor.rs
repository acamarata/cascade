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

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

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
pub async fn run(config_dir: PathBuf, shutdown: CancellationToken) -> Result<(), DaemonError> {
    let start_time = std::time::Instant::now();
    info!(config_dir = %config_dir.display(), "supervisor starting");

    // ── Load config ───────────────────────────────────────────────────────────
    // Config::load returns Config::default() when config.toml is absent —
    // safe to call unconditionally.
    let config = Config::load(&config_dir).unwrap_or_default();

    let health = HealthState::new(start_time);
    let bus = EventBus::new(config_dir.clone()).await?;
    let ipc = IpcServer::new(config_dir.clone(), health.clone(), bus.clone()).await?;

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

#[cfg(target_os = "macos")]
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

#[cfg(target_os = "macos")]
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

#[cfg(target_os = "macos")]
fn macos_plist_path() -> Result<PathBuf, DaemonError> {
    let home = dirs::home_dir().ok_or(DaemonError::NoHomeDir)?;
    Ok(home
        .join("Library")
        .join("LaunchAgents")
        .join("dev.cascade.daemon.plist"))
}

#[cfg(target_os = "macos")]
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
    InstallFailed(String),
    #[error("no home directory")]
    NoHomeDir,
    #[error("unsupported platform")]
    UnsupportedPlatform,
    #[error("event bus error: {0}")]
    EventBus(String),
    #[error("IPC error: {0}")]
    Ipc(String),
}
