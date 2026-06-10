//! Context store reader — scans `.cascade/contexts/` and deserializes sessions.
//!
//! Purpose: provides `list_contexts` (returns Vec<ContextMeta> from directory
//!   scan) and `get_context` (loads a single ContextSession by id).
//! Inputs:  `contexts_dir: &Path` — root of the contexts directory.
//! Outputs: `Vec<ContextMeta>` or `ContextSession`.
//! Constraints: malformed YAML files are skipped with a warning; they never
//!   cause list_contexts to fail. get_context returns CascadeError::Other on
//!   missing or malformed files.
//! SPORT: MASTER-COMPONENTS.md — context_pack::reader (T-P3-E07-08)

use std::path::Path;

use tracing::warn;

use cascade_types::error::{CascadeError, Result};

use super::types::{ContextMeta, ContextSession};

// ── list_contexts ──────────────────────────────────────────────────────────────

/// Scan `contexts_dir` for `*.yaml` files and return lightweight metadata.
///
/// Purpose: fast enumeration for the UI — only id, title, and item_count are
///   read; the full items list is not loaded.
/// Inputs:  `contexts_dir` — directory to scan (created if missing).
/// Outputs: `Vec<ContextMeta>` sorted by id alphabetically.
/// Constraints: any YAML that fails to parse is skipped with a `warn!` log;
///   files without a `.yaml` extension are ignored.
pub fn list_contexts(contexts_dir: &Path) -> Result<Vec<ContextMeta>> {
    if !contexts_dir.exists() {
        return Ok(vec![]);
    }

    let mut metas: Vec<ContextMeta> = Vec::new();

    let entries = std::fs::read_dir(contexts_dir)
        .map_err(|e| CascadeError::Other(format!("read_dir {}: {}", contexts_dir.display(), e)))?;

    for entry in entries {
        let entry = match entry {
            Ok(e) => e,
            Err(e) => {
                warn!("context_pack: read_dir entry error: {}", e);
                continue;
            }
        };

        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) != Some("yaml") {
            continue;
        }

        let raw = match std::fs::read_to_string(&path) {
            Ok(s) => s,
            Err(e) => {
                warn!("context_pack: failed to read {}: {}", path.display(), e);
                continue;
            }
        };

        let session: ContextSession = match serde_yaml::from_str(&raw) {
            Ok(s) => s,
            Err(e) => {
                warn!("context_pack: malformed YAML at {}: {}", path.display(), e);
                continue;
            }
        };

        metas.push(ContextMeta {
            id: session.id,
            title: session.title,
            item_count: session.items.len(),
        });
    }

    metas.sort_by(|a, b| a.id.cmp(&b.id));
    Ok(metas)
}

// ── get_context ────────────────────────────────────────────────────────────────

/// Load a single `ContextSession` by `id` from `contexts_dir`.
///
/// Purpose: returns the full session including all items.
/// Inputs:  `contexts_dir` — directory containing session YAML files.
///          `id`           — session identifier (also the filename slug).
/// Outputs: `ContextSession` on success.
/// Constraints: returns `CascadeError::Other` if the file is missing or
///   malformed.
pub fn get_context(contexts_dir: &Path, id: &str) -> Result<ContextSession> {
    let path = contexts_dir.join(format!("{}.yaml", id));
    if !path.exists() {
        return Err(CascadeError::Other(format!("context not found: {}", id)));
    }

    let raw = std::fs::read_to_string(&path)
        .map_err(|e| CascadeError::Other(format!("read {}: {}", path.display(), e)))?;

    let session: ContextSession = serde_yaml::from_str(&raw)
        .map_err(|e| CascadeError::Other(format!("parse {}: {}", path.display(), e)))?;

    Ok(session)
}
