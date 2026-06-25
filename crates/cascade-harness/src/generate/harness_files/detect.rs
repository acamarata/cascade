//! Harness installation detection helpers.

use std::path::{Path, PathBuf};

/// Returns true if `binary_name` resolves on `$PATH`.
pub(super) fn is_binary_on_path(binary_name: &str) -> bool {
    std::env::var_os("PATH")
        .map(|paths| {
            std::env::split_paths(&paths).any(|dir| {
                let candidate = dir.join(binary_name);
                candidate.is_file() || {
                    // macOS / Linux: check with executable permission.
                    #[cfg(unix)]
                    {
                        use std::os::unix::fs::PermissionsExt;
                        candidate
                            .metadata()
                            .map(|m| m.permissions().mode() & 0o111 != 0)
                            .unwrap_or(false)
                    }
                    #[cfg(not(unix))]
                    {
                        candidate.exists()
                    }
                }
            })
        })
        .unwrap_or(false)
}

/// Returns true if a Cursor global config directory exists.
pub(super) fn cursor_global_config_exists() -> bool {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/tmp"));

    // Linux / Windows: ~/.cursor/
    if home.join(".cursor").exists() {
        return true;
    }
    // macOS: ~/Library/Application Support/Cursor/
    let mac_path = home
        .join("Library")
        .join("Application Support")
        .join("Cursor");
    if mac_path.exists() {
        return true;
    }
    false
}

/// Returns true if an Antigravity global config directory exists.
pub(super) fn antigravity_global_config_exists() -> bool {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/tmp"));

    // Cross-platform: ~/.config/antigravity/ or ~/.antigravity/
    if home.join(".config").join("antigravity").exists() {
        return true;
    }
    if home.join(".antigravity").exists() {
        return true;
    }
    // macOS: ~/Library/Application Support/Antigravity/
    #[cfg(target_os = "macos")]
    {
        let mac_path = home
            .join("Library")
            .join("Application Support")
            .join("Antigravity");
        if mac_path.exists() {
            return true;
        }
    }
    false
}

/// Resolve a path under the user's home directory.
pub(super) fn home_path(rel: &str) -> PathBuf {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/tmp"))
        .join(rel)
}

/// Returns true if a harness binary / config is present on disk for the given
/// harness kind. Extracted here to keep `kind.rs` free of detection logic.
///
/// Detection heuristics (in priority order):
/// 1. Binary on `PATH`.
/// 2. Known config directory existence.
/// 3. Workspace marker file (e.g. `.cursor/settings.json`).
pub(super) fn harness_is_installed(kind: &super::kind::HarnessKind, workspace: &Path) -> bool {
    use super::kind::HarnessKind;
    match kind {
        HarnessKind::ClaudeCode => is_binary_on_path("claude"),
        HarnessKind::OpenCode => is_binary_on_path("opencode"),
        HarnessKind::Codex => is_binary_on_path("codex"),
        HarnessKind::Cursor => {
            workspace.join(".cursor").exists()
                || cursor_global_config_exists()
                || is_binary_on_path("cursor")
        }
        HarnessKind::Aider => {
            is_binary_on_path("aider")
                || home_path(".aider.conf.yml").exists()
                || home_path(".aider").exists()
        }
        HarnessKind::Antigravity => {
            workspace.join(".antigravity").exists()
                || antigravity_global_config_exists()
                || is_binary_on_path("antigravity")
        }
    }
}
