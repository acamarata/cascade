// Core IPC bridge commands — status, resolve, search, inbox, memory, config.
//
// Why: these 9 commands form the required command set (T-P3-E01-06) wiring the
// React frontend to the cascaded daemon via JSON-RPC 2.0 over the AppState socket.
//
// IPC contract: cascade_types::ipc — FROZEN at schema v1.

use serde::{Deserialize, Serialize};
use tauri::State;

use cascade_cli::ipc_client::IpcClient;
use cascade_types::ipc::{
    ConfigGetParams, ConfigGetResult, ConfigSetParams, ConfigSetResult, InboxSummaryParams,
    InboxSummaryResult, MemoryReadParams, MemoryReadResult, MemoryWriteParams, MemoryWriteResult,
    ResolveParams, ResolveResult, SearchParams, SearchResult, StatusParams, StatusResult,
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
pub fn make_client() -> Result<IpcClient, CascadeError> {
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
        .send::<ResolveParams, ResolveResult>(
            "resolve",
            ResolveParams {
                tier,
                format,
                cwd: None,
            },
        )
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
/// Writes a PCI-format markdown message to `~/Sites/{to}/.claude/inbox/`.
/// The validation and file-writing logic is shared with the MCP
/// `cascade.inbox.send` tool handler via `cascade_core::inbox::send_message`.
///
/// `priority` defaults to `"medium"` when omitted. `msg_type` defaults to
/// `"info"` (the frontend does not currently send a type field).
#[tauri::command]
pub async fn cascade_inbox_send(
    to: String,
    subject: String,
    body: String,
    priority: Option<String>,
    _state: State<'_, AppState>,
) -> Result<InboxSendAck, CascadeError> {
    let priority = priority.unwrap_or_else(|| "medium".to_string());
    let msg_type = "info".to_string();

    let result = cascade_core::inbox::send_message(
        "cascade-app",
        &to,
        &subject,
        &body,
        &priority,
        &msg_type,
    )
    .await
    .map_err(|e| CascadeError::Custom(e.to_string()))?;

    Ok(InboxSendAck {
        id: result.slug,
        path: result.path.to_string_lossy().into_owned(),
    })
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
// Local response types
// ---------------------------------------------------------------------------

/// Acknowledgement returned by cascade_inbox_send once T-P4-E01 is implemented.
#[derive(Debug, Serialize, Deserialize)]
pub struct InboxSendAck {
    /// The file slug of the created inbox message.
    pub id: String,
    /// Absolute path to the written message file.
    pub path: String,
}

/// RAG query result (P4 scope stub).
#[derive(Debug, Serialize, Deserialize)]
pub struct RagResult {
    pub content: String,
    pub score: f32,
    pub source_path: String,
    pub chunk_index: usize,
}
