//! # commands::providers
//!
//! Tauri IPC commands for the provider management layer (T-P3-E04-26/27).
//!
//! ## Purpose
//! Bridges the React frontend to the daemon's `ipc_providers` handlers via the
//! Unix-domain-socket / named-pipe IPC channel.  Each command builds a
//! `serde_json::Value` JSON-RPC request, forwards it to `cascaded`, and
//! deserializes the response.
//!
//! ## Commands
//! | Tauri command                        | Daemon method                        |
//! |--------------------------------------|--------------------------------------|
//! | `cascade_providers_list`             | `cascade_providers_list`             |
//! | `cascade_providers_test`             | `cascade_providers_test`             |
//! | `cascade_providers_remove`           | `cascade_providers_remove`           |
//! | `cascade_providers_add_apikey`       | `cascade_providers_add_apikey`       |
//! | `cascade_providers_oauth_start`      | `cascade_providers_oauth_start`      |
//! | `cascade_providers_oauth_status`     | `cascade_providers_oauth_status`     |
//! | `cascade_providers_add_generic`      | `cascade_providers_add_generic`      |
//! | `cascade_providers_usage_today`      | `cascade_providers_usage_today`      |
//! | `cascade_check_gfp_proxy`            | `cascade_check_gfp_proxy`            |
//!
//! ## Design note
//! These commands do NOT link cascade-daemon or cascade-keychain directly.
//! All sensitive operations (keychain, registry mutation) execute inside the
//! daemon process; the Tauri app is an untrusted UI renderer that forwards
//! requests over the authenticated IPC socket.
//!
//! ## SPORT
//! MASTER-COMMANDS.md — provider management Tauri commands (T-P3-E04-26/27)

use serde::{Deserialize, Serialize};
use tauri::State;
use tracing::debug;

use crate::error::CascadeError;
use crate::state::AppState;

// ── Shared wire types (mirrors daemon ipc_providers.rs) ───────────────────────

/// Per-provider list item returned by `cascade_providers_list`.
///
/// Must stay in sync with `cascade_daemon::ipc_providers::ProviderListItem`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderListItem {
    pub id: String,
    pub name: String,
    pub auth_method: String,
    pub status: String,
    pub error_msg: Option<String>,
    pub today_cost_usd: Option<f64>,
}

/// OAuth flow status (mirrors daemon `OAuthStatus`).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum OAuthStatus {
    Pending,
    Connected,
    Failed,
}

/// Per-provider today usage (mirrors daemon `ProviderUsageItem`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderUsageItem {
    pub date: String,
    pub provider_id: String,
    pub prompt_tokens: u64,
    pub completion_tokens: u64,
    pub total_tokens: u64,
    pub cost_usd: f64,
}

// ── IPC helpers ───────────────────────────────────────────────────────────────

/// Send a typed JSON-RPC request to the daemon and deserialise the response.
///
/// Uses the `cascade-cli` `IpcClient` (same pattern as the other commands in
/// `commands/mod.rs`).
async fn daemon_call<P, R>(method: &str, params: P) -> Result<R, CascadeError>
where
    P: Serialize,
    R: for<'de> Deserialize<'de>,
{
    use cascade_cli::ipc_client::IpcClient;

    let client = IpcClient::new().map_err(CascadeError::from)?;
    client
        .send::<P, R>(method, params)
        .await
        .map_err(CascadeError::from)
}

// ── T-P3-E04-26 commands ──────────────────────────────────────────────────────

/// List all registered providers with health status and today's cost.
///
/// JS: `invoke("cascade_providers_list")`
#[tauri::command]
pub async fn cascade_providers_list(
    _state: State<'_, AppState>,
) -> Result<Vec<ProviderListItem>, CascadeError> {
    debug!("cascade_providers_list invoked");
    daemon_call::<_, Vec<ProviderListItem>>("cascade_providers_list", serde_json::json!({})).await
}

/// Trigger an on-demand health check for one provider.
///
/// JS: `invoke("cascade_providers_test", { id })`
#[tauri::command]
pub async fn cascade_providers_test(
    id: String,
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!(provider_id = %id, "cascade_providers_test invoked");
    daemon_call::<_, ()>("cascade_providers_test", serde_json::json!({ "id": id })).await
}

/// Remove a provider (unregister + delete keychain key).
///
/// JS: `invoke("cascade_providers_remove", { id })`
#[tauri::command]
pub async fn cascade_providers_remove(
    id: String,
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!(provider_id = %id, "cascade_providers_remove invoked");
    daemon_call::<_, ()>("cascade_providers_remove", serde_json::json!({ "id": id })).await
}

/// Store an API key and register a provider.
///
/// JS: `invoke("cascade_providers_add_apikey", { id, key })`
///
/// The key is transmitted to the daemon over the authenticated local socket
/// and zeroed from daemon memory immediately after keychain storage.
#[tauri::command]
pub async fn cascade_providers_add_apikey(
    id: String,
    key: String,
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!(provider_id = %id, "cascade_providers_add_apikey invoked");
    daemon_call::<_, ()>(
        "cascade_providers_add_apikey",
        serde_json::json!({ "id": id, "key": key }),
    )
    .await
}

/// Start an OAuth PKCE flow; returns the auth URL to open in a browser.
///
/// JS: `invoke("cascade_providers_oauth_start", { id })`
#[tauri::command]
pub async fn cascade_providers_oauth_start(
    id: String,
    _state: State<'_, AppState>,
) -> Result<String, CascadeError> {
    debug!(provider_id = %id, "cascade_providers_oauth_start invoked");
    daemon_call::<_, String>(
        "cascade_providers_oauth_start",
        serde_json::json!({ "id": id }),
    )
    .await
}

/// Poll the status of a pending OAuth PKCE flow.
///
/// JS: `invoke("cascade_providers_oauth_status", { id })`
#[tauri::command]
pub async fn cascade_providers_oauth_status(
    id: String,
    _state: State<'_, AppState>,
) -> Result<OAuthStatus, CascadeError> {
    debug!(provider_id = %id, "cascade_providers_oauth_status invoked");
    daemon_call::<_, OAuthStatus>(
        "cascade_providers_oauth_status",
        serde_json::json!({ "id": id }),
    )
    .await
}

/// Register a generic OpenAI-compatible provider endpoint.
///
/// JS: `invoke("cascade_providers_add_generic", { base_url, api_key?, model_id, display_name? })`
#[tauri::command]
pub async fn cascade_providers_add_generic(
    base_url: String,
    api_key: Option<String>,
    model_id: String,
    display_name: Option<String>,
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!(%base_url, %model_id, "cascade_providers_add_generic invoked");
    daemon_call::<_, ()>(
        "cascade_providers_add_generic",
        serde_json::json!({
            "base_url": base_url,
            "api_key": api_key,
            "model_id": model_id,
            "display_name": display_name,
        }),
    )
    .await
}

// ── T-P3-E04-27 usage endpoint ────────────────────────────────────────────────

/// Return today's token usage per provider.
///
/// JS: `invoke("cascade_providers_usage_today")`
#[tauri::command]
pub async fn cascade_providers_usage_today(
    _state: State<'_, AppState>,
) -> Result<Vec<ProviderUsageItem>, CascadeError> {
    debug!("cascade_providers_usage_today invoked");
    daemon_call::<_, Vec<ProviderUsageItem>>("cascade_providers_usage_today", serde_json::json!({}))
        .await
}

// ── GFP proxy check ───────────────────────────────────────────────────────────

/// Check whether the Gemini Forwarding Proxy is running on localhost:3761.
///
/// Returns `true` iff `GET localhost:3761/health` responds with HTTP 200.
///
/// JS: `invoke("cascade_check_gfp_proxy")`
#[tauri::command]
pub async fn cascade_check_gfp_proxy(_state: State<'_, AppState>) -> Result<bool, CascadeError> {
    debug!("cascade_check_gfp_proxy invoked");
    // Ask the daemon; it handles the actual TCP probe with a 200 ms timeout.
    daemon_call::<_, bool>("cascade_check_gfp_proxy", serde_json::json!({})).await
}

// ── Wizard provider-step helpers ──────────────────────────────────────────────

/// Auto-connect the Gemini Forwarding Proxy detected at localhost:3761.
///
/// Called by WizardProviderStep.tsx when `cascade_check_gfp_proxy` returns true.
/// Delegates to `cascade_providers_add_apikey` with the GFP sentinel key
/// `"gfp_proxy_localhost_3761"` so the daemon registers the provider without
/// requiring the user to supply a real API key.
///
/// JS: `invoke("cascade_providers_add_gfp")`
#[tauri::command]
pub async fn cascade_providers_add_gfp(
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!("cascade_providers_add_gfp invoked");
    daemon_call::<_, ()>(
        "cascade_providers_add_apikey",
        serde_json::json!({ "id": "gemini_gfp", "key": "gfp_proxy_localhost_3761" }),
    )
    .await
}

/// Connect a provider by ID and optional API key.
///
/// Thin convenience wrapper over `cascade_providers_add_apikey`; used by wizard
/// steps that know the provider ID but may not have a key (OAuth providers pass
/// `api_key: None`, resulting in an empty string forwarded to the daemon which
/// then triggers the OAuth flow on its side).
///
/// JS: `invoke("cascade_providers_connect", { provider, apiKey })`
#[tauri::command]
pub async fn cascade_providers_connect(
    provider: String,
    api_key: Option<String>,
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!(provider_id = %provider, "cascade_providers_connect invoked");
    let key = api_key.unwrap_or_default();
    daemon_call::<_, ()>(
        "cascade_providers_add_apikey",
        serde_json::json!({ "id": provider, "key": key }),
    )
    .await
}

// ── Keychain management ───────────────────────────────────────────────────────

/// Delete the OS keychain entry for a given service label (via daemon).
///
/// Used by the settings / provider-management UI when the user disconnects a
/// provider and wants to remove its stored credential from the OS keychain.
/// The daemon owns all keychain access; this command is a thin IPC shim.
///
/// JS: `invoke("keychain_delete", { service })`
#[tauri::command]
pub async fn keychain_delete(
    service: String,
    _state: State<'_, AppState>,
) -> Result<(), CascadeError> {
    debug!(service = %service, "keychain_delete invoked");
    daemon_call::<_, ()>(
        "keychain_delete",
        serde_json::json!({ "service": service }),
    )
    .await
}
