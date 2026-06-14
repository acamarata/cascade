//! # commands::tasks
//!
//! Purpose: Thin Tauri IPC command wrappers for the Cascade Kanban task store.
//!   Delegates all logic to the daemon's JSON-RPC task_* handlers via IpcClient.
//!
//! Inputs: JSON params from the React frontend via Tauri invoke.
//!
//! Outputs: Result<serde_json::Value, String> (Tauri 2 IPC requirement).
//!
//! Constraints:
//!   - Naming: snake_case here → camelCase on the JS side (Tauri auto-transform).
//!   - All params / results use cascade_types::task wire types (camelCase serde).
//!   - State is not used; IpcClient is instantiated per-call via make_client().
//!   - Every command uses `send::<Value, Value>` to pass params verbatim to the daemon.
//!
//! SPORT: MASTER-COMMANDS.md — task_create, task_get, task_list, task_update,
//!     task_delete, task_move (E-P8-02)

use serde_json::Value;
use tauri::State;

use crate::commands::make_client;
use crate::state::AppState;

// ── Internal helper ────────────────────────────────────────────────────────────

/// Send a JSON-RPC method with Value params, return Value result.
async fn task_rpc(method: &'static str, params: Value) -> Result<Value, String> {
    let client = make_client().map_err(|e| e.to_string())?;
    client
        .send::<Value, Value>(method, params)
        .await
        .map_err(|e| e.to_string())
}

// ── Commands ──────────────────────────────────────────────────────────────────

/// Create a new kanban task.
/// JS: `invoke("task_create", { title, project?, status?, tags?, priority?, ... })`
#[tauri::command]
pub async fn task_create(_state: State<'_, AppState>, params: Value) -> Result<Value, String> {
    task_rpc("task_create", params).await
}

/// Get a single task by id.
/// JS: `invoke("task_get", { id })`
#[tauri::command]
pub async fn task_get(_state: State<'_, AppState>, params: Value) -> Result<Value, String> {
    task_rpc("task_get", params).await
}

/// List tasks with optional filter.
/// JS: `invoke("task_list", { filter: { project?, status?, tag?, assignee? } })`
#[tauri::command]
pub async fn task_list(_state: State<'_, AppState>, params: Value) -> Result<Value, String> {
    task_rpc("task_list", params).await
}

/// Update a task — PATCH semantics; only provided fields are changed.
/// JS: `invoke("task_update", { id, title?, status?, tags?, priority?, ... })`
#[tauri::command]
pub async fn task_update(_state: State<'_, AppState>, params: Value) -> Result<Value, String> {
    task_rpc("task_update", params).await
}

/// Delete a task by id.
/// JS: `invoke("task_delete", { id })`
#[tauri::command]
pub async fn task_delete(_state: State<'_, AppState>, params: Value) -> Result<Value, String> {
    task_rpc("task_delete", params).await
}

/// Move a task to a new status column and board order.
/// JS: `invoke("task_move", { id, status, order })`
#[tauri::command]
pub async fn task_move(_state: State<'_, AppState>, params: Value) -> Result<Value, String> {
    task_rpc("task_move", params).await
}
