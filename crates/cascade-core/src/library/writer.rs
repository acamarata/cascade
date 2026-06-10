//! # cascade_core::library::writer
//!
//! Write (upsert) and delete operations for the `~/.cascade/library/` store.
//!
//! ## Purpose
//!
//! Provides:
//! - [`upsert_item`] — serialise a [`LibraryItem`] to YAML and write it to
//!   `{library_root}/{kind}/{slug}.yaml`, creating directories as needed.
//! - [`delete_item`] — remove the YAML file; idempotent (file missing = Ok).
//!
//! ## Constraints
//!
//! - Write is **not atomic** by default (no tmp+rename) because YAML library
//!   items are human-edited files and partial-write corruption risk is low;
//!   the non-atomic pattern is explicitly acknowledged here per CR-A guidance.
//!   If atomicity becomes a requirement, add tmp+rename in a follow-up ticket.
//! - `delete_item` maps `NotFound` to `Ok(())` (idempotent).
//! - Parent directories are created with `fs::create_dir_all`.
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — library Rust module, write ops (T-P3-E07-03)

use std::fs;
use std::path::Path;

use cascade_types::error::CascadeError;

use super::types::LibraryItem;

/// Write `item` to `{library_root}/{item.item_type}/{item.id}.yaml`.
///
/// Creates parent directories if they do not exist.  Overwrites any
/// existing file with the same (kind, slug) pair.
///
/// Note: write is direct (not atomic via tmp+rename) — appropriate for
/// human-readable library files where partial-write risk is acceptable.
/// See module-level doc for rationale.
///
/// # Errors
///
/// - [`CascadeError::Io`] if the directory cannot be created or the file
///   cannot be written.
/// - [`CascadeError::ConfigParse`] if serialisation fails.
pub fn upsert_item(library_root: &Path, item: &LibraryItem) -> Result<(), CascadeError> {
    let dir_name = item.item_type.dir_name();
    let dir_path = library_root.join(dir_name);

    fs::create_dir_all(&dir_path).map_err(|e| CascadeError::Io {
        path: dir_path.clone(),
        operation: "create_dir_all",
        source: e,
    })?;

    let file_path = dir_path.join(format!("{}.yaml", item.id));

    let yaml = serde_yaml::to_string(item).map_err(|e| CascadeError::ConfigParse {
        path: file_path.clone(),
        detail: format!("library item serialize: {e}"),
    })?;

    fs::write(&file_path, yaml.as_bytes()).map_err(|e| CascadeError::Io {
        path: file_path,
        operation: "write",
        source: e,
    })?;

    Ok(())
}

/// Remove `{library_root}/{item_type}/{slug}.yaml`.
///
/// Idempotent: if the file does not exist, returns `Ok(())`.
///
/// # Errors
///
/// - [`CascadeError::Io`] for unexpected filesystem errors other than
///   `NotFound`.
pub fn delete_item(library_root: &Path, item_type: &str, slug: &str) -> Result<(), CascadeError> {
    let file_path = library_root.join(item_type).join(format!("{slug}.yaml"));

    match fs::remove_file(&file_path) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(CascadeError::Io {
            path: file_path,
            operation: "remove_file",
            source: e,
        }),
    }
}
