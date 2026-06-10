//! Canonical path and socket constants for the Cascade daemon and configuration.
//!
//! Every crate that needs a well-known path (daemon socket, PID file, config
//! directory) MUST import from this module. No hardcoded path strings anywhere
//! else in the workspace. This resolves CRIT-3 from the Pass 1 architecture
//! review.
//!
//! # Platform notes
//!
//! - **macOS / Linux:** socket is a Unix domain socket at `~/.cascade/daemon.sock`.
//! - **Windows:** named pipe `\\.\pipe\cascade-daemon`; the `DAEMON_SOCK`
//!   constant is still provided but points to the pipe path for uniformity.
//!
//! All functions that return `PathBuf` expand `~` via [`home_dir`].

use std::path::PathBuf;

// ── String constants (platform-independent relative names) ───────────────────

/// Name of the cascade working directory, placed under `$HOME`.
pub const CASCADE_DIR_NAME: &str = ".cascade";

/// Name of the main instruction file inside a `.cascade/` directory.
pub const CASCADE_MD_NAME: &str = "CASCADE.md";

/// Symlink name that Claude Code tools read (points to `CASCADE.md`).
pub const CLAUDE_MD_NAME: &str = "CLAUDE.md";

/// Symlink name that OpenCode reads (points to `CASCADE.md`).
pub const AGENTS_MD_NAME: &str = "AGENTS.md";

/// Subdirectory names inside a `.cascade/` directory.
pub mod subdirs {
    pub const MEMORY: &str = "memory";
    pub const DOCS: &str = "docs";
    pub const IDEAS: &str = "ideas";
    pub const INBOX: &str = "inbox";
    pub const RULES: &str = "rules";
    pub const TASKS: &str = "tasks";
    pub const PLANNING: &str = "planning";
    pub const PHASES: &str = "phases";
    pub const QA: &str = "qa";
    pub const AGENTS: &str = "agents";
    pub const SKILLS: &str = "skills";
    pub const ARCHIVE: &str = "archive";
    pub const TEMP: &str = "temp";
    pub const PROVIDERS: &str = "providers";
}

// ── Path resolution functions ─────────────────────────────────────────────────

/// Returns `$HOME`, or `/tmp/cascade-fallback` if the home directory cannot
/// be determined. The fallback prevents a panic in unusual environments (CI
/// containers, sandboxes).
///
/// Callers that REQUIRE a real home directory should check the returned path
/// starts with a known prefix.
pub fn home_dir() -> PathBuf {
    std::env::var("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|_| PathBuf::from("/tmp/cascade-fallback"))
}

/// Returns the path to the global `.cascade/` directory: `$HOME/.cascade/`.
pub fn global_cascade_dir() -> PathBuf {
    home_dir().join(CASCADE_DIR_NAME)
}

/// Returns the path to the daemon Unix socket.
///
/// - macOS / Linux: `$HOME/.cascade/daemon.sock`
/// - Windows: `\\.\pipe\cascade-daemon`
pub fn daemon_socket() -> PathBuf {
    #[cfg(target_os = "windows")]
    {
        PathBuf::from(DAEMON_PIPE_WIN)
    }
    #[cfg(not(target_os = "windows"))]
    {
        global_cascade_dir().join("daemon.sock")
    }
}

/// Returns the path where the daemon writes its PID: `$HOME/.cascade/daemon.pid`.
pub fn daemon_pid() -> PathBuf {
    global_cascade_dir().join("daemon.pid")
}

/// Returns the path to the global config file: `$HOME/.cascade/config.toml`.
///
/// On Windows this expands to `%APPDATA%\cascade\config.toml` when `%APPDATA%`
/// is set; otherwise falls back to the same home-relative path.
pub fn global_config() -> PathBuf {
    #[cfg(target_os = "windows")]
    {
        if let Ok(appdata) = std::env::var("APPDATA") {
            return PathBuf::from(appdata).join("cascade").join("config.toml");
        }
    }
    global_cascade_dir().join("config.toml")
}

/// Returns the path to the daemon state file (JSON, machine-written).
pub fn daemon_state() -> PathBuf {
    global_cascade_dir().join("daemon-state.json")
}

/// Returns the path to the per-session quota state file used by the Gemini
/// proxy and Claw Fleet widget.
pub fn quota_state() -> PathBuf {
    global_cascade_dir().join("temp").join("quota-state.json")
}

// ── Windows named pipe constant ───────────────────────────────────────────────

/// Windows named pipe path for the Cascade daemon.
///
/// Used by `cascade daemon` subcommands on Windows. On Unix, prefer
/// [`daemon_socket()`] which returns the Unix socket path.
pub const DAEMON_PIPE_WIN: &str = r"\\.\pipe\cascade-daemon";

// ── Project-scoped helpers ────────────────────────────────────────────────────

/// Returns the `.cascade/` directory for a given project root.
pub fn project_cascade_dir(project_root: &std::path::Path) -> PathBuf {
    project_root.join(CASCADE_DIR_NAME)
}

/// Returns the `CASCADE.md` path for a given project root.
pub fn project_cascade_md(project_root: &std::path::Path) -> PathBuf {
    project_cascade_dir(project_root).join(CASCADE_MD_NAME)
}

/// Returns the full path to a named subdirectory inside a project's `.cascade/`.
pub fn project_subdir(project_root: &std::path::Path, subdir: &str) -> PathBuf {
    project_cascade_dir(project_root).join(subdir)
}

// ── Canary test helper ────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn global_cascade_dir_ends_with_dot_cascade() {
        let dir = global_cascade_dir();
        assert_eq!(dir.file_name().unwrap(), ".cascade");
    }

    #[test]
    fn daemon_socket_is_absolute() {
        let sock = daemon_socket();
        // On Windows the named-pipe path starts with \\
        // On Unix it must be an absolute path under $HOME
        #[cfg(not(target_os = "windows"))]
        assert!(sock.is_absolute(), "daemon socket path must be absolute");
    }

    #[test]
    fn subdirs_are_lowercase_no_slashes() {
        let all = [
            subdirs::MEMORY,
            subdirs::DOCS,
            subdirs::IDEAS,
            subdirs::INBOX,
            subdirs::RULES,
            subdirs::TASKS,
            subdirs::PLANNING,
            subdirs::PHASES,
            subdirs::QA,
            subdirs::AGENTS,
            subdirs::SKILLS,
            subdirs::ARCHIVE,
            subdirs::TEMP,
            subdirs::PROVIDERS,
        ];
        for name in all {
            assert_eq!(
                name,
                name.to_lowercase(),
                "subdir name must be lowercase: {name}"
            );
            assert!(!name.contains('/'), "subdir must not contain slash: {name}");
        }
    }
}
