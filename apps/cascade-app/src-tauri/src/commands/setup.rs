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

/// Install the cascaded daemon as a user launchd agent (macOS) or equivalent.
///
/// Purpose: write the platform plist / unit file under the user's HOME and load it,
///          so cascaded starts on login without requiring root/admin.
/// Inputs: none (plist template is embedded; label is fixed to "dev.cascade.daemon")
/// Outputs: DaemonInstallResult { success, plistPath, message }
/// Constraints: writes only within ~/Library/LaunchAgents; atomic write (tmp+rename);
///              no shell-string injection; no admin required.
/// SPORT: T-P3-E03-08
#[tauri::command]
pub async fn install_daemon() -> Result<DaemonInstallResult, String> {
    let home = get_home_dir().ok_or_else(|| "could not resolve home directory".to_string())?;

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
