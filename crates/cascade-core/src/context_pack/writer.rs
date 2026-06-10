//! Context store writer — atomic upsert and delete of `.cascade/contexts/` YAML files.
//!
//! Purpose: provides `upsert_context` (creates or replaces a session YAML via
//!   tmp→rename atomic write) and `delete_context` (idempotent removal).
//! Inputs:  `contexts_dir: &Path`, `ContextSession` (for upsert), `id: &str` (for delete).
//! Outputs: unit on success; `CascadeError::Other` on I/O or serialization failure.
//! Constraints: atomic writes use a sibling `.tmp` file + `std::fs::rename`;
//!   creates the `contexts_dir` if it does not exist.
//!   `delete_context` returns `Ok(())` when the file is already absent (idempotent).
//! SPORT: MASTER-COMPONENTS.md — context_pack::writer (T-P3-E07-08)

use std::path::Path;

use cascade_types::error::{CascadeError, Result};

use super::types::ContextSession;

// ── upsert_context ─────────────────────────────────────────────────────────────

/// Atomically write `session` to `{contexts_dir}/{session.id}.yaml`.
///
/// Purpose: create-or-replace a context session. Uses a sibling `.tmp` file
///   and POSIX rename for atomicity — partial reads are impossible.
/// Inputs:  `contexts_dir` — parent directory (created if missing).
///          `session`      — session to persist; `id` is used as the slug.
/// Outputs: `Ok(())` on success.
/// Constraints: panics-safe — never leaves a corrupt file. Returns
///   `CascadeError::Other` on any I/O or serialization failure.
pub fn upsert_context(contexts_dir: &Path, session: &ContextSession) -> Result<()> {
    std::fs::create_dir_all(contexts_dir).map_err(|e| {
        CascadeError::Other(format!("create_dir_all {}: {}", contexts_dir.display(), e))
    })?;

    let yaml = serde_yaml::to_string(session)
        .map_err(|e| CascadeError::Other(format!("serialize context '{}': {}", session.id, e)))?;

    let final_path = contexts_dir.join(format!("{}.yaml", session.id));
    let tmp_path = contexts_dir.join(format!("{}.yaml.tmp", session.id));

    std::fs::write(&tmp_path, &yaml)
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {}", tmp_path.display(), e)))?;

    std::fs::rename(&tmp_path, &final_path).map_err(|e| {
        // Best-effort cleanup of the tmp file.
        let _ = std::fs::remove_file(&tmp_path);
        CascadeError::Other(format!(
            "rename {} -> {}: {}",
            tmp_path.display(),
            final_path.display(),
            e
        ))
    })?;

    Ok(())
}

// ── delete_context ─────────────────────────────────────────────────────────────

/// Remove `{contexts_dir}/{id}.yaml` if it exists.
///
/// Purpose: idempotent deletion — returns `Ok(())` whether or not the file
///   was present.
/// Inputs:  `contexts_dir` — directory containing session YAML files.
///          `id`           — session identifier (used as filename slug).
/// Outputs: `Ok(())` always (unless an unexpected I/O error occurs beyond
///   NotFound — e.g. permission denied).
/// Constraints: only `ErrorKind::NotFound` is silenced; all other errors
///   are returned as `CascadeError::Other`.
pub fn delete_context(contexts_dir: &Path, id: &str) -> Result<()> {
    let path = contexts_dir.join(format!("{}.yaml", id));
    match std::fs::remove_file(&path) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(CascadeError::Other(format!(
            "delete {}: {}",
            path.display(),
            e
        ))),
    }
}
