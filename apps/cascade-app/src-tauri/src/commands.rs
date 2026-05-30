// Tauri IPC command surface — bridges the React frontend to cascade-core.
//
// Every public function here becomes a callable IPC endpoint.
// Capability file (capabilities/default.json) enumerates exactly which
// commands the renderer is allowed to invoke — keep that list in sync when
// adding commands here.
//
// Naming convention: snake_case here maps to camelCase on the JS side via
// Tauri's automatic transformation. Document the JS name in each doc comment.
//
// SPORT: MASTER-COMMANDS.md in .claude/docs — update when adding/removing.

use cascade_core::CascadeCore;
use cascade_types::cascade::{CascadeDocument, ValidationResult};
use serde::{Deserialize, Serialize};
use tauri::State;

// ---------------------------------------------------------------------------
// Shared state
// ---------------------------------------------------------------------------

/// Application state injected by Tauri's state management.
/// Wraps cascade-core behind a Mutex for thread-safe access from async commands.
pub struct AppState {
    pub core: std::sync::Mutex<CascadeCore>,
}

// ---------------------------------------------------------------------------
// Daemon commands
// ---------------------------------------------------------------------------

/// Get daemon health status.
/// JS: `invoke("get_daemon_status")`
///
/// Returns a JSON object with fields: `running`, `pid`, `uptime_secs`.
/// Contacts the cascaded daemon via Unix socket / Named Pipe.
#[tauri::command]
pub async fn get_daemon_status(
    state: State<'_, AppState>,
) -> Result<DaemonStatus, String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.daemon_status().await.map_err(|e| e.to_string())
}

/// Start the cascaded daemon if not already running.
/// JS: `invoke("start_daemon")`
#[tauri::command]
pub async fn start_daemon(state: State<'_, AppState>) -> Result<(), String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.daemon_start().await.map_err(|e| e.to_string())
}

/// Stop the cascaded daemon.
/// JS: `invoke("stop_daemon")`
#[tauri::command]
pub async fn stop_daemon(state: State<'_, AppState>) -> Result<(), String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.daemon_stop().await.map_err(|e| e.to_string())
}

// ---------------------------------------------------------------------------
// CASCADE.md editor commands
// ---------------------------------------------------------------------------

/// Load a CASCADE.md file from disk by absolute path.
/// JS: `invoke("load_cascade_doc", { path })`
#[tauri::command]
pub async fn load_cascade_doc(
    path: String,
    state: State<'_, AppState>,
) -> Result<CascadeDocument, String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.load_cascade_doc(&path).await.map_err(|e| e.to_string())
}

/// Save a CASCADE.md document back to disk.
/// JS: `invoke("save_cascade_doc", { path, content })`
#[tauri::command]
pub async fn save_cascade_doc(
    path: String,
    content: String,
    state: State<'_, AppState>,
) -> Result<(), String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.save_cascade_doc(&path, &content)
        .await
        .map_err(|e| e.to_string())
}

/// Validate CASCADE.md content without saving.
/// JS: `invoke("validate_cascade_doc", { content })`
#[tauri::command]
pub async fn validate_cascade_doc(
    content: String,
    state: State<'_, AppState>,
) -> Result<ValidationResult, String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.validate_cascade_doc(&content)
        .map_err(|e| e.to_string())
}

// ---------------------------------------------------------------------------
// Inbox commands
// ---------------------------------------------------------------------------

/// List inbox messages for a registered project inbox path.
/// JS: `invoke("list_inbox", { inboxPath })`
#[tauri::command]
pub async fn list_inbox(
    inbox_path: String,
    state: State<'_, AppState>,
) -> Result<Vec<InboxMessage>, String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.list_inbox(&inbox_path)
        .await
        .map_err(|e| e.to_string())
}

// ---------------------------------------------------------------------------
// RAG commands
// ---------------------------------------------------------------------------

/// Run a RAG query against the indexed knowledge base.
/// JS: `invoke("rag_query", { query, topK })`
#[tauri::command]
pub async fn rag_query(
    query: String,
    top_k: Option<usize>,
    state: State<'_, AppState>,
) -> Result<Vec<RagResult>, String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.rag_query(&query, top_k.unwrap_or(5))
        .await
        .map_err(|e| e.to_string())
}

// ---------------------------------------------------------------------------
// Settings commands
// ---------------------------------------------------------------------------

/// Read app configuration from ~/.cascade/config.json.
/// JS: `invoke("get_config")`
#[tauri::command]
pub async fn get_config(state: State<'_, AppState>) -> Result<AppConfig, String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.get_config().map_err(|e| e.to_string())
}

/// Persist a config key-value pair.
/// JS: `invoke("set_config", { key, value })`
#[tauri::command]
pub async fn set_config(
    key: String,
    value: serde_json::Value,
    state: State<'_, AppState>,
) -> Result<(), String> {
    let core = state.core.lock().map_err(|e| e.to_string())?;
    core.set_config(&key, value).map_err(|e| e.to_string())
}

// ---------------------------------------------------------------------------
// Response types (serialized to JSON for the React frontend)
// ---------------------------------------------------------------------------

#[derive(Debug, Serialize, Deserialize)]
pub struct DaemonStatus {
    pub running: bool,
    pub pid: Option<u32>,
    pub uptime_secs: Option<u64>,
    pub version: Option<String>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct InboxMessage {
    pub id: String,
    pub subject: String,
    pub from: String,
    pub priority: String,
    pub message_type: String,
    pub created_at: String,
    pub body_preview: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct RagResult {
    pub content: String,
    pub score: f32,
    pub source_path: String,
    pub chunk_index: usize,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct AppConfig {
    pub theme: String,
    pub locale: String,
    pub update_channel: String,
    pub inbox_paths: Vec<String>,
    pub rag_index_paths: Vec<String>,
    pub daemon_autostart: bool,
}
