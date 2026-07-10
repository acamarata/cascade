//! # commands::wizard_state
//!
//! ## Purpose
//!
//! Tauri IPC commands that persist wizard UI state, dry-run logs, and merge
//! audit entries to disk in `~/.cascade/temp/`.  All four commands are thin
//! file-system helpers — no daemon involvement, no business logic.
//!
//! ## Commands
//!
//! | Tauri command              | Operation                                         |
//! |----------------------------|---------------------------------------------------|
//! | `wizard_state_read`        | Read wizard-state.json → Option<String> (raw JSON)|
//! | `wizard_state_write`       | Atomic write (tmp + rename) of wizard-state.json  |
//! | `wizard_dry_run_log`       | Append a JSON line to wizard-dry-run.log          |
//! | `wizard_merge_audit_append`| Append a pre-serialised JSON line to wizard-merge-audit.jsonl |
//!
//! ## Paths
//!
//! All files live under `global_cascade_dir()/temp/`:
//! - `wizard-state.json`          — serialised wizard form state
//! - `wizard-dry-run.log`         — NDJSON dry-run operation log
//! - `wizard-merge-audit.jsonl`   — NDJSON merge audit trail
//!
//! ## Constraints
//!
//! - `wizard_state_read` returns `None` on `NotFound`; any other IO error
//!   propagates as `CascadeError::Custom`.
//! - `wizard_state_write` uses a sibling `.tmp` file + rename for atomicity.
//! - Append commands use `OpenOptions::append(true)` and write a `\n`-terminated
//!   JSON line.  The directory is created with `create_dir_all` if absent.
//!
//! ## SPORT
//!
//! MASTER-COMMANDS.md — wizard_state IPC commands (wizard-state module)

use std::fs::{self, OpenOptions};
use std::io::{self, Write as IoWrite};
use std::time::{SystemTime, UNIX_EPOCH};

use cascade_types::paths::global_cascade_dir;
use tauri::State;
use tracing::debug;

use crate::error::CascadeError;
use crate::state::AppState;

// ── Path helpers ───────────────────────────────────────────────────────────────

fn temp_dir() -> std::path::PathBuf {
    global_cascade_dir().join("temp")
}

fn wizard_state_path() -> std::path::PathBuf {
    temp_dir().join("wizard-state.json")
}

fn dry_run_log_path() -> std::path::PathBuf {
    temp_dir().join("wizard-dry-run.log")
}

fn merge_audit_path() -> std::path::PathBuf {
    temp_dir().join("wizard-merge-audit.jsonl")
}

// ── Commands ──────────────────────────────────────────────────────────────────

/// Purpose: Read wizard-state.json from the app config temp dir.
///   Returns the raw JSON string, or `null` if the file does not yet exist.
///   Any IO error other than NotFound is propagated as `CascadeError::Custom`.
///
/// JS: `invoke("wizard_state_read")`
#[tauri::command]
pub async fn wizard_state_read(
    _state: State<'_, AppState>,
) -> Result<Option<String>, CascadeError> {
    let path = wizard_state_path();
    debug!("wizard_state_read: reading {:?}", path);

    match fs::read_to_string(&path) {
        Ok(content) => {
            debug!("wizard_state_read: {} bytes read", content.len());
            Ok(Some(content))
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            debug!("wizard_state_read: file not found, returning None");
            Ok(None)
        }
        Err(e) => Err(CascadeError::Custom(format!(
            "wizard_state_read: failed to read {path:?}: {e}"
        ))),
    }
}

/// Purpose: Atomically write `content` to wizard-state.json using a sibling
///   `.tmp` file + POSIX rename so the file is never partially written.
///   Creates parent directories if they do not exist.
///
/// JS: `invoke("wizard_state_write", { content: "..." })`
#[tauri::command]
pub async fn wizard_state_write(
    _state: State<'_, AppState>,
    content: String,
) -> Result<(), CascadeError> {
    let dir = temp_dir();
    let dest = wizard_state_path();
    let tmp = dest.with_extension("json.tmp");

    debug!(
        "wizard_state_write: writing {} bytes to {:?}",
        content.len(),
        dest
    );

    fs::create_dir_all(&dir).map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_state_write: cannot create dir {dir:?}: {e}"
        ))
    })?;

    fs::write(&tmp, &content).map_err(|e| {
        CascadeError::Custom(format!("wizard_state_write: cannot write tmp {tmp:?}: {e}"))
    })?;

    fs::rename(&tmp, &dest).map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_state_write: cannot rename {tmp:?} → {dest:?}: {e}"
        ))
    })?;

    debug!("wizard_state_write: done");
    Ok(())
}

/// Purpose: Append a dry-run log entry as a JSON line to wizard-dry-run.log.
///   Each line is a self-contained JSON object with operation, source,
///   destination, payload, and a Unix-millisecond timestamp.
///   Creates parent directories if they do not exist.
///
/// JS: `invoke("wizard_dry_run_log", { operation, source, destination, payload })`
#[tauri::command]
pub async fn wizard_dry_run_log(
    _state: State<'_, AppState>,
    operation: String,
    source: String,
    destination: String,
    payload: String,
) -> Result<(), CascadeError> {
    let dir = temp_dir();
    let log_path = dry_run_log_path();

    let timestamp_ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis();

    // Build the JSON line manually — no serde_json dep here, values are
    // caller-supplied strings (already validated by the frontend).
    let line = format!(
        "{{\"operation\":{op},\"source\":{src},\"destination\":{dst},\"payload\":{pay},\"timestamp\":{ts}}}\n",
        op  = json_string(&operation),
        src = json_string(&source),
        dst = json_string(&destination),
        pay = json_string(&payload),
        ts  = timestamp_ms,
    );

    debug!(
        "wizard_dry_run_log: appending {} bytes to {:?}",
        line.len(),
        log_path
    );

    fs::create_dir_all(&dir).map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_dry_run_log: cannot create dir {dir:?}: {e}"
        ))
    })?;

    let mut file = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)
        .map_err(|e| {
            CascadeError::Custom(format!("wizard_dry_run_log: cannot open {log_path:?}: {e}"))
        })?;

    file.write_all(line.as_bytes()).map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_dry_run_log: cannot write to {log_path:?}: {e}"
        ))
    })?;

    debug!("wizard_dry_run_log: done");
    Ok(())
}

/// Purpose: Append a pre-serialised JSON entry (one line) to wizard-merge-audit.jsonl.
///   The `entry` parameter must already be valid JSON — this command appends it
///   verbatim followed by a newline.  Creates parent directories if absent.
///
/// JS: `invoke("wizard_merge_audit_append", { entry: "{...}" })`
#[tauri::command]
pub async fn wizard_merge_audit_append(
    _state: State<'_, AppState>,
    entry: String,
) -> Result<(), CascadeError> {
    let dir = temp_dir();
    let audit_path = merge_audit_path();

    debug!(
        "wizard_merge_audit_append: appending {} bytes to {:?}",
        entry.len(),
        audit_path
    );

    fs::create_dir_all(&dir).map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_merge_audit_append: cannot create dir {dir:?}: {e}"
        ))
    })?;

    let mut file = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&audit_path)
        .map_err(|e| {
            CascadeError::Custom(format!(
                "wizard_merge_audit_append: cannot open {audit_path:?}: {e}"
            ))
        })?;

    file.write_all(entry.as_bytes()).map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_merge_audit_append: cannot write to {audit_path:?}: {e}"
        ))
    })?;
    file.write_all(b"\n").map_err(|e| {
        CascadeError::Custom(format!(
            "wizard_merge_audit_append: cannot write newline to {audit_path:?}: {e}"
        ))
    })?;

    debug!("wizard_merge_audit_append: done");
    Ok(())
}

/// Clears persisted wizard state. Called by the --reset wizard flag.
#[tauri::command]
pub async fn wizard_state_reset() -> Result<(), String> {
    let path = wizard_state_path();
    if path.exists() {
        std::fs::remove_file(&path).map_err(|e| e.to_string())?;
    }
    Ok(())
}

// ── Internal helpers ───────────────────────────────────────────────────────────

/// Escape a string value for inclusion in a JSON object literal.
/// Handles backslashes, double-quotes, and ASCII control characters.
fn json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for ch in s.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}
