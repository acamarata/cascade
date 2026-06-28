//! audit — process-global audit-log accessor for privileged daemon operations.
//!
//! Purpose: provide a single tamper-evident audit log (cascade-audit crate) that
//!   every privileged daemon operation can append to without threading an
//!   `Arc<Mutex<AuditLog>>` through every handler's state. A process-global
//!   `OnceLock<Mutex<AuditLog>>` is sufficient for P2: the daemon is one process,
//!   appends are infrequent (start/stop and future write/link/resolve ops), and
//!   the lock is never held across an `.await`.
//! Inputs: an audit-log path (`~/.cascade/audit.log`) at init; per-event
//!   (op, actor, target, detail) tuples at each call site.
//! Outputs: appended hash-chained records on disk.
//! Constraints: `record()` is NON-FATAL — any failure (uninitialised log, I/O
//!   error, poisoned mutex) is logged at ERROR and swallowed so the underlying
//!   operation always proceeds. The lock is acquired and released synchronously
//!   inside `record()`; callers must not hold any audit guard across `.await`.
//! SPORT: MASTER-SECURITY.md — audit-log-daemon-instrumentation.
//!
//! NOTE (P2 scope): of the six privileged op types specified for E-07
//! (daemon_start, daemon_stop, gci_write, key_rotation, symlink_create,
//! symlink_delete, cascade_resolve), only daemon_start/daemon_stop correspond to
//! operations the daemon performs today. The write/link/resolve/rotation ops are
//! not yet daemon-side operations (they arrive with the GUI/MCP work in P3); this
//! module is the wired-ready instrumentation point for them — each new handler
//! calls `audit::record(...)` when it lands.

use std::path::Path;
use std::sync::{Mutex, OnceLock};

use cascade_audit::{AuditEntry, AuditLog, AuditOp};

/// Process-global audit log. Set once at daemon startup via [`init`].
static AUDIT: OnceLock<Mutex<AuditLog>> = OnceLock::new();
/// Path of the initialised audit log — mirrors AUDIT, set in the same call.
static AUDIT_PATH: OnceLock<std::path::PathBuf> = OnceLock::new();

/// Initialise the global audit log at `path`.
///
/// First call wins; subsequent calls are ignored (returns `Ok(())`). Opening the
/// log is the only fallible step — append failures are handled per-event by
/// [`record`].
pub fn init(path: &Path) -> Result<(), cascade_audit::AuditError> {
    let log = AuditLog::open(path)?;
    // If already initialised, drop the freshly-opened handle; first init wins.
    if AUDIT.set(Mutex::new(log)).is_ok() {
        let _ = AUDIT_PATH.set(path.to_path_buf());
    }
    Ok(())
}

/// Return the path of the initialised audit log, or `None` if not yet initialised.
/// Used by integration tests to locate the active log regardless of init order.
// Public test/IPC utility — locates the audit log path without re-init.
#[allow(dead_code)]
pub fn active_log_path() -> Option<&'static std::path::Path> {
    AUDIT_PATH.get().map(|p| p.as_path())
}

/// Append one audit event. NON-FATAL by contract.
///
/// On any failure — the log was never initialised, the append errored (e.g. disk
/// full), or the mutex was poisoned — this logs at ERROR and returns without
/// panicking or blocking. The calling privileged operation always proceeds.
pub fn record(op: AuditOp, actor: &str, target: &str, detail: &str) {
    let Some(lock) = AUDIT.get() else {
        tracing::error!(op = %op, "audit log not initialised; dropping audit event");
        return;
    };
    let entry = AuditEntry {
        actor: actor.to_string(),
        op,
        target: target.to_string(),
        detail: detail.to_string(),
    };
    match lock.lock() {
        Ok(guard) => {
            if let Err(e) = guard.append(entry) {
                tracing::error!(%e, "audit append failed (operation not blocked)");
            }
        }
        Err(e) => tracing::error!(%e, "audit mutex poisoned (operation not blocked)"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_audit::AuditOp;

    #[test]
    fn record_is_non_fatal_when_uninitialised() {
        // No init() called in this test's view of the global — record must not panic.
        // (The global may or may not be set by another test; either way this must
        // not panic and must not block.)
        record(
            AuditOp::DaemonStart,
            "cascaded",
            "cascaded",
            "test-non-fatal",
        );
    }

    #[test]
    fn open_and_append_chain_via_audit_log() {
        // Verify the underlying AuditLog the module wraps works end to end: open a
        // temp log, append a daemon_start record, and confirm the chain verifies.
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("audit.log");
        let log = AuditLog::open(&path).unwrap();
        log.append(AuditEntry {
            actor: "cascaded".into(),
            op: AuditOp::DaemonStart,
            target: "cascaded".into(),
            detail: "v0.1.0 pid=1".into(),
        })
        .unwrap();
        let violations = log.verify_chain().unwrap();
        assert!(violations.is_empty(), "fresh chain must verify clean");
    }
}
