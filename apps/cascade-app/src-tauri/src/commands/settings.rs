//! # commands::settings
//!
//! Tauri IPC commands for application settings — bridges the React frontend
//! to the file-based `~/.cascade/settings.json` store.
//!
//! ## Purpose
//!
//! Exposes two commands:
//! - `get_settings` — read the current settings, applying defaults for any
//!   missing fields.
//! - `update_settings` — merge a partial JSON object into the current
//!   settings and persist atomically.
//!
//! ## IPC naming
//!
//! Tauri maps snake_case command names to camelCase on the JS side
//! automatically (`getSettings`, `updateSettings`).
//! All struct fields use `#[serde(rename_all = "camelCase")]` (inherited
//! from `CascadeSettings`).
//!
//! ## Constraints
//!
//! - Settings path is resolved from `$HOME/.cascade/settings.json`; never
//!   hardcoded.
//! - `update_settings` performs a **top-level** merge: only keys present in
//!   the `partial` argument are updated; all other keys are preserved.
//! - Both commands return `Result<_, String>` (required by Tauri 2 IPC
//!   serialisation — the error is a human-readable message).
//! - Unknown top-level keys are preserved in `CascadeSettings::extra`
//!   for forward-compatibility.
//!
//! ## SPORT
//!
//! MASTER-COMMANDS.md — settings IPC commands (T-P3-E07-14)

use cascade_core::settings::store::{load, save};
use cascade_core::settings::types::{
    ContextSettings, GeminiPoolSettings, HarnessBridgesSettings, HookDefinition, LibrarySettings,
    McpServerEntry, PluginsSettings, ProjectMapSettings, ProvidersSettings, ScheduledTaskEntry,
    TelemetrySettings, VaultDisplaySettings, WidgetsSettings,
};
use cascade_core::settings::CascadeSettings;
use cascade_types::paths::global_cascade_dir;
use tauri::State;

use crate::state::AppState;

/// Resolve the canonical settings.json path.
fn settings_path() -> std::path::PathBuf {
    global_cascade_dir().join("settings.json")
}

/// Read the current application settings.
///
/// If `~/.cascade/settings.json` does not exist, returns the default
/// settings (and does **not** write the file — the file is written lazily
/// on the first `update_settings` call).
///
/// # Errors
///
/// Returns a string error if the file exists but cannot be parsed.
#[tauri::command]
pub async fn get_settings(_state: State<'_, AppState>) -> Result<CascadeSettings, String> {
    let path = settings_path();
    load(&path).map_err(|e| e.to_string())
}

/// Merge a partial JSON object into the current settings and persist.
///
/// Only top-level keys present in `partial` are updated; all other keys
/// retain their previous values. The write is atomic: a `.tmp` file is
/// written first, then renamed to `settings.json`.
///
/// Recognised top-level keys (camelCase as transmitted by the JS client):
/// - `schemaVersion`
/// - `library`
/// - `context`
/// - `projectMap`
/// - `providers`
/// - `geminiPool`
/// - `harnessBridges`
/// - `hooks`
/// - `scheduledTasks`
/// - `plugins`
/// - `widgets`
/// - `mcpServers`
/// - `vaultDisplay`
/// - `telemetry`
///
/// Unknown keys are silently ignored (forward-compatibility with future
/// schema versions whose keys we don't yet parse).
///
/// # Errors
///
/// Returns a string error if the settings file cannot be read or written.
#[tauri::command]
pub async fn update_settings(
    _state: State<'_, AppState>,
    partial: serde_json::Value,
) -> Result<(), String> {
    let path = settings_path();

    // Read current (or default) settings.
    let mut current = load(&path).map_err(|e| e.to_string())?;

    // Top-level merge: only update keys present in `partial`.
    if let serde_json::Value::Object(map) = partial {
        for (k, v) in map {
            match k.as_str() {
                "schemaVersion" => {
                    if let serde_json::Value::String(s) = v {
                        current.schema_version = s;
                    }
                }
                "library" => {
                    current.library = serde_json::from_value::<LibrarySettings>(v)
                        .map_err(|e| format!("invalid library: {e}"))?;
                }
                "context" => {
                    current.context = serde_json::from_value::<ContextSettings>(v)
                        .map_err(|e| format!("invalid context: {e}"))?;
                }
                "projectMap" => {
                    current.project_map = serde_json::from_value::<ProjectMapSettings>(v)
                        .map_err(|e| format!("invalid projectMap: {e}"))?;
                }
                "providers" => {
                    current.providers = serde_json::from_value::<ProvidersSettings>(v)
                        .map_err(|e| format!("invalid providers: {e}"))?;
                }
                "geminiPool" => {
                    current.gemini_pool = serde_json::from_value::<GeminiPoolSettings>(v)
                        .map_err(|e| format!("invalid geminiPool: {e}"))?;
                }
                "harnessBridges" => {
                    current.harness_bridges =
                        serde_json::from_value::<HarnessBridgesSettings>(v)
                            .map_err(|e| format!("invalid harnessBridges: {e}"))?;
                }
                "hooks" => {
                    current.hooks = serde_json::from_value::<Vec<HookDefinition>>(v)
                        .map_err(|e| format!("invalid hooks: {e}"))?;
                }
                "scheduledTasks" => {
                    current.scheduled_tasks = serde_json::from_value::<Vec<ScheduledTaskEntry>>(v)
                        .map_err(|e| format!("invalid scheduledTasks: {e}"))?;
                }
                "plugins" => {
                    current.plugins = serde_json::from_value::<PluginsSettings>(v)
                        .map_err(|e| format!("invalid plugins: {e}"))?;
                }
                "widgets" => {
                    current.widgets = serde_json::from_value::<WidgetsSettings>(v)
                        .map_err(|e| format!("invalid widgets: {e}"))?;
                }
                "mcpServers" => {
                    current.mcp_servers = serde_json::from_value::<Vec<McpServerEntry>>(v)
                        .map_err(|e| format!("invalid mcpServers: {e}"))?;
                }
                "vaultDisplay" => {
                    current.vault_display = serde_json::from_value::<VaultDisplaySettings>(v)
                        .map_err(|e| format!("invalid vaultDisplay: {e}"))?;
                }
                "telemetry" => {
                    current.telemetry = serde_json::from_value::<TelemetrySettings>(v)
                        .map_err(|e| format!("invalid telemetry: {e}"))?;
                }
                _ => {
                    // Unknown top-level key — silently ignored to maintain
                    // forward-compatibility with future settings extensions.
                }
            }
        }
    }

    // Persist atomically.
    save(&path, &current).map_err(|e| e.to_string())
}
