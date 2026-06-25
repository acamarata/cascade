// Daemon lifecycle and window management commands.
//
// Why: groups all commands that manage the cascaded process lifetime
// (start/stop/status) and the secondary Tauri window API (open/close/focus),
// plus the CASCADE.md editor stubs (T-P3-E03).

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, State};

use cascade_types::ipc::{DaemonStopParams, DaemonStopResult, StatusParams, StatusResult};

use crate::error::CascadeError;
use crate::state::AppState;

use super::core::make_client;

// ---------------------------------------------------------------------------
// Daemon lifecycle commands
// ---------------------------------------------------------------------------

/// Get daemon health status (maps to IPC `health` + `cascade.status`).
/// JS: `invoke("get_daemon_status")`
///
/// Returns running=true when the daemon responds to cascade.status.
/// Returns running=false (not an error) when the daemon is not up.
#[tauri::command]
pub async fn get_daemon_status(_state: State<'_, AppState>) -> Result<DaemonStatus, String> {
    match make_client() {
        Err(_) => Ok(DaemonStatus {
            running: false,
            pid: None,
            uptime_secs: None,
            version: Some(env!("CARGO_PKG_VERSION").to_string()),
        }),
        Ok(client) => {
            match client
                .send::<StatusParams, StatusResult>("cascade.status", StatusParams {})
                .await
            {
                Ok(s) => Ok(DaemonStatus {
                    running: true,
                    pid: Some(s.pid),
                    uptime_secs: Some(s.uptime_secs),
                    version: Some(s.version),
                }),
                Err(_) => Ok(DaemonStatus {
                    running: false,
                    pid: None,
                    uptime_secs: None,
                    version: Some(env!("CARGO_PKG_VERSION").to_string()),
                }),
            }
        }
    }
}

/// Start the cascaded daemon if not already running.
/// JS: `invoke("start_daemon")`
///
/// Shells out to `cascade daemon start` via the PATH so the Tauri app
/// does not hard-code the daemon binary location.
/// TODO(P3-E02): replace with cascade_core::CascadeCore::daemon_start()
#[tauri::command]
pub async fn start_daemon(_state: State<'_, AppState>) -> Result<(), String> {
    std::process::Command::new("cascade")
        .args(["daemon", "start"])
        .spawn()
        .map(|_| ())
        .map_err(|e| format!("failed to start daemon: {e}"))
}

/// Stop the cascaded daemon via JSON-RPC daemon_stop.
/// JS: `invoke("stop_daemon")`
#[tauri::command]
pub async fn stop_daemon(_state: State<'_, AppState>) -> Result<(), String> {
    let client = make_client().map_err(|e| e.to_string())?;
    client
        .send::<DaemonStopParams, DaemonStopResult>("daemon_stop", DaemonStopParams {})
        .await
        .map(|_| ())
        .map_err(|e| CascadeError::from(e).to_string())
}

// ---------------------------------------------------------------------------
// CASCADE.md editor commands (T-P3-E03 scope — typed stubs)
// ---------------------------------------------------------------------------

/// Load a CASCADE.md file from disk by absolute path.
/// JS: `invoke("load_cascade_doc", { path })`
/// TODO(T-P3-E03): delegate to cascade_core::CascadeCore::load_cascade_doc()
#[tauri::command]
pub async fn load_cascade_doc(
    path: String,
    _state: State<'_, AppState>,
) -> Result<CascadeDocument, String> {
    Ok(CascadeDocument {
        path,
        content: String::new(),
        tier: "unknown".to_string(),
    })
}

/// Save a CASCADE.md document back to disk.
/// JS: `invoke("save_cascade_doc", { path, content })`
/// TODO(T-P3-E03): delegate to cascade_core::CascadeCore::save_cascade_doc()
#[tauri::command]
pub async fn save_cascade_doc(
    _path: String,
    _content: String,
    _state: State<'_, AppState>,
) -> Result<(), String> {
    Err("save_cascade_doc not yet implemented — T-P3-E03".to_string())
}

/// Validate CASCADE.md content without saving.
/// JS: `invoke("validate_cascade_doc", { content })`
/// TODO(T-P3-E03): delegate to cascade_core::CascadeCore::validate_cascade_doc()
#[tauri::command]
pub async fn validate_cascade_doc(
    _content: String,
    _state: State<'_, AppState>,
) -> Result<ValidationResult, String> {
    Ok(ValidationResult {
        valid: true,
        errors: vec![],
    })
}

// ---------------------------------------------------------------------------
// Multi-window commands (T-P3-E01-17)
// ---------------------------------------------------------------------------

/// Open a secondary Tauri window, or focus it if already open.
///
/// # Purpose
/// Opens a new WebviewWindow with the given label, route URL, and title. If a
/// window with that label already exists, focuses it instead of creating a duplicate.
///
/// # Inputs
/// - `app`: Tauri AppHandle for window management.
/// - `label`: Unique window label (e.g. "settings-panel").
/// - `url`: App route (e.g. "/settings") — resolved relative to the Tauri app origin.
/// - `title`: Window title bar text.
///
/// # Outputs
/// `Ok(())` on success or focus. `CascadeError::Daemon` on build failure.
/// # Constraints
/// Synchronous; must not block the main thread. Size defaults: 900×700, min 600×400.
/// # SPORT
/// MASTER-COMMANDS.md — cascade_open_window
#[tauri::command]
pub fn cascade_open_window(
    app: AppHandle,
    label: String,
    url: String,
    title: String,
) -> Result<(), crate::error::CascadeError> {
    if let Some(w) = app.get_webview_window(&label) {
        w.set_focus()
            .map_err(|e| crate::error::CascadeError::Daemon(e.to_string()))?;
        return Ok(());
    }
    tauri::WebviewWindowBuilder::new(&app, &label, tauri::WebviewUrl::App(url.into()))
        .title(&title)
        .inner_size(900.0, 700.0)
        .min_inner_size(600.0, 400.0)
        .build()
        .map_err(|e| crate::error::CascadeError::Daemon(e.to_string()))?;
    Ok(())
}

/// Close a secondary window by label. Silently succeeds if the window is not open.
///
/// # Purpose
/// Closes the Tauri WebviewWindow identified by `label`. No-op if the window does
/// not exist, preventing JS errors when closing an already-closed window.
///
/// # Inputs
/// - `app`: Tauri AppHandle.
/// - `label`: Window label to close.
///
/// # Outputs
/// Always `Ok(())`.
/// # SPORT
/// MASTER-COMMANDS.md — cascade_close_window
#[tauri::command]
pub fn cascade_close_window(
    app: AppHandle,
    label: String,
) -> Result<(), crate::error::CascadeError> {
    if let Some(w) = app.get_webview_window(&label) {
        let _ = w.close();
    }
    Ok(())
}

/// Focus an existing secondary window by label. Silently succeeds if not open.
///
/// # Purpose
/// Brings the window identified by `label` to the foreground. No-op if not open.
///
/// # Inputs
/// - `app`: Tauri AppHandle.
/// - `label`: Window label to focus.
///
/// # Outputs
/// Always `Ok(())`.
/// # SPORT
/// MASTER-COMMANDS.md — cascade_focus_window
#[tauri::command]
pub fn cascade_focus_window(
    app: AppHandle,
    label: String,
) -> Result<(), crate::error::CascadeError> {
    if let Some(w) = app.get_webview_window(&label) {
        let _ = w.set_focus();
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// Local response types
// ---------------------------------------------------------------------------

/// Daemon process status for the get_daemon_status compatibility command.
#[derive(Debug, Serialize, Deserialize)]
pub struct DaemonStatus {
    pub running: bool,
    pub pid: Option<u32>,
    pub uptime_secs: Option<u64>,
    pub version: Option<String>,
}

/// Scaffold placeholder for CascadeDocument until cascade-core P3 API lands.
#[derive(Debug, Serialize, Deserialize)]
pub struct CascadeDocument {
    pub path: String,
    pub content: String,
    pub tier: String,
}

/// Scaffold placeholder for ValidationResult until cascade-core P3 API lands.
#[derive(Debug, Serialize, Deserialize)]
pub struct ValidationResult {
    pub valid: bool,
    pub errors: Vec<String>,
}
