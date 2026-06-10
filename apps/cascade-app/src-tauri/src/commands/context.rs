//! # commands::context
//!
//! Tauri IPC commands for context session CRUD — bridges the React frontend to
//! `cascade_core::context_pack`.
//!
//! ## Purpose
//!
//! These commands let the frontend create, list, read, update, and delete named
//! context sessions persisted under `.cascade/contexts/`. Both a global root
//! (`~/.cascade/contexts/`) and a caller-supplied project-level root are
//! supported — the frontend passes `root` as an optional path parameter.
//!
//! ## Commands
//!
//! | Tauri command         | Operation                                         |
//! |-----------------------|---------------------------------------------------|
//! | `context_list`        | Scan contexts dir → `Vec<ContextMeta>`            |
//! | `context_get`         | Load one session by id → `ContextSession`         |
//! | `context_upsert`      | Atomic create-or-replace a session                |
//! | `context_delete`      | Idempotent remove a session                       |
//!
//! ## IPC naming
//!
//! Tauri maps snake_case command names to camelCase on the JS side automatically.
//! All struct fields use `#[serde(rename_all = "camelCase")]`.
//!
//! ## Path resolution
//!
//! When `root` is `None`, the global `~/.cascade/contexts/` directory is used.
//! When `root` is `Some(path)`, `{path}/.cascade/contexts/` is used.
//!
//! ## Constraints
//!
//! - All path arguments must be absolute; the HOME var is sourced from the OS.
//! - Atomic writes use tmp→rename — partial writes are impossible.
//! - `context_delete` is idempotent — no error on missing id.
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — context IPC commands (T-P3-E07-08)

use serde::{Deserialize, Serialize};

use cascade_core::context_pack::{
    delete_context, get_context, list_contexts, pack_codebase, upsert_context, ContextMeta,
    ContextSession,
};
use cascade_types::paths::global_cascade_dir;

// ── Error type ─────────────────────────────────────────────────────────────────

/// Typed error envelope returned by every context command.
///
/// `#[serde(tag = "kind")]` makes every error arrive at the frontend as
/// `{ "kind": "NotFound", "message": "..." }` — forward-compatible.
#[derive(Debug, thiserror::Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum ContextError {
    #[error("not found: {message}")]
    NotFound { message: String },

    #[error("io error: {message}")]
    Io { message: String },

    #[error("parse error: {message}")]
    Parse { message: String },
}

impl ContextError {
    fn from_cascade(e: cascade_types::error::CascadeError) -> Self {
        let msg = e.to_string();
        if msg.contains("not found") || msg.contains("context not found") {
            ContextError::NotFound { message: msg }
        } else {
            ContextError::Io { message: msg }
        }
    }
}

// ── Path helper ────────────────────────────────────────────────────────────────

/// Resolve the contexts directory from an optional project root.
///
/// - `None`  → `~/.cascade/contexts/`
/// - `Some(p)` → `{p}/.cascade/contexts/`
fn contexts_dir(root: Option<String>) -> std::path::PathBuf {
    match root {
        None => global_cascade_dir().join("contexts"),
        Some(r) => std::path::PathBuf::from(r).join(".cascade").join("contexts"),
    }
}

// ── IPC commands ───────────────────────────────────────────────────────────────

/// List all context sessions in the given root.
///
/// Inputs:  `root` — optional project root; None = global.
/// Outputs: `Vec<ContextMeta>` sorted by id.
#[tauri::command]
pub fn context_list(root: Option<String>) -> Result<Vec<ContextMeta>, ContextError> {
    let dir = contexts_dir(root);
    list_contexts(&dir).map_err(ContextError::from_cascade)
}

/// Load a single context session by id.
///
/// Inputs:  `root` — optional project root; `id` — session slug.
/// Outputs: `ContextSession` on success.
#[tauri::command]
pub fn context_get(root: Option<String>, id: String) -> Result<ContextSession, ContextError> {
    let dir = contexts_dir(root);
    get_context(&dir, &id).map_err(ContextError::from_cascade)
}

/// Create or replace a context session.
///
/// Inputs:  `root` — optional project root; `session` — full session payload.
/// Outputs: unit on success.
#[tauri::command]
pub fn context_upsert(
    root: Option<String>,
    session: ContextSession,
) -> Result<(), ContextError> {
    let dir = contexts_dir(root);
    upsert_context(&dir, &session).map_err(ContextError::from_cascade)
}

/// Delete a context session by id (idempotent).
///
/// Inputs:  `root` — optional project root; `id` — session slug.
/// Outputs: unit on success; no error if already absent.
#[tauri::command]
pub fn context_delete(root: Option<String>, id: String) -> Result<(), ContextError> {
    let dir = contexts_dir(root);
    delete_context(&dir, &id).map_err(ContextError::from_cascade)
}

/// Pack a directory into a single UTF-8 text bundle (repomix-style).
///
/// Inputs:
///   `root`     — absolute path of the directory to pack; must be inside HOME.
///   `excludes` — optional additional gitignore-compatible glob patterns.
///
/// Outputs:
///   `String` — absolute path of the generated temp file. The caller is
///   responsible for deleting the file when it is no longer needed.
///
/// Errors:
///   Returns `ContextError::Io` for I/O failures or traversal rejection,
///   and `ContextError::TooLarge` when the bundle would exceed 2 MB.
///
/// # SPORT
///
/// MASTER-COMPONENTS.md — pack_codebase IPC (T-P3-E07-09)
#[tauri::command]
pub fn pack_codebase_cmd(
    root: String,
    excludes: Option<Vec<String>>,
) -> Result<String, ContextError> {
    let root_path = std::path::PathBuf::from(&root);
    let extra = excludes.unwrap_or_default();
    pack_codebase(&root_path, &extra)
        .map(|p| p.to_string_lossy().into_owned())
        .map_err(|e| ContextError::Io { message: e.to_string() })
}
