//! Purpose: Dedicated HTTP module for the cascaded dashboard server (ADR-P3-002).
//!   Houses the static-file SPA mount and (in later E-02 tickets) per-group API
//!   route handlers, keeping them out of the monolithic dashboard.rs.
//! Constraints: The dashboard binds 127.0.0.1:9761 only (see dashboard.rs guard).
//!   The static dist directory is resolved at runtime from `CASCADE_DASHBOARD_DIST`
//!   (falls back to none — API-only — when unset, e.g. in dev or tests).
//! SPORT: MASTER-ENDPOINTS.md § dashboard HTTP; ADR-P3-002 (src/http/ module)

pub mod chat_handlers;
pub mod chat_history_memory;
pub mod chat_tools;
pub mod gci_handlers;
pub mod harness;
pub mod hooks_write;
pub mod personal_handlers;
pub mod projects_handlers;
pub mod rag_status;
pub mod static_handler;
pub mod usage_history;

use std::path::PathBuf;

/// Resolve the dashboard `dist/` directory from the environment.
///
/// Returns `Some(path)` when `CASCADE_DASHBOARD_DIST` is set AND the directory
/// exists on disk; `None` otherwise. A `None` result means the daemon runs the
/// API surface without serving the SPA (dev mode uses the Vite dev server, and
/// tests do not need the static mount).
pub fn dashboard_dist_dir() -> Option<PathBuf> {
    let raw = std::env::var_os("CASCADE_DASHBOARD_DIST")?;
    let path = PathBuf::from(raw);
    if path.is_dir() {
        Some(path)
    } else {
        None
    }
}
