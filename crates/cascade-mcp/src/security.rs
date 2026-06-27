//! MCP transport security — origin validation, capability gating, and audit logging.
//!
//! ## Components
//!
//! - [`validate_local_origin`] — helper that rejects requests carrying a foreign
//!   `Origin` or `Host` header (CSRF defence).
//! - [`CapabilitySet`] — bitmask of per-session capabilities; gates sensitive tools.
//! - [`tool_requires_personal_data`] — gate check for `cascade.memory.write`.
//! - [`AuditRing`] — unbounded-sender → bounded in-memory ring → async JSONL flush.
//!
//! ## Inputs / Outputs
//!
//! `validate_local_origin` reads `&HeaderMap` and returns `Ok(())` or
//! `Err(StatusCode::FORBIDDEN)`.
//! `AuditRing::record` is fire-and-forget; the background task flushes to
//! `~/.cascade/logs/mcp-access.jsonl`.
//!
//! ## Constraints
//!
//! - No circular dependencies — imports only std / tokio / tracing / chrono /
//!   serde_json; nothing else from cascade-mcp.
//! - Ring capped at 10 000 in-memory entries; older entries are evicted silently.

use std::collections::VecDeque;

use axum::http::{HeaderMap, StatusCode};
use chrono::{DateTime, Utc};
use serde::Serialize;
use tracing::{debug, warn};

// ── CapabilitySet ──────────────────────────────────────────────────────────────

/// Bitmask of capabilities granted to an MCP session.
///
/// Default is [`Standard`](CapabilitySet::Standard) (read-only tools).
/// `PersonalData` must be explicitly granted to allow `cascade.memory.write`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct CapabilitySet(pub u32);

impl CapabilitySet {
    /// Basic read-only access: search, read, inbox.list, memory.read.
    pub const STANDARD: CapabilitySet = CapabilitySet(0b0000_0001);
    /// Allows write operations on personal data (memory.write, inbox.send).
    pub const PERSONAL_DATA: CapabilitySet = CapabilitySet(0b0000_0010);
    /// Full administrative access (reserved for future use).
    pub const ADMIN: CapabilitySet = CapabilitySet(0b1000_0000);

    /// Parse from a raw `u32` (e.g. from `X-Cascade-Cap` header).
    pub fn from_bits(bits: u32) -> Self {
        Self(bits)
    }

    /// Return the raw bitmask.
    pub fn bits(self) -> u32 {
        self.0
    }

    /// Return `true` if this set contains every bit in `other`.
    pub fn contains(self, other: CapabilitySet) -> bool {
        (self.0 & other.0) == other.0
    }
}

impl Default for CapabilitySet {
    fn default() -> Self {
        Self::STANDARD
    }
}

/// Returns `true` when the named tool requires the `PersonalData` capability.
///
/// Used in HTTP/SSE handlers to gate `cascade.memory.write` before dispatch.
pub fn tool_requires_personal_data(tool_name: &str) -> bool {
    matches!(
        tool_name,
        "cascade.memory.write" | "cascade.inbox.send"
    )
}

// ── Origin / Host validation ───────────────────────────────────────────────────

/// Reject requests that carry a foreign `Origin` or `Host` header.
///
/// Allowed origins: `localhost`, `127.0.0.1`, `[::1]`, `::1`, `null`, or absent.
/// Any other value returns `Err(StatusCode::FORBIDDEN)`.
///
/// # Why
///
/// Browsers always send the `Origin` header on cross-origin POST requests, so
/// an attacker page at `https://evil.com` cannot silently reach the loopback
/// MCP server — this guard returns 403 before auth is even checked.
pub fn validate_local_origin(headers: &HeaderMap) -> Result<(), StatusCode> {
    if let Some(origin) = headers.get(axum::http::header::ORIGIN) {
        let s = origin.to_str().unwrap_or("");
        if !is_local_origin(s) {
            warn!(origin = s, "MCP: rejected foreign Origin header");
            return Err(StatusCode::FORBIDDEN);
        }
        debug!(origin = s, "MCP: Origin accepted");
    }

    if let Some(host) = headers.get(axum::http::header::HOST) {
        let s = host.to_str().unwrap_or("");
        let host_only = strip_port(s);
        if !is_local_host(host_only) {
            warn!(host = s, "MCP: rejected foreign Host header");
            return Err(StatusCode::FORBIDDEN);
        }
        debug!(host = s, "MCP: Host accepted");
    }

    Ok(())
}

/// Accept only localhost / loopback / null origin strings.
fn is_local_origin(origin: &str) -> bool {
    if origin == "null" {
        return true;
    }
    let host = strip_scheme_and_port(origin);
    is_local_host(host)
}

/// Strip `scheme://` prefix and `:port` suffix.
fn strip_scheme_and_port(s: &str) -> &str {
    let s = if let Some(pos) = s.find("://") {
        &s[pos + 3..]
    } else {
        s
    };
    strip_port(s)
}

/// Strip `:port` from a host string, respecting IPv6 `[::1]:port` form.
fn strip_port(s: &str) -> &str {
    if s.starts_with('[') {
        // IPv6 bracket form: [::1]:7722 → [::1]
        if let Some(end) = s.find(']') {
            return &s[..=end];
        }
        return s;
    }
    // IPv4 / hostname: 127.0.0.1:7722 → 127.0.0.1
    if let Some(pos) = s.rfind(':') {
        return &s[..pos];
    }
    s
}

/// Return `true` for loopback host values.
fn is_local_host(host: &str) -> bool {
    matches!(host, "localhost" | "127.0.0.1" | "[::1]" | "::1")
}

// ── AuditRing ─────────────────────────────────────────────────────────────────

/// Result of a tool call recorded in the audit log.
#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "snake_case")]
pub enum AuditResult {
    Allowed,
    Denied,
}

/// One audit log entry (serialised as a JSONL line).
#[derive(Debug, Serialize, Clone)]
pub struct AuditEntry {
    pub timestamp: DateTime<Utc>,
    pub tool: String,
    pub client_cap: u32,
    pub result: AuditResult,
    pub peer_addr: Option<String>,
}

/// Bounded in-memory ring buffer with async JSONL flush to disk.
///
/// `record` is non-blocking (fire-and-forget via an unbounded channel).
/// The background task caps the in-memory ring at [`MAX_RING`] entries and
/// appends each entry as one JSON line to `~/.cascade/logs/mcp-access.jsonl`.
#[derive(Clone, Debug)]
pub struct AuditRing {
    sender: tokio::sync::mpsc::UnboundedSender<AuditEntry>,
}

const MAX_RING: usize = 10_000;

impl AuditRing {
    /// Construct and start the background flush task.
    pub fn new() -> Self {
        let (tx, rx) = tokio::sync::mpsc::unbounded_channel::<AuditEntry>();
        tokio::spawn(audit_flush_task(rx));
        Self { sender: tx }
    }

    /// Record an audit entry. Non-blocking; never panics.
    pub fn record(&self, entry: AuditEntry) {
        // Ignore send error (receiver gone only on shutdown).
        let _ = self.sender.send(entry);
    }
}

impl Default for AuditRing {
    fn default() -> Self {
        Self::new()
    }
}

/// Background task: receive entries, maintain ring, flush to JSONL.
async fn audit_flush_task(mut rx: tokio::sync::mpsc::UnboundedReceiver<AuditEntry>) {
    let log_path = audit_log_path();
    if let Some(parent) = log_path.parent() {
        if let Err(e) = std::fs::create_dir_all(parent) {
            warn!(path = %parent.display(), error = %e, "AuditRing: cannot create log dir");
        }
    }

    let mut ring: VecDeque<AuditEntry> = VecDeque::with_capacity(MAX_RING);

    while let Some(entry) = rx.recv().await {
        if ring.len() >= MAX_RING {
            ring.pop_front();
        }
        ring.push_back(entry.clone());

        if let Ok(mut line) = serde_json::to_string(&entry) {
            line.push('\n');
            use std::io::Write as _;
            match std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&log_path)
            {
                Ok(mut f) => {
                    if let Err(e) = f.write_all(line.as_bytes()) {
                        warn!(error = %e, "AuditRing: write error");
                    }
                }
                Err(e) => {
                    warn!(path = %log_path.display(), error = %e, "AuditRing: open error");
                }
            }
        }
    }
}

/// Canonical path for the MCP access audit log.
fn audit_log_path() -> std::path::PathBuf {
    let home = std::env::var("HOME").unwrap_or_else(|_| "/tmp".to_string());
    std::path::PathBuf::from(home)
        .join(".cascade")
        .join("logs")
        .join("mcp-access.jsonl")
}

// ── Unit tests ─────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::HeaderValue;

    fn headers_with(name: &str, value: &str) -> HeaderMap {
        let mut h = HeaderMap::new();
        h.insert(
            axum::http::HeaderName::from_bytes(name.as_bytes()).unwrap(),
            HeaderValue::from_str(value).unwrap(),
        );
        h
    }

    #[test]
    fn no_headers_allowed() {
        assert!(validate_local_origin(&HeaderMap::new()).is_ok());
    }

    #[test]
    fn localhost_origin_allowed() {
        let h = headers_with("origin", "http://localhost:7722");
        assert!(validate_local_origin(&h).is_ok());
    }

    #[test]
    fn loopback_ipv4_origin_allowed() {
        let h = headers_with("origin", "http://127.0.0.1:7722");
        assert!(validate_local_origin(&h).is_ok());
    }

    #[test]
    fn null_origin_allowed() {
        let h = headers_with("origin", "null");
        assert!(validate_local_origin(&h).is_ok());
    }

    #[test]
    fn foreign_origin_rejected() {
        let h = headers_with("origin", "https://evil.com");
        assert_eq!(validate_local_origin(&h), Err(StatusCode::FORBIDDEN));
    }

    #[test]
    fn foreign_host_rejected() {
        let h = headers_with("host", "evil.com");
        assert_eq!(validate_local_origin(&h), Err(StatusCode::FORBIDDEN));
    }

    #[test]
    fn localhost_host_with_port_allowed() {
        let h = headers_with("host", "localhost:7722");
        assert!(validate_local_origin(&h).is_ok());
    }

    #[test]
    fn loopback_host_no_port_allowed() {
        let h = headers_with("host", "127.0.0.1");
        assert!(validate_local_origin(&h).is_ok());
    }

    #[test]
    fn capability_contains() {
        let cap = CapabilitySet(CapabilitySet::STANDARD.0 | CapabilitySet::PERSONAL_DATA.0);
        assert!(cap.contains(CapabilitySet::PERSONAL_DATA));
        assert!(!CapabilitySet::STANDARD.contains(CapabilitySet::PERSONAL_DATA));
    }

    #[test]
    fn tool_gate_memory_write() {
        assert!(tool_requires_personal_data("cascade.memory.write"));
        assert!(!tool_requires_personal_data("cascade.search"));
        assert!(!tool_requires_personal_data("cascade.read"));
        assert!(!tool_requires_personal_data("cascade.memory.read"));
    }
}
