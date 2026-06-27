//! Plugin capability audit log — append-only JSONL ring.
//!
//! Purpose: log every capability use or denial to ~/.cascade/logs/plugin-audit.jsonl.
//! Inputs: plugin id, capability, path (optional), outcome (use/deny).
//! Outputs: AuditEntry appended to JSONL file.
//! Constraints: append-only; no rotation in v0.1.0; fire-and-forget (log errors printed,
//!   not propagated — auditing must not block plugin execution).
//! SPORT: cascade-plugins / audit log (plug-01)

use std::path::PathBuf;

use chrono::Utc;
use serde::{Deserialize, Serialize};

use crate::capability::Capability;

/// Audit log outcome.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuditOutcome {
    /// Capability was successfully used.
    Used,
    /// Capability was denied.
    Denied,
}

/// A single audit log entry.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuditEntry {
    pub timestamp: String,
    pub plugin_id: String,
    pub capability: String,
    pub path: Option<String>,
    pub outcome: AuditOutcome,
    pub reason: Option<String>,
}

/// Append one capability event to the audit log.
///
/// Fire-and-forget: any I/O error is logged via `tracing::warn` and ignored.
pub fn log_capability_event(
    plugin_id: &str,
    cap: &Capability,
    path: Option<&std::path::Path>,
    outcome: AuditOutcome,
    reason: Option<&str>,
) {
    let entry = AuditEntry {
        timestamp: Utc::now().to_rfc3339(),
        plugin_id: plugin_id.to_owned(),
        capability: cap_name(cap).to_owned(),
        path: path.map(|p| p.display().to_string()),
        outcome,
        reason: reason.map(str::to_owned),
    };

    if let Err(e) = append_entry(&entry) {
        tracing::warn!("plugin audit log write failed: {e}");
    }
}

fn append_entry(entry: &AuditEntry) -> std::io::Result<()> {
    let log_path = audit_log_path();
    if let Some(parent) = log_path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let mut line = serde_json::to_string(entry).map_err(std::io::Error::other)?;
    line.push('\n');
    use std::io::Write;
    let mut file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)?;
    file.write_all(line.as_bytes())
}

fn audit_log_path() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".cascade")
        .join("logs")
        .join("plugin-audit.jsonl")
}

fn cap_name(cap: &Capability) -> &'static str {
    match cap {
        Capability::FsRead       => "fs_read",
        Capability::FsWrite      => "fs_write",
        Capability::FsExec       => "fs_exec",
        Capability::NetOutbound  => "net_outbound",
        Capability::NetListen    => "net_listen",
        Capability::IpcCascade   => "ipc_cascade",
        Capability::PersonalData => "personal_data",
        Capability::McpInvoke    => "mcp_invoke",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn log_capability_event_does_not_panic() {
        // Fire-and-forget — just ensure it does not panic.
        // The real log path (~/.cascade/logs/) may or may not be writable in CI.
        log_capability_event(
            "com.ex.p",
            &Capability::FsRead,
            Some(std::path::Path::new("/tmp/test")),
            AuditOutcome::Denied,
            Some("no PersonalData grant"),
        );
    }
}
