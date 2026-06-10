//! # cascade_core::settings::store
//!
//! Atomic load/save for [`CascadeSettings`] backed by `~/.cascade/settings.json`.
//!
//! ## Purpose
//!
//! Provides two functions: `load` reads (or creates) the settings file, and
//! `save` writes it atomically via a sibling `.tmp` file + `fs::rename`.
//!
//! ## Inputs
//!
//! `path` is expected to be `~/.cascade/settings.json` (resolved by the
//! caller; this module is path-agnostic to stay testable).
//!
//! ## Outputs
//!
//! `load` returns `CascadeSettings`; `save` writes the file and returns `()`.
//!
//! ## Constraints
//!
//! - Missing file → `Default::default()` (not an error).
//! - Atomic write: writes to `settings.json.tmp` first, then
//!   `std::fs::rename` to the target path (POSIX-atomic on same filesystem).
//! - Parent directory is created if it does not exist.
//!
//! ## SPORT
//!
//! MASTER-RUST-CRATES.md — cascade-core: settings::store

use std::fs;
use std::path::Path;

use cascade_types::error::CascadeError;

use super::types::CascadeSettings;

/// Map a `std::io::Error` to `CascadeError::Io` with the given path and operation.
fn io_err(path: &Path, operation: &'static str, source: std::io::Error) -> CascadeError {
    CascadeError::Io {
        path: path.to_path_buf(),
        operation,
        source,
    }
}

/// Load settings from `path`.
///
/// Returns `CascadeSettings::default()` when the file does not exist. Any
/// other I/O error, or a JSON parse failure, is propagated as
/// [`CascadeError`].
///
/// # Errors
///
/// - [`CascadeError::Io`] for unexpected I/O failures.
/// - Serde errors are wrapped as [`CascadeError::ConfigParse`].
pub fn load(path: &Path) -> Result<CascadeSettings, CascadeError> {
    match fs::read(path) {
        Ok(bytes) => {
            let s: CascadeSettings =
                serde_json::from_slice(&bytes).map_err(|e| CascadeError::ConfigParse {
                    path: path.to_path_buf(),
                    detail: e.to_string(),
                })?;
            Ok(s)
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(CascadeSettings::default()),
        Err(e) => Err(io_err(path, "read", e)),
    }
}

/// Atomically write `settings` to `path`.
///
/// Writes to `{path}.tmp` first, then renames to `path`.  Creates all
/// parent directories if needed.
///
/// # Errors
///
/// - [`CascadeError::Io`] for any filesystem failure.
pub fn save(path: &Path, settings: &CascadeSettings) -> Result<(), CascadeError> {
    // Ensure parent directory exists.
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| io_err(parent, "create_dir_all", e))?;
    }

    let tmp = path.with_extension("json.tmp");

    let json = serde_json::to_string_pretty(settings).map_err(|e| CascadeError::ConfigParse {
        path: path.to_path_buf(),
        detail: format!("settings serialize: {e}"),
    })?;

    fs::write(&tmp, json.as_bytes()).map_err(|e| io_err(&tmp, "write", e))?;
    fs::rename(&tmp, path).map_err(|e| io_err(path, "rename", e))?;

    Ok(())
}
