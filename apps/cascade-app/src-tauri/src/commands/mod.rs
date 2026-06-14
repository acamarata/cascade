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

// Sub-modules added by W-02 (T-P3-E03-09 scaffold):
pub mod scanner;
pub mod symlinks;
// Sub-module added by W-03 (T-P3-E03-15 scaffold):
pub mod archive;
// Sub-module added by W-04 (T-P3-E03-23 scaffold):
pub mod merge;
// Sub-module added by W-05 (T-P3-E03-39/39b/40/41/42 scaffold):
pub mod provision;
// T-P3-E04-26/27: Provider IPC commands + usage today
pub mod providers;
// T-P3-E04-30: Usage analytics IPC commands
pub mod usage;
// T-P3-E05-15/17: Template engine IPC commands (list/apply/diff/upgrade)
pub mod templates;
// T-P3-E06-01: Vault file-system IPC commands (list/read/write/watch)
pub mod vault;
// T-P3-E07-00: Application settings IPC commands (get/update)
pub mod settings;
// T-P3-E07-02/03: Library item IPC commands (list/get/upsert/delete)
pub mod library;
// T-P3-E07-08: Context session IPC commands (list/get/upsert/delete)
pub mod context;
// T-P3-E07-12: Project maps data providers (project graph, cascade tier tree, PEWS DAG)
pub mod maps;
// E-P8-02: Kanban task IPC commands (create/get/list/update/delete/move)
pub mod tasks;
// E-P8-05: PBD PEWS tree visualization + mutation IPC commands
pub mod pbd;

use serde::{Deserialize, Serialize};
use std::env;
use std::fs;
use std::path::PathBuf;
use tauri::{AppHandle, Manager, State};

use cascade_cli::ipc_client::IpcClient;
use cascade_types::ipc::{
    ConfigGetParams, ConfigGetResult, ConfigSetParams, ConfigSetResult, DaemonStopParams,
    DaemonStopResult, InboxSummaryParams, InboxSummaryResult, MemoryReadParams, MemoryReadResult,
    MemoryWriteParams, MemoryWriteResult, ResolveParams, ResolveResult, SearchParams, SearchResult,
    StatusParams, StatusResult,
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
// RAG commands (T-P4-E01-29: wired to daemon rag.search endpoint)
// ---------------------------------------------------------------------------

/// Params forwarded to the daemon's `rag.search` JSON-RPC method.
///
/// Matches `cascade_daemon::search_handler::RagSearchParams` without requiring
/// cascade-daemon as a direct dependency.
#[derive(Debug, Serialize)]
struct RagSearchIpcParams<'a> {
    query: &'a str,
    project_root: String,
    k: u32,
}

/// Daemon `rag.search` response shape (mirrors `RagSearchResponse`).
///
/// We only deserialize the fields we need for mapping to `RagResult`.
#[derive(Debug, Deserialize)]
struct RagSearchIpcResponse {
    citations: Vec<RagCitationIpc>,
}

/// Minimal citation shape from `cascade_rag::citation::RagCitation` (camelCase).
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RagCitationIpc {
    source_path: String,
    snippet: String,
    rrf_score: f64,
    line_start: usize,
}

/// Run a RAG search query against the daemon's indexed knowledge base.
///
/// # Purpose
/// Delegates to the daemon's `rag.search` IPC method (T-P4-E01-29).
/// Returns a mapped `Vec<RagResult>` ordered by descending relevance score.
///
/// # JS
/// `invoke("rag_query", { query, topK })`
///
/// # Inputs
/// - `query`: User search string.
/// - `top_k`: Max results (defaults to 10).
///
/// # Outputs
/// Ordered list of RAG results. Returns empty vec if daemon returns no hits.
#[tauri::command]
pub async fn rag_query(
    query: String,
    top_k: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<Vec<RagResult>, String> {
    let client = make_client().map_err(|e| e.to_string())?;

    // Resolve the project root from the environment (same as other commands).
    let project_root = std::env::var("CASCADE_PROJECT_ROOT")
        .or_else(|_| std::env::current_dir().map(|p| p.display().to_string()))
        .unwrap_or_default();

    let params = RagSearchIpcParams {
        query: &query,
        project_root,
        k: top_k.unwrap_or(10) as u32,
    };

    let resp: RagSearchIpcResponse = client
        .send("rag.search", &params)
        .await
        .map_err(|e| e.to_string())?;

    let results = resp
        .citations
        .into_iter()
        .map(|c| RagResult {
            content: c.snippet,
            score: c.rrf_score as f32,
            source_path: c.source_path,
            chunk_index: c.line_start,
        })
        .collect();

    Ok(results)
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

// ---------------------------------------------------------------------------
// T-P3-E03-07: Wizard first-run detection and T-P3-E03-03: Wizard checkpoint persistence
// ---------------------------------------------------------------------------

/// Get home directory for current platform.
fn get_home_dir() -> Option<PathBuf> {
    #[cfg(target_os = "windows")]
    {
        env::var("USERPROFILE").ok().map(PathBuf::from)
    }
    #[cfg(not(target_os = "windows"))]
    {
        env::var("HOME").ok().map(PathBuf::from)
    }
}

/// Wizard status enum for first-run detection.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "status", content = "data")]
pub enum WizardStatus {
    /// Wizard has never been run
    NeverRun,
    /// Wizard is in progress
    InProgress(WizardCheckpoint),
    /// Wizard is complete
    Complete,
}

/// Wizard checkpoint type.
///
/// Serialized as camelCase JSON so the TypeScript frontend can consume it
/// without a mapping layer (e.g. `completedSteps`, `providerConnected`).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(crate = "serde", rename_all = "camelCase")]
pub struct WizardCheckpoint {
    pub step: u32,
    pub completed_steps: Vec<u32>,
    pub provider_connected: bool,
    pub scan_result: Option<ScanResult>,
    pub merge_result: Option<MergeResult>,
    pub archive_manifest_path: Option<String>,
    pub daemon_installed: bool,
    pub started_at: String,
    pub updated_at: String,
    /// Forward-compatible passthrough: preserves wizard-state fields added on the
    /// TS side (mergeResults, toolModes, archivedTools, detectedToolIds, ...) through
    /// the save/load round-trip without requiring a Rust struct change per field.
    #[serde(flatten)]
    pub extra: serde_json::Map<String, serde_json::Value>,
}

/// Result of the legacy config scan (wizard phase 3).
/// Fields serialized as camelCase for TypeScript consumers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(crate = "serde", rename_all = "camelCase")]
pub struct ScanResult {
    pub legacy_files_found: u32,
    pub total_size: u64,
    pub project_roots: Vec<String>,
}

/// Result of the config merge operation (wizard phase 4).
/// Fields serialized as camelCase for TypeScript consumers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(crate = "serde", rename_all = "camelCase")]
pub struct MergeResult {
    pub merged_file_count: u32,
    pub conflicts_resolved: u32,
    pub timestamp: String,
}

/// Get the checkpoint file path: ~/.cascade/wizard-state.json
fn checkpoint_path() -> Result<PathBuf, CascadeError> {
    get_home_dir()
        .ok_or_else(|| CascadeError::Custom("could not resolve home directory".into()))
        .map(|h| h.join(".cascade").join("wizard-state.json"))
}

/// Get the temporary checkpoint file path: ~/.cascade/wizard-state.json.tmp
fn checkpoint_tmp_path() -> Result<PathBuf, CascadeError> {
    checkpoint_path().map(|p| {
        let mut tmp = p.as_os_str().to_owned();
        tmp.push(".tmp");
        PathBuf::from(tmp)
    })
}

/// Check wizard status to determine if it should launch, resume, or be skipped.
#[tauri::command]
pub fn check_wizard_status() -> Result<WizardStatus, CascadeError> {
    let home = get_home_dir()
        .ok_or_else(|| CascadeError::Custom("Could not determine home directory".to_string()))?;

    let cascade_dir = home.join(".cascade");
    let wizard_complete_marker = cascade_dir.join(".wizard-complete");
    let wizard_state_file = cascade_dir.join("wizard-state.json");

    // Check if ~/.cascade/ exists
    if !cascade_dir.exists() {
        return Ok(WizardStatus::NeverRun);
    }

    // Check if wizard-complete marker exists
    if wizard_complete_marker.exists() {
        return Ok(WizardStatus::Complete);
    }

    // Check if wizard-state.json exists
    if wizard_state_file.exists() {
        match fs::read_to_string(&wizard_state_file) {
            Ok(content) => match serde_json::from_str::<WizardCheckpoint>(&content) {
                Ok(checkpoint) => {
                    if checkpoint.completed_steps.len() < 10 {
                        return Ok(WizardStatus::InProgress(checkpoint));
                    }
                    return Ok(WizardStatus::Complete);
                }
                Err(_) => {
                    return Ok(WizardStatus::NeverRun);
                }
            },
            Err(_) => {
                return Ok(WizardStatus::NeverRun);
            }
        }
    }

    Ok(WizardStatus::NeverRun)
}

/// Save checkpoint atomically to ~/.cascade/wizard-state.json.
#[tauri::command]
pub async fn wizard_save_checkpoint(checkpoint: WizardCheckpoint) -> Result<(), CascadeError> {
    let target_path = checkpoint_path()?;
    let tmp_path = checkpoint_tmp_path()?;

    if let Some(parent) = target_path.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| CascadeError::Custom(format!("failed to create directory: {}", e)))?;
    }

    let json = serde_json::to_string_pretty(&checkpoint)
        .map_err(|e| CascadeError::Custom(format!("failed to serialize checkpoint: {}", e)))?;

    fs::write(&tmp_path, json)
        .map_err(|e| CascadeError::Custom(format!("failed to write checkpoint: {}", e)))?;

    fs::rename(&tmp_path, &target_path)
        .map_err(|e| CascadeError::Custom(format!("failed to finalize checkpoint: {}", e)))?;

    Ok(())
}

/// Load checkpoint from ~/.cascade/wizard-state.json.
#[tauri::command]
pub async fn wizard_load_checkpoint() -> Result<Option<WizardCheckpoint>, CascadeError> {
    let path = checkpoint_path()?;

    if !path.exists() {
        return Ok(None);
    }

    let json = fs::read_to_string(&path)
        .map_err(|e| CascadeError::Custom(format!("failed to read checkpoint: {}", e)))?;

    let checkpoint = serde_json::from_str(&json)
        .map_err(|e| CascadeError::Custom(format!("failed to parse checkpoint: {}", e)))?;

    Ok(Some(checkpoint))
}

/// Clear (delete) the checkpoint file.
#[tauri::command]
pub async fn wizard_clear_checkpoint() -> Result<(), CascadeError> {
    let path = checkpoint_path()?;

    if !path.exists() {
        return Ok(());
    }

    fs::remove_file(&path)
        .map_err(|e| CascadeError::Custom(format!("failed to delete checkpoint: {}", e)))?;

    Ok(())
}

/// Audit check result from wizard_check_audit_format.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AuditCheckResult {
    pub audit_log_exists: bool,
    pub format_mismatch: bool,
    pub is_tampered: bool,
    pub violation_count: u32,
}

/// Check the audit log format and classify violations as format-mismatch or tampering.
/// JS: `invoke("wizard_check_audit_format")`
///
/// Returns AuditCheckResult with:
/// - `audit_log_exists`: true if ~/.cascade/audit.log exists
/// - `format_mismatch`: true if ALL records fail self-hash (pre-gate format)
/// - `is_tampered`: true if only some records fail (genuine tampering)
/// - `violation_count`: number of violations detected by verify_chain
#[tauri::command]
pub async fn wizard_check_audit_format() -> Result<AuditCheckResult, CascadeError> {
    use cascade_audit::AuditLog;
    use cascade_types::paths::home_dir;

    let audit_log_path = home_dir().join(".cascade").join("audit.log");

    // Check if log exists
    if !audit_log_path.exists() {
        return Ok(AuditCheckResult {
            audit_log_exists: false,
            format_mismatch: false,
            is_tampered: false,
            violation_count: 0,
        });
    }

    // Log exists — verify chain
    let log = AuditLog::open(&audit_log_path)
        .map_err(|e| CascadeError::Custom(format!("failed to open audit log: {}", e)))?;

    let violations = log
        .verify_chain()
        .map_err(|e| CascadeError::Custom(format!("failed to verify audit chain: {}", e)))?;

    if violations.is_empty() {
        // No violations — log is valid
        return Ok(AuditCheckResult {
            audit_log_exists: true,
            format_mismatch: false,
            is_tampered: false,
            violation_count: 0,
        });
    }

    // Violations detected — classify them
    let hash_mismatch_count = violations
        .iter()
        .filter(|v| v.reason.contains("hash does not match recomputed"))
        .count();

    // Format-mismatch only if EVERY violation is a hash mismatch (pre-gate format)
    let format_mismatch = hash_mismatch_count == violations.len();

    Ok(AuditCheckResult {
        audit_log_exists: true,
        format_mismatch,
        is_tampered: !format_mismatch,
        violation_count: violations.len() as u32,
    })
}

/// Rotate the audit log: archive old log to audit.log.pre-p3.<timestamp>
/// and remove the .count sidecar. Next daemon append starts a fresh chain from GENESIS.
/// JS: `invoke("wizard_rotate_audit_log")`
///
/// Returns the absolute path to the archived log file.
#[tauri::command]
pub async fn wizard_rotate_audit_log() -> Result<String, CascadeError> {
    use cascade_types::paths::home_dir;
    use std::time::{SystemTime, UNIX_EPOCH};

    let audit_log_path = home_dir().join(".cascade").join("audit.log");
    let count_sidecar_path = home_dir().join(".cascade").join("audit.log.count");

    // Check if log exists
    if !audit_log_path.exists() {
        return Err(CascadeError::Custom(
            "audit.log does not exist; nothing to rotate".into(),
        ));
    }

    // Generate archive name: audit.log.pre-p3.<unix-timestamp>
    let unix_ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|e| CascadeError::Custom(format!("failed to get current time: {}", e)))?
        .as_secs();

    let archive_path = home_dir()
        .join(".cascade")
        .join(format!("audit.log.pre-p3.{}", unix_ts));

    // Rename (move) the log to archive
    fs::rename(&audit_log_path, &archive_path)
        .map_err(|e| CascadeError::Custom(format!("failed to archive audit log: {}", e)))?;

    // Remove the count sidecar (safe to ignore if it doesn't exist)
    let _ = fs::remove_file(&count_sidecar_path);

    Ok(archive_path.to_string_lossy().to_string())
}

/// RAG query result (P4 scope stub).
#[derive(Debug, Serialize, Deserialize)]
pub struct RagResult {
    pub content: String,
    pub score: f32,
    pub source_path: String,
    pub chunk_index: usize,
}

// ---------------------------------------------------------------------------
// T-P3-E03-06: AI provider connection commands
// T-P3-E03-08: Daemon install + wizard-complete marker
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

// ---------------------------------------------------------------------------
// T-P3-E03-03: serde round-trip unit tests for WizardCheckpoint
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    /// Verify WizardCheckpoint serializes to camelCase JSON and round-trips intact.
    /// Acceptance: T-P3-E03-03 — "Rust unit test covers save→load round-trip with all fields"
    #[test]
    fn wizard_checkpoint_serde_camel_case_round_trip() {
        let original = WizardCheckpoint {
            step: 5,
            completed_steps: vec![1, 2, 3, 4],
            provider_connected: true,
            scan_result: Some(ScanResult {
                legacy_files_found: 42,
                total_size: 1_048_576,
                project_roots: vec!["/home/user/project".to_string()],
            }),
            merge_result: Some(MergeResult {
                merged_file_count: 40,
                conflicts_resolved: 2,
                timestamp: "2026-06-09T00:00:00Z".to_string(),
            }),
            archive_manifest_path: Some("/home/user/.cascade/manifest.json".to_string()),
            daemon_installed: false,
            started_at: "2026-06-09T00:00:00Z".to_string(),
            updated_at: "2026-06-09T00:01:00Z".to_string(),
            extra: serde_json::Map::new(),
        };

        let json = serde_json::to_string(&original).expect("serialization failed");

        // Verify camelCase keys are present in the JSON output.
        assert!(
            json.contains("\"completedSteps\""),
            "expected camelCase 'completedSteps', got: {}",
            json
        );
        assert!(
            json.contains("\"providerConnected\""),
            "expected camelCase 'providerConnected', got: {}",
            json
        );
        assert!(
            json.contains("\"scanResult\""),
            "expected camelCase 'scanResult', got: {}",
            json
        );
        assert!(
            json.contains("\"mergeResult\""),
            "expected camelCase 'mergeResult', got: {}",
            json
        );
        assert!(
            json.contains("\"archiveManifestPath\""),
            "expected camelCase 'archiveManifestPath', got: {}",
            json
        );
        assert!(
            json.contains("\"daemonInstalled\""),
            "expected camelCase 'daemonInstalled', got: {}",
            json
        );
        assert!(
            json.contains("\"startedAt\""),
            "expected camelCase 'startedAt', got: {}",
            json
        );
        assert!(
            json.contains("\"updatedAt\""),
            "expected camelCase 'updatedAt', got: {}",
            json
        );

        // Verify no snake_case keys leaked through.
        assert!(
            !json.contains("\"completed_steps\""),
            "snake_case key leaked: {}",
            json
        );
        assert!(
            !json.contains("\"provider_connected\""),
            "snake_case key leaked: {}",
            json
        );

        // ScanResult camelCase keys.
        assert!(
            json.contains("\"legacyFilesFound\""),
            "expected 'legacyFilesFound', got: {}",
            json
        );
        assert!(
            json.contains("\"totalSize\""),
            "expected 'totalSize', got: {}",
            json
        );
        assert!(
            json.contains("\"projectRoots\""),
            "expected 'projectRoots', got: {}",
            json
        );

        // MergeResult camelCase keys.
        assert!(
            json.contains("\"mergedFileCount\""),
            "expected 'mergedFileCount', got: {}",
            json
        );
        assert!(
            json.contains("\"conflictsResolved\""),
            "expected 'conflictsResolved', got: {}",
            json
        );

        // Round-trip: deserialize back and compare.
        let deserialized: WizardCheckpoint =
            serde_json::from_str(&json).expect("deserialization failed");

        assert_eq!(deserialized.step, original.step);
        assert_eq!(deserialized.completed_steps, original.completed_steps);
        assert_eq!(deserialized.provider_connected, original.provider_connected);
        assert_eq!(deserialized.daemon_installed, original.daemon_installed);
        assert_eq!(deserialized.started_at, original.started_at);
        assert_eq!(deserialized.updated_at, original.updated_at);
        assert_eq!(
            deserialized.archive_manifest_path,
            original.archive_manifest_path
        );

        let sr = deserialized
            .scan_result
            .expect("scan_result missing after round-trip");
        assert_eq!(sr.legacy_files_found, 42);
        assert_eq!(sr.total_size, 1_048_576);
        assert_eq!(sr.project_roots, vec!["/home/user/project"]);

        let mr = deserialized
            .merge_result
            .expect("merge_result missing after round-trip");
        assert_eq!(mr.merged_file_count, 40);
        assert_eq!(mr.conflicts_resolved, 2);
    }
}
