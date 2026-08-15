//! # commands::harness
//!
//! Tauri IPC command for harness config-file auto-detection.
//!
//! ## Purpose
//!
//! `detect_harness_path` checks common install locations for each supported
//! harness (Claude Code, OpenCode, Codex) and returns the first existing
//! config file path, or `null` when none is found.
//!
//! ## IPC naming
//!
//! Tauri maps `detect_harness_path` to `detectHarnessPath` on the JS side.
//!
//! ## Constraints
//!
//! - Read-only: only `std::fs::metadata` (file-existence checks); no side effects.
//! - Respects `$XDG_CONFIG_HOME` for Linux/Unix config locations.
//! - Returns `Ok(None)` when no config file exists (not an error).
//!
//! ## SPORT
//!
//! MASTER-COMMANDS.md — detect_harness_path (T-P7-E10-02)

use std::path::PathBuf;

use crate::error::CascadeError;

/// Resolve the user's home directory.
fn home_dir() -> PathBuf {
    dirs::home_dir().unwrap_or_else(|| PathBuf::from("/"))
}

/// Resolve `$XDG_CONFIG_HOME` or fall back to `~/.config`.
fn xdg_config_home() -> PathBuf {
    if let Ok(xdg) = std::env::var("XDG_CONFIG_HOME") {
        let p = PathBuf::from(xdg);
        if !p.as_os_str().is_empty() {
            return p;
        }
    }
    home_dir().join(".config")
}

/// Return the ordered list of candidate config-file paths for a harness slug.
///
/// Supported slugs: `"cc"` (Claude Code), `"oc"` (OpenCode), `"codex"`.
/// Unknown slugs return an empty list.
fn candidate_paths(harness: &str) -> Vec<PathBuf> {
    let home = home_dir();
    let xdg = xdg_config_home();

    match harness {
        "cc" => vec![
            home.join(".claude").join("settings.json"),
            home.join(".claude.json"),
        ],
        "oc" => vec![
            xdg.join("opencode").join("opencode.json"),
            home.join(".config").join("opencode").join("opencode.json"),
            home.join(".opencode").join("opencode.json"),
        ],
        "codex" => vec![
            xdg.join("codex").join("config.yaml"),
            home.join(".config").join("codex").join("config.yaml"),
            home.join(".codex").join("config.yaml"),
        ],
        _ => Vec::new(),
    }
}

/// Detect the config file path for a given harness.
///
/// # Purpose
/// Powers the "Auto-Detect" button in the Harness Bridges settings tab.
/// Checks common install locations for the requested harness and returns
/// the first config file that exists on disk.
///
/// # Inputs
/// - `harness`: Harness slug — `"cc"`, `"oc"`, or `"codex"`.
///
/// # Outputs
/// `Ok(Some(path))` when a config file is found; `Ok(None)` when the harness
/// is recognised but no config file exists at any candidate location.
/// `Err(CascadeError::InvalidParams)` when the harness slug is unknown.
///
/// # Constraints
/// - Read-only filesystem access (metadata stat only).
/// - No process spawning, no network calls.
///
/// # SPORT
/// MASTER-COMMANDS.md — detect_harness_path
#[tauri::command]
pub fn detect_harness_path(harness: String) -> Result<Option<String>, CascadeError> {
    let candidates = candidate_paths(&harness);
    if candidates.is_empty() {
        return Err(CascadeError::InvalidParams(format!(
            "unknown harness '{harness}' — supported: cc, oc, codex"
        )));
    }

    for path in candidates {
        if path.is_file() {
            return Ok(Some(path.display().to_string()));
        }
    }

    Ok(None)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use std::sync::Mutex;
    use tempfile::TempDir;

    /// Serialise tests that mutate HOME / XDG_CONFIG_HOME.
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    /// Set HOME to `tmp` and XDG_CONFIG_HOME to `tmp/.config`, return both paths.
    fn isolate_env(tmp: &TempDir) -> (PathBuf, PathBuf) {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("HOME", tmp.path().as_os_str());
        let xdg = tmp.path().join(".config");
        std::env::set_var("XDG_CONFIG_HOME", xdg.as_os_str());
        (tmp.path().to_path_buf(), xdg)
    }

    #[test]
    #[serial(global_env)]
    fn detects_cc_settings_json() {
        let tmp = TempDir::new().unwrap();
        let (home, _xdg) = isolate_env(&tmp);
        fs::create_dir_all(home.join(".claude")).unwrap();
        fs::write(home.join(".claude").join("settings.json"), "{}").unwrap();

        let result = detect_harness_path("cc".to_string()).unwrap();
        assert_eq!(
            result,
            Some(
                home.join(".claude")
                    .join("settings.json")
                    .display()
                    .to_string()
            )
        );
    }

    #[test]
    #[serial(global_env)]
    fn detects_oc_opencode_json() {
        let tmp = TempDir::new().unwrap();
        let (_, xdg) = isolate_env(&tmp);
        fs::create_dir_all(xdg.join("opencode")).unwrap();
        fs::write(xdg.join("opencode").join("opencode.json"), "{}").unwrap();

        let result = detect_harness_path("oc".to_string()).unwrap();
        assert!(result.unwrap().ends_with("opencode/opencode.json"));
    }

    #[test]
    #[serial(global_env)]
    fn detects_codex_config_yaml() {
        let tmp = TempDir::new().unwrap();
        let (_, xdg) = isolate_env(&tmp);
        fs::create_dir_all(xdg.join("codex")).unwrap();
        fs::write(xdg.join("codex").join("config.yaml"), "model: gpt-4").unwrap();

        let result = detect_harness_path("codex".to_string()).unwrap();
        assert!(result.unwrap().ends_with("codex/config.yaml"));
    }

    #[test]
    #[serial(global_env)]
    fn returns_none_when_no_config_found() {
        let tmp = TempDir::new().unwrap();
        let _ = isolate_env(&tmp);

        let result = detect_harness_path("cc".to_string()).unwrap();
        assert_eq!(result, None);
    }

    #[test]
    #[serial(global_env)]
    fn returns_err_for_unknown_harness() {
        let tmp = TempDir::new().unwrap();
        let _ = isolate_env(&tmp);

        let result = detect_harness_path("unknown".to_string());
        assert!(result.is_err());
    }

    #[test]
    #[serial(global_env)]
    fn detects_cc_fallback_claude_json() {
        let tmp = TempDir::new().unwrap();
        let (home, _xdg) = isolate_env(&tmp);
        // No .claude/settings.json, but ~/.claude.json exists
        fs::write(home.join(".claude.json"), "{}").unwrap();

        let result = detect_harness_path("cc".to_string()).unwrap();
        assert_eq!(
            result,
            Some(home.join(".claude.json").display().to_string())
        );
    }
}
