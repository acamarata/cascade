// Tauri IPC command surface — bridges the React frontend to the cascaded daemon.
//
// Why: every public function here becomes a callable IPC endpoint via
// `invoke("<command_name>")` on the JS side. Naming: snake_case here maps to
// camelCase on the JS side via Tauri's automatic transformation.
//
// Architecture: each command builds an IpcClient using the socket path from
// AppState, calls the daemon via JSON-RPC 2.0, and maps IpcClientError to
// CascadeError so the frontend receives a structured JSON error payload.
//
// IPC contract: cascade_types::ipc — FROZEN at schema v1. Add methods by
// appending only; never rename or remove a method in this file.
//
// Remaining TODOs (scope boundaries):
//   cascade_inbox_send — T-P4-E01: daemon inbox_send method not yet in the
//     JSON-RPC contract (ipc.rs v1). Stubbed with NotImplemented.
//   load_cascade_doc, save_cascade_doc, validate_cascade_doc — T-P3-E03
//   list_inbox — replaced by cascade_inbox_list / cascade_inbox_send split (T-P3-E01-07)
//   rag_query — T-P4-E01: cascade_rag crate not yet available.
//   get_config / set_config renamed cascade_config_get / cascade_config_set in this ticket.
//
// SPORT: MASTER-COMMANDS.md in .claude/docs — update when adding/removing commands.

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, State};
use std::path::PathBuf;

use cascade_cli::ipc_client::IpcClient;
use cascade_types::ipc::{
    ConfigGetParams, ConfigGetResult, ConfigSetParams, ConfigSetResult, DaemonStopParams,
    DaemonStopResult, InboxSummaryParams, InboxSummaryResult, MemoryReadParams, MemoryReadResult,
    MemoryWriteParams, MemoryWriteResult, ResolveParams, ResolveResult,
    SearchParams, SearchResult, StatusParams, StatusResult,
};

use crate::error::CascadeError;
use crate::state::AppState;

// ---------------------------------------------------------------------------
// Helper — build IpcClient from AppState
// ---------------------------------------------------------------------------

/// Build an [`IpcClient`] for the current invocation.
///
/// Why: the CLI's IpcClient::new() resolves paths from the environment which
/// is correct for CLI usage, but Tauri commands need the socket_path already
/// resolved from AppState (populated at launch from CASCADE_SOCKET or the
/// canonical path). We call IpcClient::new() which reads ipc_token from
/// ~/.cascade/ipc_token — same path the daemon writes on startup.
///
/// Returns CascadeError::DaemonNotRunning if the token file is absent.
fn make_client() -> Result<IpcClient, CascadeError> {
    IpcClient::new().map_err(CascadeError::from)
}

// ---------------------------------------------------------------------------
// T-P3-E01-06: Required 9-command set
// ---------------------------------------------------------------------------

/// Get daemon runtime status.
/// JS: `invoke("cascade_status")`
///
/// Returns daemon PID, uptime, queue depth, RAG freshness, and version.
/// Returns CascadeError::DaemonNotRunning if the daemon is not up.
#[tauri::command]
pub async fn cascade_status(_state: State<'_, AppState>) -> Result<StatusResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<StatusParams, StatusResult>("cascade.status", StatusParams {})
        .await
        .map_err(CascadeError::from)
}

/// Resolve the instruction cascade for a given tier.
/// JS: `invoke("cascade_resolve", { tier?, format? })`
///
/// `tier`: one of "gci", "pci", "apc", "ppc", "prc", "pac", or omit for full cascade.
/// `format`: "markdown" (default) or "json".
#[tauri::command]
pub async fn cascade_resolve(
    tier: Option<String>,
    format: Option<String>,
    _state: State<'_, AppState>,
) -> Result<ResolveResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<ResolveParams, ResolveResult>("resolve", ResolveParams { tier, format })
        .await
        .map_err(CascadeError::from)
}

/// Search the indexed knowledge base with a natural language query.
/// JS: `invoke("cascade_search", { query, limit? })`
///
/// `limit`: max hits to return (daemon defaults to 10 when absent).
/// Returns ranked SearchHit list (best match first).
#[tauri::command]
pub async fn cascade_search(
    query: String,
    limit: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<SearchResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<SearchParams, SearchResult>("search", SearchParams { query, limit })
        .await
        .map_err(CascadeError::from)
}

/// List inbox messages for the active cascade context.
/// JS: `invoke("cascade_inbox_list", { limit? })`
///
/// `limit`: max items to return (all items when absent).
/// Returns InboxSummaryResult with items in reverse-chronological order.
#[tauri::command]
pub async fn cascade_inbox_list(
    limit: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<InboxSummaryResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<InboxSummaryParams, InboxSummaryResult>(
            "inbox_summary",
            InboxSummaryParams { limit },
        )
        .await
        .map_err(CascadeError::from)
}

/// Send (write) a message into a project inbox.
/// JS: `invoke("cascade_inbox_send", { to, subject, body, priority? })`
///
/// NOTE: the `inbox_send` JSON-RPC method is not yet in the daemon contract
/// (ipc.rs schema v1 defines inbox_summary only). This stub returns
/// NotImplemented until T-P4-E01 adds the server-side handler and the
/// corresponding ipc.rs types. The parameter struct is defined here so the
/// TypeScript bindings (T-P3-E01-07) can be written against a stable shape.
#[tauri::command]
pub async fn cascade_inbox_send(
    to: String,
    subject: String,
    body: String,
    priority: Option<String>,
    _state: State<'_, AppState>,
) -> Result<InboxSendAck, CascadeError> {
    // Suppress unused-variable warnings while this stub is pending.
    let _ = (to, subject, body, priority);
    Err(CascadeError::NotImplemented(
        "cascade_inbox_send — awaits T-P4-E01 daemon inbox_send method".to_string(),
    ))
}

/// Read a memory file from a project's `.claude/memory/` directory.
/// JS: `invoke("cascade_memory_read", { project, file })`
///
/// `project`: absolute path to the project root or a slug registered with the daemon.
/// `file`: filename relative to `.claude/memory/`, e.g. `"decisions.md"`.
#[tauri::command]
pub async fn cascade_memory_read(
    project: String,
    file: String,
    _state: State<'_, AppState>,
) -> Result<MemoryReadResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<MemoryReadParams, MemoryReadResult>(
            "memory_read",
            MemoryReadParams { project, file },
        )
        .await
        .map_err(CascadeError::from)
}

/// Write (overwrite) a memory file in a project's `.claude/memory/` directory.
/// JS: `invoke("cascade_memory_write", { project, file, content })`
///
/// `content`: full UTF-8 text to write (overwrites existing content).
#[tauri::command]
pub async fn cascade_memory_write(
    project: String,
    file: String,
    content: String,
    _state: State<'_, AppState>,
) -> Result<MemoryWriteResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<MemoryWriteParams, MemoryWriteResult>(
            "memory_write",
            MemoryWriteParams {
                project,
                file,
                content,
            },
        )
        .await
        .map_err(CascadeError::from)
}

/// Read a configuration key from the daemon.
/// JS: `invoke("cascade_config_get", { key })`
///
/// `key`: dot-separated config key, e.g. `"daemon.socket_path"`.
#[tauri::command]
pub async fn cascade_config_get(
    key: String,
    _state: State<'_, AppState>,
) -> Result<ConfigGetResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<ConfigGetParams, ConfigGetResult>("config_get", ConfigGetParams { key })
        .await
        .map_err(CascadeError::from)
}

/// Update a configuration key in the daemon.
/// JS: `invoke("cascade_config_set", { key, value })`
///
/// `value`: new JSON value; type depends on the key.
#[tauri::command]
pub async fn cascade_config_set(
    key: String,
    value: serde_json::Value,
    _state: State<'_, AppState>,
) -> Result<ConfigSetResult, CascadeError> {
    let client = make_client()?;
    client
        .send::<ConfigSetParams, ConfigSetResult>("config_set", ConfigSetParams { key, value })
        .await
        .map_err(CascadeError::from)
}

// ---------------------------------------------------------------------------
// Daemon lifecycle commands (existing — updated to use IPC where available)
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
// RAG commands (T-P4-E01 scope — typed stub)
// ---------------------------------------------------------------------------

/// Run a RAG query against the indexed knowledge base (P4 scope).
/// JS: `invoke("rag_query", { query, topK })`
/// TODO(T-P4-E01): delegate to cascade_rag::RagEngine::query()
#[tauri::command]
pub async fn rag_query(
    _query: String,
    _top_k: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<Vec<RagResult>, String> {
    Ok(vec![])
}

// ---------------------------------------------------------------------------
// Re-exported types from cascade_types (forwarded to JS via Tauri serialization)
// ---------------------------------------------------------------------------

// The following types are re-exported from cascade_types::ipc so the TypeScript
// bindings (T-P3-E01-07) can be generated without duplicating the struct definitions.
// They are referenced in the command return types above.
pub use cascade_types::ipc::{
    ConfigGetResult as CascadeConfigGetResult, ConfigSetResult as CascadeConfigSetResult,
    InboxSummaryResult as CascadeInboxSummaryResult, MemoryReadResult as CascadeMemoryReadResult,
    MemoryWriteResult as CascadeMemoryWriteResult, ResolveResult as CascadeResolveResult,
    SearchResult as CascadeSearchResult, StatusResult as CascadeStatusResult,
};

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
    tauri::WebviewWindowBuilder::new(
        &app,
        &label,
        tauri::WebviewUrl::App(url.into()),
    )
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
// Local response types (not in cascade_types)
// ---------------------------------------------------------------------------

/// Acknowledgement returned by cascade_inbox_send once T-P4-E01 is implemented.
#[derive(Debug, Serialize, Deserialize)]
pub struct InboxSendAck {
    /// The file slug of the created inbox message.
    pub id: String,
    /// Absolute path to the written message file.
    pub path: String,
}

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

/// RAG query result (P4 scope stub).
#[derive(Debug, Serialize, Deserialize)]
pub struct RagResult {
    pub content: String,
    pub score: f32,
    pub source_path: String,
    pub chunk_index: usize,
}
