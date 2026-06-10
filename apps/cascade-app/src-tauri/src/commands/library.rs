//! # commands::library
//!
//! Tauri IPC commands for the Cascade library — bridges the React frontend
//! to the file-based `~/.cascade/library/` store.
//!
//! ## Purpose
//!
//! Exposes four commands:
//! - `list_library_items` — list all items, optionally filtered by type.
//! - `get_library_item`   — load a single item by (id, type) pair.
//! - `upsert_library_item` — create or overwrite a library item.
//! - `delete_library_item` — remove an item; idempotent.
//!
//! ## IPC naming
//!
//! Tauri maps snake_case command names to camelCase on the JS side
//! automatically (`listLibraryItems`, `getLibraryItem`, etc.).
//! `LibraryItem` struct fields are `snake_case` on disk but the Tauri layer
//! re-serialises them; callers should match the struct field names.
//!
//! ## Constraints
//!
//! - Library root is always resolved to `$HOME/.cascade/library/`.
//! - Both commands return `Result<_, String>` (Tauri 2 IPC requirement).
//! - `delete_library_item` is idempotent: missing file → `Ok(())`.
//! - `inject_library_item` is idempotent: already-injected items return success.
//!
//! ## SPORT
//!
//! MASTER-COMMANDS.md — library IPC commands (T-P3-E07-02/03/07)

use std::env;

use cascade_core::library::inject::{inject_item, InjectionTarget};
use cascade_core::library::reader::{get_item, list_items};
use cascade_core::library::types::LibraryItem;
use cascade_core::library::writer::{delete_item, upsert_item};
use cascade_types::paths::global_cascade_dir;
use serde::Serialize;
use tauri::State;

use crate::state::AppState;

// ── Home-dir resolver (IPC-local, HOME-confined) ──────────────────────────────

fn home_dir() -> std::path::PathBuf {
    #[cfg(target_os = "windows")]
    let var = "USERPROFILE";
    #[cfg(not(target_os = "windows"))]
    let var = "HOME";

    env::var(var)
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|_| std::path::PathBuf::from("/tmp/cascade-fallback"))
}

/// Resolve the canonical library root path.
fn library_root() -> std::path::PathBuf {
    global_cascade_dir().join("library")
}

/// List all library items.
///
/// `item_type` filters by directory name: `"prompts"`, `"agents"`,
/// `"personas"`, or `"slash-commands"`. Pass `null` / omit to return all.
///
/// Returns a JSON array of [`LibraryItem`] objects.
#[tauri::command]
pub async fn list_library_items(
    _state: State<'_, AppState>,
    item_type: Option<String>,
) -> Result<Vec<LibraryItem>, String> {
    let root = library_root();
    list_items(&root, item_type.as_deref()).map_err(|e| e.to_string())
}

/// Load a single library item by slug and type.
///
/// `id` is the slug (filename without `.yaml`).
/// `item_type` is the directory name: `"prompts"`, `"agents"`, etc.
///
/// Returns `CascadeError::PathNotFound` (as String) if the item does not
/// exist.
#[tauri::command]
pub async fn get_library_item(
    _state: State<'_, AppState>,
    id: String,
    item_type: String,
) -> Result<LibraryItem, String> {
    let root = library_root();
    get_item(&root, &item_type, &id).map_err(|e| e.to_string())
}

/// Create or overwrite a library item.
///
/// The item's `id` and `item_type` fields determine the target file path.
/// Parent directories are created if needed.
#[tauri::command]
pub async fn upsert_library_item(
    _state: State<'_, AppState>,
    item: LibraryItem,
) -> Result<(), String> {
    let root = library_root();
    upsert_item(&root, &item).map_err(|e| e.to_string())
}

/// Delete a library item.
///
/// Idempotent: deleting a non-existent item returns `Ok(())`.
///
/// `id` is the item slug; `item_type` is the directory name.
#[tauri::command]
pub async fn delete_library_item(
    _state: State<'_, AppState>,
    id: String,
    item_type: String,
) -> Result<(), String> {
    let root = library_root();
    delete_item(&root, &item_type, &id).map_err(|e| e.to_string())
}

/// Inject a library item into a harness config file.
///
/// # Purpose
///
/// Appends a formatted, marker-wrapped block for the item into the appropriate
/// harness config file, creating parent directories as needed.  Idempotent:
/// if the item is already present (detected by the `<!-- cascade:{id} -->` marker),
/// this command returns `InjectResult::already_present = true` without writing.
///
/// # Inputs
///
/// - `id` — item slug (e.g. `"refactor-typescript"`).
/// - `item_type` — directory name: `"prompts"`, `"agents"`, `"personas"`,
///   or `"slash-commands"`.
/// - `harness` — target harness: `"cc"`, `"oc"`, or `"codex"`.
/// - `dry_run` — if `true`, returns the predicted outcome without writing.
///
/// # Outputs
///
/// [`InjectResult`] with `injected`, `already_present`, or `would_inject`.
///
/// # Constraints
///
/// - HOME-confined: all paths resolved from `$HOME` (never hardcoded).
/// - Atomic write: tmp file + rename on every code path.
/// - Returns `Err(String)` only on unexpected I/O failures; idempotency is
///   surfaced via the `already_present` field, not an error.
///
/// # SPORT
///
/// MASTER-COMMANDS.md — inject_library_item (T-P3-E07-07)
#[tauri::command]
pub async fn inject_library_item(
    _state: State<'_, AppState>,
    id: String,
    item_type: String,
    harness: String,
    dry_run: Option<bool>,
) -> Result<InjectResult, String> {
    let root = library_root();
    let home = home_dir();

    // Load the item to inject
    let item = get_item(&root, &item_type, &id).map_err(|e| e.to_string())?;

    // Parse the harness target
    let target =
        InjectionTarget::from_str(&harness).ok_or_else(|| format!("unknown harness '{harness}'; expected 'cc', 'oc', or 'codex'"))?;

    let outcome = inject_item(&item, target, &home, dry_run.unwrap_or(false))
        .map_err(|e| e.to_string())?;

    use cascade_core::library::inject::InjectionOutcome;
    Ok(match outcome {
        InjectionOutcome::Injected => InjectResult {
            injected: true,
            already_present: false,
            would_inject: false,
        },
        InjectionOutcome::AlreadyPresent => InjectResult {
            injected: false,
            already_present: true,
            would_inject: false,
        },
        InjectionOutcome::WouldInject => InjectResult {
            injected: false,
            already_present: false,
            would_inject: true,
        },
    })
}

/// Result returned by `inject_library_item`.
///
/// Exactly one of the three boolean fields is true per call.
#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InjectResult {
    /// Block was appended successfully.
    pub injected: bool,
    /// Block was already present — nothing written (idempotent).
    pub already_present: bool,
    /// Dry-run: would have injected but wrote nothing.
    pub would_inject: bool,
}
