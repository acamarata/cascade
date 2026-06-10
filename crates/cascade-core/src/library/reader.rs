//! # cascade_core::library::reader
//!
//! List and get operations for the `~/.cascade/library/` file store.
//!
//! ## Purpose
//!
//! Provides two functions:
//! - [`list_items`] — scan one or all type subdirectories and return all
//!   [`LibraryItem`]s, optionally filtered by kind.
//! - [`get_item`] — load a single item by (kind, slug) pair.
//!
//! ## Constraints
//!
//! - All paths stay inside `library_root` (no traversal).
//! - Non-YAML files and directories are silently skipped.
//! - A file that fails to parse logs a warning and is skipped (never panics).
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — library Rust module, read ops (T-P3-E07-02)

use std::fs;
use std::path::Path;

use tracing::warn;

use cascade_types::error::CascadeError;

use super::types::LibraryItem;

/// List all library items, optionally filtered by kind directory name.
///
/// `item_type` accepts the directory name strings: `"prompts"`, `"agents"`,
/// `"personas"`, or `"slash-commands"`. Pass `None` to return all kinds.
///
/// Files that fail to parse are logged and skipped.
pub fn list_items(
    library_root: &Path,
    item_type: Option<&str>,
) -> Result<Vec<LibraryItem>, CascadeError> {
    let dirs_to_scan: Vec<&str> = if let Some(t) = item_type {
        vec![t]
    } else {
        vec!["prompts", "agents", "personas", "slash-commands"]
    };

    let mut items = Vec::new();

    for dir_name in dirs_to_scan {
        let dir_path = library_root.join(dir_name);
        if !dir_path.exists() {
            continue;
        }

        let read_dir = fs::read_dir(&dir_path).map_err(|e| CascadeError::Io {
            path: dir_path.clone(),
            operation: "read_dir",
            source: e,
        })?;

        for entry in read_dir.flatten() {
            let file_path = entry.path();
            if file_path.extension().and_then(|s| s.to_str()) != Some("yaml") {
                continue;
            }

            match load_yaml_item(&file_path) {
                Ok(item) => items.push(item),
                Err(e) => {
                    warn!(path = %file_path.display(), error = %e, "library: skipping unparseable item");
                }
            }
        }
    }

    Ok(items)
}

/// Load a single library item by kind directory name and slug.
///
/// Returns `CascadeError::PathNotFound` if the file does not exist.
pub fn get_item(
    library_root: &Path,
    item_type: &str,
    slug: &str,
) -> Result<LibraryItem, CascadeError> {
    let file_path = library_root.join(item_type).join(format!("{slug}.yaml"));

    if !file_path.exists() {
        return Err(CascadeError::PathNotFound {
            path: file_path,
        });
    }

    load_yaml_item(&file_path)
}

// ── Internal helpers ─────────────────────────────────────────────────────────

fn load_yaml_item(path: &Path) -> Result<LibraryItem, CascadeError> {
    let bytes = fs::read(path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "read",
        source: e,
    })?;

    let item: LibraryItem = serde_yaml::from_slice(&bytes).map_err(|e| CascadeError::ConfigParse {
        path: path.to_path_buf(),
        detail: e.to_string(),
    })?;

    Ok(item)
}
