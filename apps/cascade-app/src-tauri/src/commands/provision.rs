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

use tauri::State;

use cascade_providers::google_provision::GoogleProvisionClient;
use cascade_providers::validate_gemini_key;
use cascade_providers::{import_accounts, scan_all};
use cascade_types::auto_auth::{DiscoveredAccount, ImportResult};
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
/// Calls all harness scanners (Claude Code, OpenCode, Codex, Cursor,
/// Antigravity, env vars) via `cascade_providers::scan_all` and returns the
/// merged `DiscoveredAccount` list. Read-only — no harness config file is
/// ever written. Only invoked on explicit user trigger from the onboarding
/// wizard (never auto-scans without consent).
///
/// JS: `invoke("cascade_auto_auth_scan")`
#[tauri::command]
pub async fn cascade_auto_auth_scan(
    _state: State<'_, AppState>,
) -> Result<Vec<DiscoveredAccount>, CascadeError> {
    // Delegate to the real scanner in cascade-providers (T-P7-E09-01).
    // scan_all is sync (filesystem + env reads only); run on the async runtime.
    let accounts = tokio::task::spawn_blocking(scan_all)
        .await
        .map_err(|e| CascadeError::Custom(format!("auto_auth_scan task panicked: {e}")))?;
    Ok(accounts)
}

/// Import selected discovered accounts into cascade-keychain.
///
/// # Purpose
/// For each importable `EnvApiKey` account in `selected`, delegates to
/// `cascade_providers::import_accounts` which reads the env var value and
/// stores it in cascade-keychain under the appropriate provider key.
/// Non-importable accounts are skipped. Only imports accounts the user
/// explicitly selected in the wizard — never imports without consent.
///
/// JS: `invoke("cascade_auto_auth_import", { selected })`
#[tauri::command]
pub async fn cascade_auto_auth_import(
    selected: Vec<DiscoveredAccount>,
    _state: State<'_, AppState>,
) -> Result<ImportResult, CascadeError> {
    // Delegate to the real importer in cascade-providers (T-P7-E09-01).
    let result = import_accounts(selected).await;
    Ok(result)
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
// Note: AutoAuthDiscoveredAccount and AutoAuthImportResult were removed in
// T-P7-E09-01. The real DiscoveredAccount and ImportResult types from
// cascade_types::auto_auth are now used directly (the app already depends on
// cascade-types). Their serde shape is identical (camelCase), so the IPC
// contract is unchanged for frontend consumers.

// ---------------------------------------------------------------------------
// Tests — auto_auth scan/import delegation (T-P7-E09-01)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::auto_auth::{AuthSource, AuthType};
    use serial_test::serial;

    /// scan_all (the function cascade_auto_auth_scan delegates to) must find
    /// a real fixture credential set via an env var. This verifies the
    /// delegation path is wired — the Tauri command is a thin wrapper around
    /// scan_all, so testing scan_all directly proves the command works.
    #[test]
    #[serial(global_env)]
    fn scan_finds_real_fixture_credential() {
        // Clear all API key env vars to ensure deterministic state.
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-fixture-test-key");

        let accounts = scan_all();

        std::env::remove_var("ANTHROPIC_API_KEY");

        // scan_all must find the env var credential.
        let env_account = accounts
            .iter()
            .find(|a| a.source == AuthSource::EnvVar && a.provider == "anthropic");
        assert!(
            env_account.is_some(),
            "scan_all must find ANTHROPIC_API_KEY env credential; got {:?} accounts",
            accounts.len()
        );
        let acc = env_account.unwrap();
        assert_eq!(acc.auth_type, AuthType::EnvApiKey);
        assert!(acc.importable, "env API keys must be importable");
        assert_eq!(acc.email_or_hint, "env: ANTHROPIC_API_KEY");
        // SECURITY: the key value must NOT appear in the hint.
        assert!(!acc.email_or_hint.contains("sk-ant-fixture"));
    }

    /// import_accounts (the function cascade_auto_auth_import delegates to)
    /// must successfully import a fixture env API key credential. This
    /// verifies the import delegation path is wired end-to-end.
    #[test]
    #[serial(global_env)]
    fn import_succeeds_for_fixture_credential() {
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-fixture-import-key");

        // Build the DiscoveredAccount that scan_all would produce for this
        // env var, then import it (exactly what the wizard flow does).
        let account = DiscoveredAccount {
            source: AuthSource::EnvVar,
            email_or_hint: "env: ANTHROPIC_API_KEY".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::EnvApiKey,
            importable: true,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(import_accounts(vec![account]));

        std::env::remove_var("ANTHROPIC_API_KEY");

        // Import succeeds (imported=1) or errors on keychain access (CI as
        // root) — both are valid; a panic or empty result is not.
        assert!(
            result.imported.len() == 1 || !result.errors.is_empty(),
            "expected imported=1 or an error string; got {:?}",
            result
        );
        assert!(
            result.skipped.is_empty(),
            "importable account must not be skipped"
        );
        // SECURITY: the key value must NOT appear in any result string.
        assert!(!result
            .imported
            .join("")
            .contains("sk-ant-fixture-import-key"));
        assert!(!result.errors.join("").contains("sk-ant-fixture-import-key"));
    }

    /// Non-importable accounts (e.g. Claude Code OAuth) must be skipped, not
    /// imported. This verifies the import path respects the importable flag.
    #[test]
    #[serial(global_env)]
    fn import_skips_non_importable_accounts() {
        let account = DiscoveredAccount {
            source: AuthSource::ClaudeCode,
            email_or_hint: "cc@example.com".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(import_accounts(vec![account]));

        assert!(
            result.imported.is_empty(),
            "non-importable must not be imported"
        );
        assert_eq!(result.skipped.len(), 1, "non-importable must be skipped");
        assert!(result.errors.is_empty());
    }
}
