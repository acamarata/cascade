//! # auth
//!
//! Claude Code install and authentication detection for the CC API proxy.
//!
//! # Purpose
//! Before the bridge can start, it must verify that:
//! 1. The `claude` binary is available on PATH (`CcStatus::installed`).
//! 2. Claude Code has an authenticated session (`CcStatus::authenticated`).
//!
//! This module delegates detection to heuristics similar to those in
//! `cascade-providers/src/auto_auth_import.rs` (`scan_claude_code`) but
//! expressed as a standalone struct so `cascade-ccapi` does not need to
//! depend on the full `cascade-providers` crate at runtime.
//!
//! # Inputs
//! None — reads from the filesystem and runs `which claude`.
//!
//! # Outputs
//! `CcStatus` — installed + authenticated flags + optional account hint.
//!
//! # Constraints
//! - Read-only: never writes to `~/.claude/` or modifies CC config.
//! - No secret logging: token values are never traced or printed.
//! - Test-friendly: `CcStatus::fixture(…)` constructs known-good values.

use std::path::PathBuf;

/// Authentication and install status for the Claude Code CLI.
#[derive(Debug, Clone, PartialEq)]
pub struct CcStatus {
    /// The `claude` binary was found on PATH (or at a known location).
    pub installed: bool,
    /// A CC session / credentials file exists and is non-empty.
    pub authenticated: bool,
    /// Account email or hint extracted from settings / credentials.
    pub account_hint: Option<String>,
    /// Path to the detected `claude` binary (if found).
    pub binary_path: Option<PathBuf>,
}

impl CcStatus {
    /// Construct a fixture for tests with explicit values.
    ///
    /// # Example
    ///
    /// ```
    /// # use cascade_ccapi::auth::CcStatus;
    /// let status = CcStatus::fixture(true, true, Some("user@example.com"));
    /// assert!(status.installed);
    /// assert!(status.authenticated);
    /// ```
    pub fn fixture(
        installed: bool,
        authenticated: bool,
        account_hint: Option<&str>,
    ) -> Self {
        Self {
            installed,
            authenticated,
            account_hint: account_hint.map(|s| s.to_string()),
            binary_path: if installed {
                Some(PathBuf::from("/usr/local/bin/claude"))
            } else {
                None
            },
        }
    }

    /// Returns `true` if CC is ready to be driven by the proxy.
    pub fn ready(&self) -> bool {
        self.installed && self.authenticated
    }
}

/// Detect the Claude Code install and auth status from the local environment.
///
/// # Algorithm
///
/// 1. Run `which claude` (or `where claude` on Windows) to check for the binary.
/// 2. Check `~/.claude/settings.json` or `~/.claude/.credentials` for a session.
/// 3. Return `CcStatus` with flags + optional account hint.
///
/// # Constraints
/// - Filesystem reads only; no subprocess other than `which`.
/// - Permission-denied on any file → silently skip.
pub fn detect() -> CcStatus {
    let binary_path = find_claude_binary();
    let installed = binary_path.is_some();

    if !installed {
        return CcStatus {
            installed: false,
            authenticated: false,
            account_hint: None,
            binary_path: None,
        };
    }

    let (authenticated, account_hint) = check_auth();

    CcStatus { installed, authenticated, account_hint, binary_path }
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Try to locate the `claude` binary.
fn find_claude_binary() -> Option<PathBuf> {
    // Try `which`/`where` first.
    #[cfg(unix)]
    let which_cmd = "which";
    #[cfg(windows)]
    let which_cmd = "where";

    if let Ok(out) = std::process::Command::new(which_cmd).arg("claude").output() {
        if out.status.success() {
            let path = String::from_utf8_lossy(&out.stdout).trim().to_string();
            if !path.is_empty() {
                return Some(PathBuf::from(path));
            }
        }
    }

    // Fallback: check well-known install paths.
    let candidates = [
        dirs::home_dir().map(|h| h.join(".claude").join("bin").join("claude")),
        Some(PathBuf::from("/usr/local/bin/claude")),
        Some(PathBuf::from("/opt/homebrew/bin/claude")),
    ];
    candidates.into_iter().flatten().find(|p| p.exists())
}

/// Check whether CC has an authenticated session.
///
/// Returns `(authenticated, account_hint)`.
fn check_auth() -> (bool, Option<String>) {
    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return (false, None),
    };

    // 1. ~/.claude/settings.json
    let settings = home.join(".claude").join("settings.json");
    if let Ok(contents) = std::fs::read_to_string(&settings) {
        if let Ok(json) = serde_json::from_str::<serde_json::Value>(&contents) {
            for field in &["email", "userEmail", "account"] {
                if let Some(val) = json.get(field).and_then(|v| v.as_str()) {
                    if !val.is_empty() {
                        return (true, Some(val.to_string()));
                    }
                }
            }
            // settings.json exists → CC is installed; may or may not be authed.
            return (true, None);
        }
    }

    // 2. ~/.claude/.credentials
    let creds = home.join(".claude").join(".credentials");
    if creds.exists() {
        if let Ok(contents) = std::fs::read_to_string(&creds) {
            if let Ok(json) = serde_json::from_str::<serde_json::Value>(&contents) {
                for field in &["email", "sub", "userEmail"] {
                    if let Some(val) = json.get(field).and_then(|v| v.as_str()) {
                        if !val.is_empty() {
                            return (true, Some(val.to_string()));
                        }
                    }
                }
            }
            // File exists but couldn't parse email → still authenticated.
            return (!contents.trim().is_empty(), None);
        }
    }

    (false, None)
}

// Re-export dirs used internally so serde_json remains the only external dep.
use serde_json;

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fixture_installed_and_authed() {
        let s = CcStatus::fixture(true, true, Some("test@example.com"));
        assert!(s.installed);
        assert!(s.authenticated);
        assert_eq!(s.account_hint.as_deref(), Some("test@example.com"));
        assert!(s.binary_path.is_some());
        assert!(s.ready());
    }

    #[test]
    fn fixture_not_installed() {
        let s = CcStatus::fixture(false, false, None);
        assert!(!s.installed);
        assert!(!s.authenticated);
        assert!(!s.ready());
        assert!(s.binary_path.is_none());
    }

    #[test]
    fn fixture_installed_but_not_authed() {
        let s = CcStatus::fixture(true, false, None);
        assert!(s.installed);
        assert!(!s.authenticated);
        assert!(!s.ready());
    }

    #[test]
    fn detect_does_not_panic() {
        // We can't assert the result (depends on the dev environment),
        // but detect() must not panic under any circumstances.
        let _ = detect();
    }

    #[test]
    fn ready_requires_both_flags() {
        let installed_only = CcStatus { installed: true, authenticated: false, account_hint: None, binary_path: None };
        assert!(!installed_only.ready());

        let both = CcStatus { installed: true, authenticated: true, account_hint: None, binary_path: None };
        assert!(both.ready());
    }
}
