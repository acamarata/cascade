//! MCP Logging — forward structured log messages to the MCP client.
//!
//! The MCP 2025-03 spec allows the server to send log messages to the client
//! via `notifications/message`. Clients that support logging (Claude Desktop,
//! VS Code / Continue) surface these in their debug console.
//!
//! ## Log levels
//!
//! MCP defines 8 levels matching syslog severity:
//! `debug`, `info`, `notice`, `warning`, `error`, `critical`, `alert`, `emergency`.
//!
//! The default level is `warning` (only warning and above are forwarded).
//! Clients can lower the threshold via `logging/setLevel`.
//!
//! ## Integration with `tracing`
//!
//! `McpLogger` implements a thin bridge: the MCP server's `tracing` spans
//! are forwarded through this module when the MCP log level is `debug` or
//! lower. This is opt-in — enabling debug MCP logging on a production daemon
//! is noisy and should be used only for diagnostics.

use std::sync::atomic::{AtomicU8, Ordering};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

// ── Log level ─────────────────────────────────────────────────────────────────

/// MCP log severity levels (syslog order — lower is more severe).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum LogLevel {
    Emergency = 0,
    Alert = 1,
    Critical = 2,
    Error = 3,
    Warning = 4,
    Notice = 5,
    Info = 6,
    Debug = 7,
}

impl LogLevel {
    fn from_str(s: &str) -> Option<Self> {
        match s {
            "emergency" => Some(Self::Emergency),
            "alert" => Some(Self::Alert),
            "critical" => Some(Self::Critical),
            "error" => Some(Self::Error),
            "warning" => Some(Self::Warning),
            "notice" => Some(Self::Notice),
            "info" => Some(Self::Info),
            "debug" => Some(Self::Debug),
            _ => None,
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::Emergency => "emergency",
            Self::Alert => "alert",
            Self::Critical => "critical",
            Self::Error => "error",
            Self::Warning => "warning",
            Self::Notice => "notice",
            Self::Info => "info",
            Self::Debug => "debug",
        }
    }
}

// ── Log message notification ──────────────────────────────────────────────────

/// A log notification sent to the client via `notifications/message`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct McpLogMessage {
    pub level: LogLevel,
    /// Optional logger name (e.g. `"cascade-rag"`, `"cascade-mcp"`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub logger: Option<String>,
    /// The log data — may be a string or a JSON object.
    pub data: Value,
}

// ── McpLogger ─────────────────────────────────────────────────────────────────

/// Manages the MCP log level gate and log message emission.
///
/// Held by `McpServer`. Other subsystems call `McpLogger::log` to emit
/// a message; `McpLogger` checks the current level gate and discards
/// messages below the threshold.
///
/// The transport write is done by the server loop (which owns both the
/// logger and the transport); `McpLogger` produces the serialized
/// notification string.
pub struct McpLogger {
    /// Current minimum level (atomic so it can be updated from any task).
    /// Stored as u8 matching `LogLevel` discriminants.
    min_level: AtomicU8,
}

impl McpLogger {
    pub fn new() -> Self {
        Self {
            min_level: AtomicU8::new(LogLevel::Warning as u8),
        }
    }

    /// Current log level.
    pub fn level(&self) -> LogLevel {
        match self.min_level.load(Ordering::Relaxed) {
            0 => LogLevel::Emergency,
            1 => LogLevel::Alert,
            2 => LogLevel::Critical,
            3 => LogLevel::Error,
            4 => LogLevel::Warning,
            5 => LogLevel::Notice,
            6 => LogLevel::Info,
            _ => LogLevel::Debug,
        }
    }

    /// Handle `logging/setLevel` from the client.
    ///
    /// Params: `{ "level": "debug" }`
    pub fn set_level(&self, params: &Value) -> Result<Value> {
        let level_str = params
            .get("level")
            .and_then(|v| v.as_str())
            .ok_or_else(|| CascadeError::ConfigParse {
                path: "<logging-params>".into(),
                detail: "missing 'level'".into(),
            })?;

        let level = LogLevel::from_str(level_str).ok_or_else(|| CascadeError::ConfigParse {
            path: "<logging-params>".into(),
            detail: format!("unknown level: {level_str}"),
        })?;

        self.min_level.store(level as u8, Ordering::Relaxed);
        debug!(level = level.as_str(), "MCP log level updated");

        Ok(serde_json::json!({}))
    }

    /// Check whether a message at `level` should be forwarded.
    pub fn should_emit(&self, level: LogLevel) -> bool {
        level as u8 <= self.min_level.load(Ordering::Relaxed)
    }

    /// Serialize a log message as a `notifications/message` JSON string.
    ///
    /// The server loop calls this and writes the string to the transport.
    /// Returns `None` if the level is below the current threshold.
    pub fn format_notification(&self, msg: McpLogMessage) -> Option<String> {
        if !self.should_emit(msg.level) {
            return None;
        }

        let notification = serde_json::json!({
            "jsonrpc": "2.0",
            "method": "notifications/message",
            "params": msg,
        });

        serde_json::to_string(&notification).ok()
    }
}

impl Default for McpLogger {
    fn default() -> Self {
        Self::new()
    }
}
