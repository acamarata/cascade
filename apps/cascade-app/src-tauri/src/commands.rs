// Tauri IPC command surface — bridges the React frontend to cascade-core.
//
// Why: every public function here becomes a callable IPC endpoint.
// Naming: snake_case here maps to camelCase on the JS side via Tauri's
// automatic transformation. Document the JS name in each doc comment.
//
// Scaffold state (P3-E01 wave 1): CascadeCore API is defined in P3 Rust tickets
// (T-P3-E02-xx). Until those land, commands return placeholder responses so the
// Tauri app compiles and the frontend can be developed against realistic types.
// Replace each command body with a real cascade-core call as P3 Rust work lands.
//
// SPORT: MASTER-COMMANDS.md in .claude/docs — update when adding/removing.

use serde::{Deserialize, Serialize};
use tauri::State;

use crate::AppState;

// ---------------------------------------------------------------------------
// Daemon commands
// ---------------------------------------------------------------------------

/// Get daemon health status.
/// JS: `invoke("get_daemon_status")`
///
/// Returns a JSON object with fields: `running`, `pid`, `uptime_secs`.
#[tauri::command]
pub async fn get_daemon_status(
    _state: State<'_, AppState>,
) -> Result<DaemonStatus, String> {
    // TODO(P3-E02): delegate to cascade_core::CascadeCore::daemon_status()
    Ok(DaemonStatus {
        running: false,
        pid: None,
        uptime_secs: None,
        version: Some(env!("CARGO_PKG_VERSION").to_string()),
    })
}

/// Start the cascaded daemon if not already running.
/// JS: `invoke("start_daemon")`
#[tauri::command]
pub async fn start_daemon(_state: State<'_, AppState>) -> Result<(), String> {
    // TODO(P3-E02): delegate to cascade_core::CascadeCore::daemon_start()
    Err("daemon start not yet implemented — P3-E02".to_string())
}

/// Stop the cascaded daemon.
/// JS: `invoke("stop_daemon")`
#[tauri::command]
pub async fn stop_daemon(_state: State<'_, AppState>) -> Result<(), String> {
    // TODO(P3-E02): delegate to cascade_core::CascadeCore::daemon_stop()
    Err("daemon stop not yet implemented — P3-E02".to_string())
}

// ---------------------------------------------------------------------------
// CASCADE.md editor commands
// ---------------------------------------------------------------------------

/// Load a CASCADE.md file from disk by absolute path.
/// JS: `invoke("load_cascade_doc", { path })`
#[tauri::command]
pub async fn load_cascade_doc(
    path: String,
    _state: State<'_, AppState>,
) -> Result<CascadeDocument, String> {
    // TODO(P3-E03): delegate to cascade_core::CascadeCore::load_cascade_doc()
    Ok(CascadeDocument {
        path,
        content: String::new(),
        tier: "unknown".to_string(),
    })
}

/// Save a CASCADE.md document back to disk.
/// JS: `invoke("save_cascade_doc", { path, content })`
#[tauri::command]
pub async fn save_cascade_doc(
    _path: String,
    _content: String,
    _state: State<'_, AppState>,
) -> Result<(), String> {
    // TODO(P3-E03): delegate to cascade_core::CascadeCore::save_cascade_doc()
    Err("save_cascade_doc not yet implemented — P3-E03".to_string())
}

/// Validate CASCADE.md content without saving.
/// JS: `invoke("validate_cascade_doc", { content })`
#[tauri::command]
pub async fn validate_cascade_doc(
    _content: String,
    _state: State<'_, AppState>,
) -> Result<ValidationResult, String> {
    // TODO(P3-E03): delegate to cascade_core::CascadeCore::validate_cascade_doc()
    Ok(ValidationResult { valid: true, errors: vec![] })
}

// ---------------------------------------------------------------------------
// Inbox commands
// ---------------------------------------------------------------------------

/// List inbox messages for a registered project inbox path.
/// JS: `invoke("list_inbox", { inboxPath })`
#[tauri::command]
pub async fn list_inbox(
    _inbox_path: String,
    _state: State<'_, AppState>,
) -> Result<Vec<InboxMessage>, String> {
    // TODO(P3-E04): delegate to cascade_core::CascadeCore::list_inbox()
    Ok(vec![])
}

// ---------------------------------------------------------------------------
// RAG commands
// ---------------------------------------------------------------------------

/// Run a RAG query against the indexed knowledge base.
/// JS: `invoke("rag_query", { query, topK })`
#[tauri::command]
pub async fn rag_query(
    _query: String,
    _top_k: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<Vec<RagResult>, String> {
    // TODO(P4-E01): delegate to cascade_rag::RagEngine::query()
    Ok(vec![])
}

// ---------------------------------------------------------------------------
// Settings commands
// ---------------------------------------------------------------------------

/// Read app configuration from ~/.cascade/config.json.
/// JS: `invoke("get_config")`
#[tauri::command]
pub async fn get_config(_state: State<'_, AppState>) -> Result<AppConfig, String> {
    // TODO(P3-E05): delegate to cascade_core::CascadeCore::get_config()
    Ok(AppConfig {
        theme: "system".to_string(),
        locale: "en".to_string(),
        update_channel: "stable".to_string(),
        inbox_paths: vec![],
        rag_index_paths: vec![],
        daemon_autostart: false,
    })
}

/// Persist a config key-value pair.
/// JS: `invoke("set_config", { key, value })`
#[tauri::command]
pub async fn set_config(
    _key: String,
    _value: serde_json::Value,
    _state: State<'_, AppState>,
) -> Result<(), String> {
    // TODO(P3-E05): delegate to cascade_core::CascadeCore::set_config()
    Err("set_config not yet implemented — P3-E05".to_string())
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

/// Scaffold placeholder for CascadeDocument until cascade-types P3 API lands.
#[derive(Debug, Serialize, Deserialize)]
pub struct CascadeDocument {
    pub path: String,
    pub content: String,
    pub tier: String,
}

/// Scaffold placeholder for ValidationResult until cascade-types P3 API lands.
#[derive(Debug, Serialize, Deserialize)]
pub struct ValidationResult {
    pub valid: bool,
    pub errors: Vec<String>,
}
