// AI provider connection and daemon install commands (T-P3-E03-06/08).
//
// Why: groups the one-time setup commands (provider_connect, detect_gemini_pool,
// download_local_model, install_daemon, wizard_mark_complete) so they can grow
// independently without crowding the core IPC surface.

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::PathBuf;

use super::wizard::get_home_dir;

// ---------------------------------------------------------------------------
// Result types
// ---------------------------------------------------------------------------

/// Result returned by `detect_gemini_pool`.
/// Serialized as camelCase for the TypeScript provider-connect phase.
#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GeminiPoolDetectResult {
    /// True if a Gemini proxy pool was found at localhost:3761/health.
    pub detected: bool,
    /// Human-readable status message shown in the provider card badge.
    pub status_message: String,
}

/// Result returned by `provider_connect`.
/// Serialized as camelCase for the TypeScript provider-connect phase.
#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProviderConnectResult {
    /// Whether the connection/validation succeeded.
    pub success: bool,
    /// Provider id that was connected (echoed back).
    pub provider_id: String,
    /// Human-readable message (success detail or error description).
    pub message: String,
}

/// Result returned by `download_local_model`.
/// Serialized as camelCase for the TypeScript offline-toggle progress bar.
#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LocalModelDownloadResult {
    /// True if the pull completed without error.
    pub success: bool,
    /// The model variant that was requested.
    pub model_variant: String,
    /// stdout/stderr captured from the ollama pull command.
    pub output: String,
}

/// Result returned by `install_daemon`.
/// Serialized as camelCase for the TypeScript daemon-install phase.
#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DaemonInstallResult {
    /// True if the daemon was successfully installed and loaded.
    pub success: bool,
    /// Absolute path to the written plist file.
    pub plist_path: String,
    /// Human-readable status message for the UI.
    pub message: String,
}

// ---------------------------------------------------------------------------
// Setup commands
// ---------------------------------------------------------------------------

/// Detect whether a Gemini proxy pool is reachable at localhost:3761.
///
/// Purpose: auto-populate the "Gemini Pool detected" badge in ProviderConnectPhase
///          without requiring the user to enter credentials.
/// Inputs: none
/// Outputs: GeminiPoolDetectResult { detected, statusMessage }
/// Constraints: 500 ms HTTP timeout; never panics; returns Ok on timeout (detected = false).
/// SPORT: T-P3-E03-06
#[tauri::command]
pub async fn detect_gemini_pool() -> Result<GeminiPoolDetectResult, String> {
    use std::time::Duration;

    let client = reqwest::Client::builder()
        .timeout(Duration::from_millis(500))
        .build()
        .map_err(|e| format!("failed to build http client: {}", e))?;

    match client.get("http://127.0.0.1:3761/health").send().await {
        Ok(resp) if resp.status().is_success() => Ok(GeminiPoolDetectResult {
            detected: true,
            status_message: "Gemini Pool detected".to_string(),
        }),
        Ok(resp) => Ok(GeminiPoolDetectResult {
            detected: false,
            status_message: format!("Gemini proxy returned HTTP {}", resp.status()),
        }),
        Err(_) => Ok(GeminiPoolDetectResult {
            detected: false,
            status_message: "Gemini Pool not detected".to_string(),
        }),
    }
}

/// Connect / validate an AI provider by id and optional API key credential.
///
/// Purpose: Delegates to `cascade_providers::connect_provider` to validate the
///          supplied API key against the provider's live auth endpoint, then
///          stores the secret in the OS keychain and records the connection in
///          `~/.cascade/providers.json`.  OAuth providers (opencode, openai-oauth,
///          gemini-oauth) pass `credential = None`; the PKCE flow stores the
///          token in keychain before this command is called.
/// Inputs: provider_id (e.g. "anthropic", "openai", "gemini"), credential (API key or None for OAuth)
/// Outputs: ProviderConnectResult { success, providerId, message }
/// Constraints: Secrets NEVER stored plaintext or logged. Result shape is stable (frozen IPC).
/// SPORT: T-P3-E03-06 / E-P5-08
#[tauri::command]
pub async fn provider_connect(
    provider_id: String,
    credential: Option<String>,
) -> Result<ProviderConnectResult, String> {
    use cascade_keychain::platform_keychain;
    use cascade_providers::{connect_provider, Credential, ProviderKind};

    if provider_id.is_empty() {
        return Err("provider_id must not be empty".to_string());
    }

    // Parse the provider kind. Unknown slugs fall back to a graceful error.
    let kind = match ProviderKind::from_slug(&provider_id) {
        Some(k) => k,
        None => {
            return Ok(ProviderConnectResult {
                success: false,
                provider_id: provider_id.clone(),
                message: format!(
                    "Unknown provider '{}'. Supported: anthropic, openai, gemini, openrouter, groq, mistral, deepseek, together, cohere.",
                    provider_id
                ),
            });
        }
    };

    // Determine credential type.
    let cred = match credential {
        Some(key) if !key.is_empty() => Credential::ApiKey(key),
        _ => Credential::OAuth,
    };

    let kc = platform_keychain();
    match connect_provider(kind, cred, kc.as_ref()).await {
        Ok(connected) => Ok(ProviderConnectResult {
            success: true,
            provider_id: connected.id.clone(),
            message: format!(
                "Provider '{}' connected and validated successfully.",
                connected.name
            ),
        }),
        Err(e) => Ok(ProviderConnectResult {
            success: false,
            provider_id: provider_id.clone(),
            message: format!("Connection failed: {e}"),
        }),
    }
}

/// Trigger a local model pull via `ollama pull <model>`.
///
/// Purpose: allow the user to opt in to offline operation by downloading a local model.
/// Inputs: variant — one of "gemma-2-2b" or "llama-3.2-3b"
/// Outputs: LocalModelDownloadResult { success, modelVariant, output }
/// Constraints: discrete args (no bash -c), blocks until pull completes, ~seconds to minutes.
/// SPORT: T-P3-E03-06
#[tauri::command]
pub async fn download_local_model(variant: String) -> Result<LocalModelDownloadResult, String> {
    // Allowlist validated model variants — never pass arbitrary user input to the shell.
    let allowed_variants = ["gemma-2-2b", "llama-3.2-3b"];
    if !allowed_variants.contains(&variant.as_str()) {
        return Err(format!(
            "unsupported model variant '{}'; allowed: {}",
            variant,
            allowed_variants.join(", ")
        ));
    }

    // Discrete args — NEVER bash -c — to prevent injection.
    let output = std::process::Command::new("ollama")
        .arg("pull")
        .arg(&variant)
        .output()
        .map_err(|e| format!("failed to launch ollama: {}", e))?;

    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    Ok(LocalModelDownloadResult {
        success: output.status.success(),
        model_variant: variant,
        output: combined,
    })
}

/// Install the cascaded daemon as a user launchd agent (macOS), Windows service,
/// or Linux systemd user unit.
///
/// Purpose: write the platform-native service definition under the user's HOME
///          and start it, so cascaded starts on login without requiring root/admin.
/// Inputs: none (service definition is embedded; label is fixed to "dev.cascade.daemon")
/// Outputs: DaemonInstallResult { success, plistPath, message }
/// Constraints:
///   - macOS: writes only within ~/Library/LaunchAgents; atomic write (tmp+rename);
///     no shell-string injection; no admin required.
///   - Windows: registers a Windows service via the SCM (requires admin UAC);
///     uses the windows-service crate.
///   - Linux: writes the existing packaging/aur/cascade@.service unit to
///     ~/.config/systemd/user/ and enables it via systemctl --user.
///   - Does NOT touch the separate CLI `cascade daemon install` path.
/// SPORT: T-P3-E03-08 / T-P7-E09-05
#[tauri::command]
pub async fn install_daemon() -> Result<DaemonInstallResult, String> {
    let home = get_home_dir().ok_or_else(|| "could not resolve home directory".to_string())?;

    #[cfg(target_os = "macos")]
    {
        return install_daemon_macos(&home).await;
    }

    #[cfg(target_os = "windows")]
    {
        return install_daemon_windows(&home).await;
    }

    #[cfg(target_os = "linux")]
    {
        return install_daemon_linux(&home).await;
    }

    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        Err("install_daemon: unsupported platform".to_string())
    }
}

// ---------------------------------------------------------------------------
// macOS: LaunchAgent (unchanged from original implementation)
// ---------------------------------------------------------------------------

/// The existing systemd user unit file content from packaging/aur/cascade@.service.
/// Embedded at compile time so the desktop app does not need the packaging dir
/// at runtime. T-P7-E09-05 wires the Linux install path to this existing unit.
#[cfg(any(target_os = "linux", doc))]
const SYSTEMD_UNIT_CONTENT: &str = r#"[Unit]
Description=Cascade AI context daemon
Documentation=https://github.com/acamarata/cascade/wiki
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/cascaded
Restart=on-failure
RestartSec=5s
# Allow user's home directory access for cascade root discovery
Environment=HOME=%h

[Install]
WantedBy=default.target
"#;

/// Install cascaded as a macOS LaunchAgent (user-level, no admin required).
///
/// Writes the plist to ~/Library/LaunchAgents/dev.cascade.daemon.plist and loads
/// it via `launchctl load -w`. This is the original macOS path, unchanged.
#[cfg(target_os = "macos")]
async fn install_daemon_macos(home: &std::path::Path) -> Result<DaemonInstallResult, String> {
    // Confine write target to ~/Library/LaunchAgents.
    let launch_agents_dir = home.join("Library").join("LaunchAgents");
    fs::create_dir_all(&launch_agents_dir)
        .map_err(|e| format!("failed to create LaunchAgents directory: {}", e))?;

    let plist_path = launch_agents_dir.join("dev.cascade.daemon.plist");

    // Resolve the daemon binary path: prefer the installed location.
    let daemon_bin = home.join(".local").join("bin").join("cascaded");

    let plist_content = format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.cascade.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>{}</string>
        <string>serve</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{}</string>
    <key>StandardErrorPath</key>
    <string>{}</string>
</dict>
</plist>
"#,
        daemon_bin.to_string_lossy(),
        home.join("Library")
            .join("Logs")
            .join("cascade-daemon.log")
            .to_string_lossy(),
        home.join("Library")
            .join("Logs")
            .join("cascade-daemon-err.log")
            .to_string_lossy(),
    );

    // Atomic write: tmp → rename.
    let tmp_path = {
        let mut p = plist_path.as_os_str().to_owned();
        p.push(".tmp");
        PathBuf::from(p)
    };
    fs::write(&tmp_path, &plist_content).map_err(|e| format!("failed to write plist: {}", e))?;
    fs::rename(&tmp_path, &plist_path).map_err(|e| format!("failed to finalize plist: {}", e))?;

    // Load the agent (discrete args — no shell injection).
    let load_result = std::process::Command::new("launchctl")
        .arg("load")
        .arg("-w")
        .arg(&plist_path)
        .output();

    let message = match load_result {
        Ok(out) if out.status.success() => {
            "cascaded daemon installed and loaded as LaunchAgent".to_string()
        }
        Ok(out) => format!(
            "plist written but launchctl load returned non-zero: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ),
        Err(e) => format!("plist written but launchctl not available: {}", e),
    };

    Ok(DaemonInstallResult {
        success: true,
        plist_path: plist_path.to_string_lossy().to_string(),
        message,
    })
}

// ---------------------------------------------------------------------------
// Windows: Windows service via SCM (requires admin UAC)
// ---------------------------------------------------------------------------

/// Install cascaded as a Windows service via the Service Control Manager.
///
/// Registers the service with `ServiceStartType::AutoStart` so it starts at
/// boot, matching the macOS `RunAtLoad` + `KeepAlive` behavior. Requires
/// admin privileges (UAC prompt). Uses the `windows-service` crate.
///
/// **Manual verification required:** This code cannot be tested on macOS.
/// It must be verified on a Windows machine with admin privileges.
#[cfg(target_os = "windows")]
async fn install_daemon_windows(home: &std::path::Path) -> Result<DaemonInstallResult, String> {
    use std::ffi::OsString;
    use windows_service::service::{
        ServiceAccess, ServiceErrorControl, ServiceInfo, ServiceStartType, ServiceType,
    };
    use windows_service::service_manager::{ServiceManager, ServiceManagerAccess};

    const SERVICE_NAME: &str = "CascadeDaemon";
    const SERVICE_DISPLAY_NAME: &str = "Cascade AI Context Daemon";
    const SERVICE_DESCRIPTION: &str =
        "Cascade AI context daemon — local DB-free context manager for Claude Code Extension.";

    // Resolve the daemon binary path.
    let daemon_bin = home.join(".local").join("bin").join("cascaded.exe");

    // Connect to the local Service Control Manager.
    let manager_access = ServiceManagerAccess::CONNECT | ServiceManagerAccess::CREATE_SERVICE;
    let service_manager = ServiceManager::local_computer(None::<&str>, manager_access)
        .map_err(|e| format!("failed to connect to Service Control Manager: {e}"))?;

    let service_info = ServiceInfo {
        name: OsString::from(SERVICE_NAME),
        display_name: OsString::from(SERVICE_DISPLAY_NAME),
        service_type: ServiceType::OWN_PROCESS,
        start_type: ServiceStartType::AutoStart,
        error_control: ServiceErrorControl::Normal,
        executable_path: daemon_bin.clone(),
        launch_arguments: vec!["serve".to_string()],
        dependencies: vec![],
        account_name: None, // run as LocalSystem
        account_password: None,
    };

    // Create or open the service (idempotent: if already exists, open it).
    let service_access =
        ServiceAccess::CHANGE_CONFIG | ServiceAccess::START | ServiceAccess::QUERY_STATUS;
    let service = service_manager
        .create_service(&service_info, service_access)
        .or_else(|_| service_manager.open_service(SERVICE_NAME, service_access))
        .map_err(|e| format!("failed to create or open service: {e}"))?;

    // Set the service description.
    service
        .set_description(SERVICE_DESCRIPTION)
        .map_err(|e| format!("failed to set service description: {e}"))?;

    // Start the service.
    let start_result = service.start(Vec::new());
    let message = match start_result {
        Ok(_) => "cascaded daemon installed and started as Windows service".to_string(),
        Err(e) => {
            format!("service registered but failed to start (it will start on next boot): {e}")
        }
    };

    Ok(DaemonInstallResult {
        success: true,
        plist_path: format!(r"\\.\{}\{}", "Services", SERVICE_NAME),
        message,
    })
}

// ---------------------------------------------------------------------------
// Linux: systemd user unit (wired to existing packaging/aur/cascade@.service)
// ---------------------------------------------------------------------------

/// Install cascaded as a Linux systemd user unit.
///
/// Writes the existing `packaging/aur/cascade@.service` unit file content to
/// `~/.config/systemd/user/cascade@.service`, then runs
/// `systemctl --user daemon-reload` and `systemctl --user enable --now
/// cascade@default.service`. No root required (user-level systemd).
///
/// **Manual verification required:** This code cannot be fully tested on macOS.
/// It must be verified on a Linux machine with systemd user units enabled.
#[cfg(target_os = "linux")]
async fn install_daemon_linux(home: &std::path::Path) -> Result<DaemonInstallResult, String> {
    // Write the existing systemd unit file to the user's systemd directory.
    let systemd_user_dir = home.join(".config").join("systemd").join("user");
    fs::create_dir_all(&systemd_user_dir)
        .map_err(|e| format!("failed to create systemd user directory: {}", e))?;

    let unit_path = systemd_user_dir.join("cascade@.service");

    // Atomic write: tmp → rename.
    let tmp_path = {
        let mut p = unit_path.as_os_str().to_owned();
        p.push(".tmp");
        PathBuf::from(p)
    };
    fs::write(&tmp_path, SYSTEMD_UNIT_CONTENT)
        .map_err(|e| format!("failed to write systemd unit: {}", e))?;
    fs::rename(&tmp_path, &unit_path)
        .map_err(|e| format!("failed to finalize systemd unit: {}", e))?;

    // Reload systemd to pick up the new unit.
    let reload_result = std::process::Command::new("systemctl")
        .args(["--user", "daemon-reload"])
        .output();

    let mut messages = Vec::new();

    match reload_result {
        Ok(out) if out.status.success() => {}
        Ok(out) => messages.push(format!(
            "systemctl --user daemon-reload returned non-zero: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )),
        Err(e) => messages.push(format!("systemctl not available: {e}")),
    }

    // Enable and start the service instance (cascade@default.service).
    // The `--now` flag both enables and starts in one command.
    let enable_result = std::process::Command::new("systemctl")
        .args(["--user", "enable", "--now", "cascade@default.service"])
        .output();

    match enable_result {
        Ok(out) if out.status.success() => {
            messages.push("cascaded daemon installed and started as systemd user unit".to_string());
        }
        Ok(out) => {
            messages.push(format!(
                "unit written but systemctl enable --now returned non-zero: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ));
        }
        Err(e) => {
            messages.push(format!("systemctl not available: {e}"));
        }
    }

    Ok(DaemonInstallResult {
        success: true,
        plist_path: unit_path.to_string_lossy().to_string(),
        message: messages.join("; "),
    })
}

/// Mark the onboarding wizard complete by writing ~/.cascade/.wizard-complete.
///
/// Purpose: allows check_wizard_status to return WizardStatus::Complete on next
///          app launch, preventing the wizard from re-running.
/// Inputs: none
/// Outputs: Ok(()) on success
/// Constraints: writes only to ~/.cascade/.wizard-complete; atomic touch (empty file).
/// SPORT: T-P3-E03-08
#[tauri::command]
pub async fn wizard_mark_complete() -> Result<(), String> {
    let home = get_home_dir().ok_or_else(|| "could not resolve home directory".to_string())?;

    let cascade_dir = home.join(".cascade");
    fs::create_dir_all(&cascade_dir)
        .map_err(|e| format!("failed to create .cascade directory: {}", e))?;

    let marker = cascade_dir.join(".wizard-complete");
    // Write empty file (atomic touch).
    fs::write(&marker, b"")
        .map_err(|e| format!("failed to write wizard-complete marker: {}", e))?;

    Ok(())
}
