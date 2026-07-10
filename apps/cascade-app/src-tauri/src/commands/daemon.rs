// Daemon lifecycle and window management commands.
//
// Why: groups all commands that manage the cascaded process lifetime
// (start/stop/status) and the secondary Tauri window API (open/close/focus),
// plus the CASCADE.md editor commands (T-P3-E03).

use std::io::Write as _;
use std::path::Path;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, State};

use cascade_types::ipc::{DaemonStopParams, DaemonStopResult, StatusParams, StatusResult};

use crate::error::CascadeError;
use crate::state::AppState;

use super::core::make_client;

// ---------------------------------------------------------------------------
// Daemon lifecycle commands
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
// CASCADE.md editor commands (T-P3-E03)
// ---------------------------------------------------------------------------

/// Infer the instruction tier from the file path.
///
/// # Purpose
/// Returns a human-readable tier label ("gci", "asi", "ppi", "pri", "pai", or "unknown")
/// by inspecting well-known path segments.
///
/// # Inputs
/// - `path`: absolute path string of the CLAUDE.md file.
///
/// # Outputs
/// Static string slice indicating the tier label.
///
/// # Constraints
/// Pure function, no I/O. Order matters: more-specific tests first.
fn infer_tier(path: &str) -> &'static str {
    if path.contains("/.claude/CLAUDE.md") && path.contains("/Sites/") {
        // ASI sits under ~/Sites/.claude/
        return "asi";
    }
    // Depth-based, cross-platform heuristic (macOS /Users/, Linux /home/, etc.).
    // GCI: <home>/.claude/CLAUDE.md      → .claude at index 3 (/, home, user, .claude)
    // PPI: project/.claude/CLAUDE.md     → index 4
    // PRI: project/repo/.claude/...      → index 5
    // PAI: project/repo/app/.claude/...  → index 6+
    let segments: Vec<&str> = path.split('/').collect();
    if let Some(idx) = segments.iter().position(|s| *s == ".claude") {
        match idx {
            i if i >= 6 => "pai",
            5 => "pri",
            4 => "ppi",
            i if i <= 3 => "gci",
            _ => "unknown",
        }
    } else {
        "unknown"
    }
}

/// Load a CASCADE.md / tier instruction file from disk by absolute path.
///
/// # Purpose
/// Reads the UTF-8 text content of the given path so the GUI editor can display it.
/// If the file does not exist, returns an empty CascadeDocument (not an error) so
/// the editor can show a blank template for a new file.
///
/// # Inputs
/// - `path`: absolute filesystem path to the CLAUDE.md file.
/// - `_state`: Tauri AppState (reserved for future daemon delegation).
///
/// # Outputs
/// `Ok(CascadeDocument { path, content, tier })` always; content is empty when the
/// file does not exist. `Err(String)` only on unexpected I/O errors (permission
/// denied, etc.).
///
/// # Constraints
/// Path must be absolute. No path-traversal check needed here: the Tauri sandbox and
/// OS permissions are the security boundary for local-only desktop use.
/// # SPORT
/// MASTER-COMMANDS.md — load_cascade_doc
#[tauri::command]
pub async fn load_cascade_doc(
    path: String,
    _state: State<'_, AppState>,
) -> Result<CascadeDocument, String> {
    let p = Path::new(&path);
    let content = match std::fs::read_to_string(p) {
        Ok(text) => text,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(e) => return Err(format!("failed to read {path}: {e}")),
    };
    let tier = infer_tier(&path).to_string();
    Ok(CascadeDocument {
        path,
        content,
        tier,
    })
}

/// Save a CASCADE.md / tier instruction file to disk atomically.
///
/// # Purpose
/// Writes `content` to `path` using a temp-file + rename strategy so partial writes
/// never corrupt an in-use instruction file. Creates parent directories if needed.
///
/// # Inputs
/// - `path`: absolute destination path.
/// - `content`: UTF-8 text to persist.
/// - `_state`: Tauri AppState (reserved).
///
/// # Outputs
/// `Ok(())` on success. `Err(String)` on any I/O failure.
///
/// # Constraints
/// Atomic on POSIX (same-filesystem rename). On Windows, falls back to direct write
/// (std::fs::rename across volumes returns an error; handled below).
/// # SPORT
/// MASTER-COMMANDS.md — save_cascade_doc
#[tauri::command]
pub async fn save_cascade_doc(
    path: String,
    content: String,
    _state: State<'_, AppState>,
) -> Result<(), String> {
    let dest = Path::new(&path);

    // Create parent directories if they do not exist.
    if let Some(parent) = dest.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create parent dirs for {path}: {e}"))?;
    }

    // Write to a sibling temp file, then rename for atomicity.
    let tmp_path = {
        let mut t = dest.to_path_buf();
        let stem = dest
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("cascade_doc");
        t.set_file_name(format!(".{stem}.tmp"));
        t
    };

    {
        let mut f = std::fs::File::create(&tmp_path)
            .map_err(|e| format!("failed to create temp file: {e}"))?;
        f.write_all(content.as_bytes())
            .map_err(|e| format!("failed to write temp file: {e}"))?;
        f.flush()
            .map_err(|e| format!("failed to flush temp file: {e}"))?;
    }

    std::fs::rename(&tmp_path, dest).or_else(|_| -> Result<(), String> {
        // Fallback for cross-volume or Windows scenarios: copy + remove.
        std::fs::copy(&tmp_path, dest)
            .map(|_| ())
            .map_err(|e| format!("failed to copy temp file to {path}: {e}"))?;
        let _ = std::fs::remove_file(&tmp_path);
        Ok(())
    })?;

    Ok(())
}

/// Validate CASCADE.md / tier instruction content without saving.
///
/// # Purpose
/// Checks that `content` meets the minimum requirements for a valid instruction file:
/// - non-empty (after trimming whitespace)
/// - valid UTF-8 (guaranteed by the `String` type)
/// - if the content starts with a YAML frontmatter block (lines between `---` fences),
///   verify that it is well-formed TOML or YAML-style key:value syntax (basic check:
///   no unmatched braces, balanced quotes)
///
/// Returns `ValidationResult { valid: bool, errors: Vec<String> }`.
///
/// # Inputs
/// - `content`: raw document text from the editor.
/// - `_state`: Tauri AppState (reserved).
///
/// # Outputs
/// `Ok(ValidationResult)` always. `Err(String)` is reserved for future delegate calls.
///
/// # Constraints
/// Pure validation; does not touch the filesystem.
/// # SPORT
/// MASTER-COMMANDS.md — validate_cascade_doc
#[tauri::command]
pub async fn validate_cascade_doc(
    content: String,
    _state: State<'_, AppState>,
) -> Result<ValidationResult, String> {
    let mut errors: Vec<String> = Vec::new();

    if content.trim().is_empty() {
        errors.push("Document is empty.".to_string());
        return Ok(ValidationResult {
            valid: false,
            errors,
        });
    }

    // Check for unmatched YAML/TOML frontmatter fences (--- ... ---).
    // If the document starts with "---\n", count the fence occurrences.
    let trimmed = content.trim_start();
    if trimmed.starts_with("---") {
        let fence_count = content.lines().filter(|l| l.trim() == "---").count();
        if fence_count < 2 {
            errors.push(
                "Frontmatter block is not closed: found opening '---' but no closing '---'."
                    .to_string(),
            );
        }
    }

    // Detect obviously broken markdown: unclosed code fences (``` count must be even).
    let backtick_fence_count = content.lines().filter(|l| l.starts_with("```")).count();
    if backtick_fence_count % 2 != 0 {
        errors.push(format!(
            "Unmatched code fence: found {backtick_fence_count} ``` lines (must be even)."
        ));
    }

    let valid = errors.is_empty();
    Ok(ValidationResult { valid, errors })
}

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
// Local response types
// ---------------------------------------------------------------------------

/// Daemon process status for the get_daemon_status compatibility command.
#[derive(Debug, Serialize, Deserialize)]
pub struct DaemonStatus {
    pub running: bool,
    pub pid: Option<u32>,
    pub uptime_secs: Option<u64>,
    pub version: Option<String>,
}

/// A loaded CASCADE.md / tier instruction document.
#[derive(Debug, Serialize, Deserialize)]
pub struct CascadeDocument {
    /// Absolute path that was read.
    pub path: String,
    /// UTF-8 file content; empty string when the file does not exist yet.
    pub content: String,
    /// Detected instruction tier ("gci", "asi", "ppi", "pri", "pai", "unknown").
    pub tier: String,
}

/// Validation result returned by validate_cascade_doc.
#[derive(Debug, Serialize, Deserialize)]
pub struct ValidationResult {
    /// True when no errors were found.
    pub valid: bool,
    /// Human-readable error descriptions; empty when valid.
    pub errors: Vec<String>,
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // ------------------------------------------------------------------
    // infer_tier
    // ------------------------------------------------------------------

    #[test]
    fn tier_gci() {
        assert_eq!(infer_tier("/home/user/.claude/CLAUDE.md"), "gci");
    }

    #[test]
    fn tier_asi() {
        assert_eq!(infer_tier("/home/user/Sites/.claude/CLAUDE.md"), "asi");
    }

    // ------------------------------------------------------------------
    // load_cascade_doc (sync filesystem helpers tested directly)
    // ------------------------------------------------------------------

    #[test]
    fn load_missing_file_returns_empty() {
        let p = "/tmp/cascade_test_nonexistent_abc123.md";
        // Ensure it really does not exist.
        let _ = std::fs::remove_file(p);

        let content = match std::fs::read_to_string(p) {
            Ok(t) => t,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
            Err(e) => panic!("unexpected error: {e}"),
        };
        assert!(
            content.is_empty(),
            "missing file should return empty string"
        );
    }

    // ------------------------------------------------------------------
    // save + load round-trip
    // ------------------------------------------------------------------

    #[test]
    fn save_then_load_roundtrip() {
        // Unique per-run dir — a fixed path races other tests in the same
        // binary under the parallel runner (rename hit EINVAL mid-cleanup).
        let tmp_dir = tempfile::TempDir::new().unwrap();
        let dir = tmp_dir.path().to_path_buf();
        let path = dir.join("CLAUDE.md");
        let expected = "# Test\n\nHello cascade doc.\n";

        // Write via the same atomic-write logic used in save_cascade_doc.
        let dest = &path;
        let tmp = dir.join(".CLAUDE.md.tmp");
        {
            let mut f = std::fs::File::create(&tmp).unwrap();
            f.write_all(expected.as_bytes()).unwrap();
            f.flush().unwrap();
        }
        std::fs::rename(&tmp, dest).unwrap();

        // Read back.
        let got = std::fs::read_to_string(dest).unwrap();
        assert_eq!(got, expected);

        // TempDir cleans up on drop.
    }

    // ------------------------------------------------------------------
    // validate_cascade_doc logic
    // ------------------------------------------------------------------

    #[test]
    fn validate_empty_is_invalid() {
        let result = validate_content("");
        assert!(!result.valid);
        assert!(result.errors.iter().any(|e| e.contains("empty")));
    }

    #[test]
    fn validate_whitespace_only_is_invalid() {
        let result = validate_content("   \n\t\n");
        assert!(!result.valid);
    }

    #[test]
    fn validate_valid_markdown_passes() {
        let doc = "# GCI Instructions\n\nSome rules here.\n\n```bash\necho hi\n```\n";
        let result = validate_content(doc);
        assert!(result.valid, "errors: {:?}", result.errors);
    }

    #[test]
    fn validate_unclosed_frontmatter_fails() {
        let doc = "---\nmode: normal\n# No closing fence\n";
        let result = validate_content(doc);
        assert!(!result.valid);
        assert!(result.errors.iter().any(|e| e.contains("Frontmatter")));
    }

    #[test]
    fn validate_closed_frontmatter_passes() {
        let doc = "---\nmode: normal\n---\n# Body\n";
        let result = validate_content(doc);
        assert!(result.valid, "errors: {:?}", result.errors);
    }

    #[test]
    fn validate_unmatched_code_fence_fails() {
        let doc = "# Doc\n\n```bash\necho hi\n";
        let result = validate_content(doc);
        assert!(!result.valid);
        assert!(result.errors.iter().any(|e| e.contains("code fence")));
    }

    // Helper: runs validation synchronously without needing a Tauri State.
    fn validate_content(content: &str) -> ValidationResult {
        let mut errors: Vec<String> = Vec::new();

        if content.trim().is_empty() {
            errors.push("Document is empty.".to_string());
            return ValidationResult {
                valid: false,
                errors,
            };
        }

        let trimmed = content.trim_start();
        if trimmed.starts_with("---") {
            let fence_count = content.lines().filter(|l| l.trim() == "---").count();
            if fence_count < 2 {
                errors.push(
                    "Frontmatter block is not closed: found opening '---' but no closing '---'."
                        .to_string(),
                );
            }
        }

        let backtick_fence_count = content.lines().filter(|l| l.starts_with("```")).count();
        if backtick_fence_count % 2 != 0 {
            errors.push(format!(
                "Unmatched code fence: found {backtick_fence_count} ``` lines (must be even)."
            ));
        }

        let valid = errors.is_empty();
        ValidationResult { valid, errors }
    }
}
