//! # cascade_core::security::env_allowlist
//!
//! Environment variable injection guard applied at daemon startup.
//!
//! ## Responsibilities
//!
//! Any environment variable whose name is NOT on the allowlist is:
//! 1. Logged at WARN with its name (value is never logged).
//! 2. Removed from the process environment via `std::env::remove_var`.
//! 3. Recorded as an `AuditEvent::EnvVarStripped` event.
//!
//! Additionally, values for allowlisted variables are canonicalized:
//! - `PATH` entries have separators normalized per platform.
//! - All values have null bytes and ASCII control characters stripped.
//!
//! ## STRIDE mapping
//!
//! Denial of service (D) and Elevation (E) — prevents an attacker who can
//! influence the daemon's launch environment (e.g. through a compromised shell
//! profile or a malicious launchd plist) from injecting `LD_PRELOAD`,
//! `DYLD_INSERT_LIBRARIES`, `PYTHONPATH`, or similar variables that would alter
//! process behavior.
//!
//! ## Usage
//!
//! Call `EnvVarAllowlist::apply()` as the first action in `main()`, before any
//! other initialization code reads environment variables.
//!
//! ```rust,no_run
//! use cascade_core::security::env_allowlist::EnvVarAllowlist;
//!
//! #[tokio::main]
//! async fn main() {
//!     EnvVarAllowlist::default().apply().await;
//!     // … rest of startup
//! }
//! ```

use crate::security::audit::{AuditEvent, AuditLogger};

// ── ALLOWED_ENV_VARS ──────────────────────────────────────────────────────────

/// The explicit allowlist of environment variables the daemon is permitted to
/// read. Any key not in this list is stripped from the process environment.
///
/// The list is conservative by design. Add entries only when a specific
/// subsystem genuinely requires the variable.
pub const ALLOWED_ENV_VARS: &[&str] = &[
    // Standard POSIX runtime vars
    "HOME",
    "USER",
    "LOGNAME",
    "SHELL",
    "TERM",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "TZ",
    // macOS-specific
    "TMPDIR",
    "DYLD_LIBRARY_PATH", // Allowed only if required by a bundled library — see NOTE below
    // Linux
    "XDG_RUNTIME_DIR",
    "XDG_DATA_HOME",
    "XDG_CONFIG_HOME",
    "DBUS_SESSION_BUS_ADDRESS",
    // Cascade-specific
    "CASCADE_CONFIG_DIR",
    "CASCADE_LOG_LEVEL",
    "CASCADE_SOCKET_PATH",
    "CASCADE_VAULT_PATH",
    // PATH is allowlisted but canonicalized
    "PATH",
    // Rust async runtime tuning
    "TOKIO_WORKER_THREADS",
    // Test harness — only present in test builds
    #[cfg(test)]
    "RUST_LOG",
    #[cfg(test)]
    "RUST_BACKTRACE",
];

// NOTE on DYLD_LIBRARY_PATH: macOS SIP strips this for protected binaries.
// It is included here only because some bundled native libraries (e.g. llama.cpp
// inference backend) may require it. Remove if the daemon has no bundled dylibs.

// ── EnvVarAllowlist ───────────────────────────────────────────────────────────

/// Guard that enforces the environment variable allowlist at daemon startup.
///
/// Holds a reference to an `AuditLogger` so that stripped variables are
/// recorded in the tamper-evident audit log.
pub struct EnvVarAllowlist {
    logger: AuditLogger,
    /// Override allowlist (used in tests).
    extra_allowed: Vec<String>,
}

impl Default for EnvVarAllowlist {
    fn default() -> Self {
        Self {
            logger: AuditLogger::noop(),
            extra_allowed: Vec::new(),
        }
    }
}

impl EnvVarAllowlist {
    /// Create with a specific logger.
    pub fn with_logger(logger: AuditLogger) -> Self {
        Self {
            logger,
            extra_allowed: Vec::new(),
        }
    }

    /// Add extra allowlist entries (useful in integration tests).
    pub fn allow(mut self, var: impl Into<String>) -> Self {
        self.extra_allowed.push(var.into());
        self
    }

    /// Enforce the allowlist and canonicalize values.
    ///
    /// Must be called before any other environment reads.
    pub async fn apply(&self) {
        let all_vars: Vec<(String, String)> = std::env::vars().collect();
        for (key, _value) in all_vars {
            if self.is_allowed(&key) {
                // Canonicalize the value: strip null bytes and control chars.
                let cleaned =
                    self.canonicalize_value(&key, &std::env::var(&key).unwrap_or_default());
                // Only update if cleaning changed the value.
                if cleaned != std::env::var(&key).unwrap_or_default() {
                    std::env::set_var(&key, &cleaned);
                }
            } else {
                tracing::warn!(var = %key, "stripping disallowed env var");
                std::env::remove_var(&key);
                self.logger
                    .log(AuditEvent::EnvVarStripped { key: key.clone() })
                    .await;
            }
        }
    }

    fn is_allowed(&self, key: &str) -> bool {
        ALLOWED_ENV_VARS.contains(&key) || self.extra_allowed.iter().any(|s| s == key)
    }

    /// Canonicalize an environment variable value.
    ///
    /// - Strips null bytes (U+0000) and ASCII control characters (0x01..0x1F,
    ///   0x7F) from all values.
    /// - For `PATH`, additionally normalizes duplicate separators.
    fn canonicalize_value(&self, key: &str, value: &str) -> String {
        // Strip control characters and null bytes.
        let cleaned: String = value
            .chars()
            .filter(|&c| c != '\0' && !c.is_ascii_control())
            .collect();

        if key == "PATH" {
            // Deduplicate consecutive separators on Unix.
            #[cfg(unix)]
            {
                cleaned
                    .split(':')
                    .filter(|s| !s.is_empty())
                    .collect::<Vec<_>>()
                    .join(":")
            }
            #[cfg(not(unix))]
            {
                cleaned
            }
        } else {
            cleaned
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    // Deliberate: the std MutexGuard must span the await so process-env mutation
    // stays serialized while `apply()` reads and strips env vars.
    #[allow(clippy::await_holding_lock)]
    async fn strips_disallowed_var() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        // Inject a rogue variable.
        std::env::set_var("_CASCADE_TEST_ROGUE_VAR_12345", "evil");
        assert!(std::env::var("_CASCADE_TEST_ROGUE_VAR_12345").is_ok());

        let guard = EnvVarAllowlist::default();
        guard.apply().await;

        // Variable must be gone.
        assert!(
            std::env::var("_CASCADE_TEST_ROGUE_VAR_12345").is_err(),
            "rogue env var should have been stripped"
        );
    }

    #[test]
    fn canonicalize_strips_null() {
        let guard = EnvVarAllowlist::default();
        let result = guard.canonicalize_value("SOME_VAR", "val\0ue");
        assert_eq!(result, "value");
    }

    #[test]
    fn canonicalize_path_deduplicates() {
        let guard = EnvVarAllowlist::default();
        let result = guard.canonicalize_value("PATH", "/usr/bin::/usr/local/bin:");
        assert_eq!(result, "/usr/bin:/usr/local/bin");
    }
}
