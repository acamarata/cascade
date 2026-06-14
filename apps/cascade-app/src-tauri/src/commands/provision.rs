//! # commands::provision
//!
//! Tauri IPC commands for the Gemini Pool provisioning wizard steps.
//!
//! ## Purpose
//! Bridges the React frontend to the `cascade-providers` provisioning engine
//! for T-P3-E03-39b (GCP project + API key provisioning) and T-P3-E03-40
//! (Gemini Pool key registration/deregistration).
//!
//! ## Scaffold note
//! These commands are stubs registered in the Tauri invoke handler. Real
//! implementation delegates to `cascade-providers` (FILL in T-P3-E03-39b/40).
//!
//! All commands are wired in `lib.rs` invoke_handler per the existing pattern.

use serde::{Deserialize, Serialize};
use tauri::State;

use cascade_providers::google_provision::GoogleProvisionClient;
use cascade_providers::validate_gemini_key;
use cascade_types::pool::{GeminiKeyEntry, GeminiPoolConfig, RegisterResult};
use cascade_types::provision::{ProvisionMode, ProvisionRequest, ProvisionResult, ProvisionStatus};

use crate::error::CascadeError;
use crate::state::{AppState, ProvisionStateMap};

// ---------------------------------------------------------------------------
// T-P3-E03-39b: GCP provisioning commands
// ---------------------------------------------------------------------------

/// Start GCP project + API key provisioning for a Google account.
///
/// # Purpose
/// Accepts a `ProvisionRequest` and dispatches to FullAuto / Guided / Manual
/// via `GoogleProvisionClient` from cascade-providers.
///
/// JS: `invoke("cascade_provision_google_start", { req })`
#[tauri::command]
pub async fn cascade_provision_google_start(
    req: ProvisionRequest,
    state: State<'_, AppState>,
) -> Result<ProvisionResult, CascadeError> {
    use crate::state::EmailProvStatus;

    let email = req.account_email.clone();
    let prov_map = state.provision_state.clone();

    // Init status.
    {
        let mut map = prov_map.lock().await;
        map.insert(
            email.clone(),
            EmailProvStatus {
                status: "starting".to_string(),
                done: false,
                error: None,
                cancel: false,
            },
        );
    }

    let result = match req.mode {
        ProvisionMode::FullAuto => {
            let oauth_token = match req.client_id.as_deref() {
                Some(t) if !t.is_empty() => t.to_string(),
                _ => {
                    return Ok(ProvisionResult::Error {
                        message: "FullAuto mode requires an OAuth access token (client_id)"
                            .to_string(),
                    });
                }
            };
            prov_set_status(&prov_map, &email, "Creating GCP project\u{2026}").await;
            let client = GoogleProvisionClient::new(oauth_token);
            match client.full_auto(&email, 1).await {
                Ok(r) => r,
                Err(e) => ProvisionResult::Error {
                    message: e.to_string(),
                },
            }
        }

        ProvisionMode::Guided => {
            let client = GoogleProvisionClient::new(String::new());
            client.guided(0)
        }

        ProvisionMode::Manual => {
            let api_key = match req.client_secret.as_deref() {
                Some(k) if !k.is_empty() => k.to_string(),
                _ => {
                    return Ok(ProvisionResult::Error {
                        message: "Manual mode requires the API key in client_secret".to_string(),
                    });
                }
            };
            prov_set_status(&prov_map, &email, "Validating API key\u{2026}").await;
            match validate_gemini_key(&api_key).await {
                Ok(()) => ProvisionResult::Success {
                    api_key,
                    project_id: String::new(),
                },
                Err(e) => ProvisionResult::Error {
                    message: e.to_string(),
                },
            }
        }
    };

    // Update done status.
    let error = if let ProvisionResult::Error { message: ref msg } = result {
        Some(msg.clone())
    } else {
        None
    };
    prov_set_done(&prov_map, &email, error).await;

    Ok(result)
}

async fn prov_set_status(map: &ProvisionStateMap, email: &str, msg: &str) {
    let mut m = map.lock().await;
    if let Some(s) = m.get_mut(email) {
        s.status = msg.to_string();
    }
}

async fn prov_set_done(map: &ProvisionStateMap, email: &str, error: Option<String>) {
    let mut m = map.lock().await;
    if let Some(s) = m.get_mut(email) {
        s.done = true;
        s.error = error;
        s.status = if s.error.is_some() {
            "error".into()
        } else {
            "complete".into()
        };
    }
}

/// Poll the status of an in-progress GCP provisioning operation.
///
/// JS: `invoke("cascade_provision_google_status", { email })`
#[tauri::command]
pub async fn cascade_provision_google_status(
    email: String,
    state: State<'_, AppState>,
) -> Result<ProvisionStatus, CascadeError> {
    let map = state.provision_state.lock().await;
    Ok(match map.get(&email) {
        Some(s) => ProvisionStatus {
            account_email: email,
            status: s.status.clone(),
            done: s.done,
            error: s.error.clone(),
            // Per-key progress is tracked on the daemon GFP path (E-P7-04);
            // the wizard's single-key Tauri flow reports 0 here.
            keys_created: 0,
            keys_target: 0,
        },
        None => ProvisionStatus {
            account_email: email,
            status: "not_started".to_string(),
            done: false,
            error: None,
            keys_created: 0,
            keys_target: 0,
        },
    })
}

/// Cancel an in-progress GCP provisioning operation.
///
/// JS: `invoke("cascade_provision_google_cancel", { email })`
#[tauri::command]
pub async fn cascade_provision_google_cancel(
    email: String,
    state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    let mut m = state.provision_state.lock().await;
    if let Some(s) = m.get_mut(&email) {
        s.cancel = true;
        s.status = "cancelling".to_string();
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// T-P3-E03-40: Gemini Pool key registration commands
// ---------------------------------------------------------------------------

/// Register a Gemini API key in the central pool config.
///
/// Validates the key, appends an entry to `~/.cascade/pool/gemini-keys.json`
/// (atomic write), and signals the P2 pool daemon to hot-reload.
///
/// JS: `invoke("cascade_pool_register_key", { email, apiKey, projectId })`
#[tauri::command]
pub async fn cascade_pool_register_key(
    email: String,
    api_key: String,
    project_id: String,
    _state: State<'_, AppState>,
) -> Result<RegisterResult, CascadeError> {
    use chrono::Utc;

    let config_path = dirs_pool_config_path();

    // Read current config.
    let mut config = read_pool_config_app(&config_path)?;

    // Duplicate guard.
    if config.keys.iter().any(|k| k.email == email) {
        return Ok(RegisterResult::AlreadyRegistered);
    }

    // Validate key (never log api_key).
    match validate_gemini_key(&api_key).await {
        Ok(()) => {}
        Err(e) => {
            return Ok(RegisterResult::InvalidKey {
                message: e.to_string(),
            })
        }
    }

    // Append entry.
    config.keys.push(GeminiKeyEntry {
        email,
        api_key,
        project_id,
        added_at: Utc::now(),
        enabled: true,
    });

    // Atomic write.
    write_pool_config_app(&config, &config_path)?;

    // Signal reload.
    pool_reload_signal();

    Ok(RegisterResult::Success)
}

/// Deregister a Gemini API key from the central pool config.
///
/// JS: `invoke("cascade_pool_deregister_key", { email })`
#[tauri::command]
pub async fn cascade_pool_deregister_key(
    email: String,
    _state: State<'_, AppState>,
) -> Result<RegisterResult, CascadeError> {
    let config_path = dirs_pool_config_path();
    let mut config = read_pool_config_app(&config_path)?;
    config.keys.retain(|k| k.email != email);
    write_pool_config_app(&config, &config_path)?;
    pool_reload_signal();
    Ok(RegisterResult::Success)
}

// ── Pool config helpers (Tauri-app local copy; daemon has its own) ────────────

fn dirs_pool_config_path() -> std::path::PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"))
        .join(".cascade")
        .join("pool")
        .join("gemini-keys.json")
}

fn read_pool_config_app(path: &std::path::Path) -> Result<GeminiPoolConfig, CascadeError> {
    if !path.exists() {
        return Ok(GeminiPoolConfig::default());
    }
    let data = std::fs::read_to_string(path).map_err(|e| CascadeError::Custom(e.to_string()))?;
    serde_json::from_str(&data).map_err(|e| CascadeError::Custom(e.to_string()))
}

fn write_pool_config_app(
    config: &GeminiPoolConfig,
    path: &std::path::Path,
) -> Result<(), CascadeError> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| CascadeError::Custom(e.to_string()))?;
    }
    let mut tmp = path.to_path_buf();
    let mut name = tmp.file_name().unwrap_or_default().to_owned();
    name.push(".tmp");
    tmp.set_file_name(name);
    let json =
        serde_json::to_string_pretty(config).map_err(|e| CascadeError::Custom(e.to_string()))?;
    std::fs::write(&tmp, json).map_err(|e| CascadeError::Custom(e.to_string()))?;
    std::fs::rename(&tmp, path).map_err(|e| CascadeError::Custom(e.to_string()))?;
    Ok(())
}

#[cfg(unix)]
fn pool_reload_signal() {
    let pid_path = dirs::home_dir()
        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"))
        .join(".cascade")
        .join("daemon.pid");
    if let Ok(s) = std::fs::read_to_string(&pid_path) {
        if let Ok(pid) = s.trim().parse::<i32>() {
            unsafe { libc::kill(pid, libc::SIGHUP) };
        }
    }
}

#[cfg(not(unix))]
fn pool_reload_signal() {}

// ---------------------------------------------------------------------------
// T-P3-E03-41: Auto-auth scan + import commands
// ---------------------------------------------------------------------------

/// Scan installed harnesses for known accounts and API keys.
///
/// # Purpose
/// Calls all five harness scanners (CC, OpenCode, Codex, Cursor, env vars)
/// and returns the merged `DiscoveredAccount` list. Read-only — no harness
/// config file is ever written.
///
/// JS: `invoke("cascade_auto_auth_scan")`
/// FILL: T-P3-E03-41 — delegate to daemon IPC auto_auth_scan handler.
#[tauri::command]
pub async fn cascade_auto_auth_scan(
    _state: State<'_, AppState>,
) -> Result<Vec<AutoAuthDiscoveredAccount>, CascadeError> {
    // FILL: T-P3-E03-41 — call daemon IPC cascade_auto_auth_scan.
    Ok(vec![])
}

/// Import selected discovered accounts into cascade-keychain.
///
/// # Purpose
/// For each importable EnvApiKey account in `selected`, reads the env var
/// value and stores it in cascade-keychain under the appropriate provider key.
///
/// JS: `invoke("cascade_auto_auth_import", { selected })`
/// FILL: T-P3-E03-41 — delegate to daemon IPC auto_auth_import handler.
#[tauri::command]
pub async fn cascade_auto_auth_import(
    selected: Vec<AutoAuthDiscoveredAccount>,
    _state: State<'_, AppState>,
) -> Result<AutoAuthImportResult, CascadeError> {
    let _ = selected;
    // FILL: T-P3-E03-41 — call daemon IPC cascade_auto_auth_import.
    Ok(AutoAuthImportResult {
        imported: vec![],
        skipped: vec![],
        errors: vec![],
    })
}

// ---------------------------------------------------------------------------
// T-P3-E03-42: AI-optional provider health check
// ---------------------------------------------------------------------------

/// Check whether any AI provider is currently connected.
///
/// # Purpose
/// Used by `useProviderConnected` hook and `AIGatedStep` to determine whether
/// AI-powered wizard steps can be shown. Returns the IDs of all currently-healthy
/// providers.  Returns empty list when no providers are configured or all checks fail.
///
/// JS: `invoke("cascade_providers_health")`
/// T-P3-E04-15: reads from AppState.provider_health cache populated by
/// the background health-check task spawned in lib.rs setup().
/// Note: T-P3-E04-25 may later replace this with a live daemon IPC query.
#[tauri::command]
pub async fn cascade_providers_health(
    state: State<'_, AppState>,
) -> Result<Vec<String>, CascadeError> {
    let map = state.provider_health.read().await;
    let ids: Vec<String> = map
        .iter()
        .filter(|(_, h)| h.ok)
        .map(|(id, _)| id.clone())
        .collect();
    Ok(ids)
}

// ---------------------------------------------------------------------------
// Local response types (Tauri-native, not in cascade_types)
// ---------------------------------------------------------------------------

/// Frontend-facing DiscoveredAccount shape (mirrors cascade_types::auto_auth).
///
/// Defined here to avoid adding a cascade-types dependency to the app crate
/// before T-P3-E03-41 wires it through the daemon IPC layer.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AutoAuthDiscoveredAccount {
    pub source: String,
    pub email_or_hint: String,
    pub provider: String,
    pub auth_type: String,
    pub importable: bool,
}

/// Frontend-facing ImportResult shape.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AutoAuthImportResult {
    pub imported: Vec<String>,
    pub skipped: Vec<String>,
    pub errors: Vec<String>,
}
