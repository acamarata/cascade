//! MCP Logging primitive — `logging/setLevel` handler + structured log forwarding.
//!
//! ## Purpose
//! Implements the MCP logging primitive:
//! - `LoggingHandler` handles `logging/setLevel` requests from MCP clients, updating a
//!   `watch::Sender<LoggingLevel>` that the `McpTracingLayer` watches.
//! - `McpLogger` is the legacy level-gate used by `McpServer` for direct log emission
//!   (retained for back-compat; new code should use `McpTracingLayer`).
//!
//! ## Log levels
//!
//! MCP defines 8 levels matching RFC-5424 syslog severity:
//! `debug`, `info`, `notice`, `warning`, `error`, `critical`, `alert`, `emergency`.
//!
//! The default level is `warning` (only warning and above are forwarded).
//! Clients can lower the threshold via `logging/setLevel`.
//!
//! ## Integration with `tracing`
//!
//! `McpTracingLayer` (see `tracing_layer.rs`) implements a `tracing_subscriber::Layer`
//! that reads from the `watch::Receiver<LoggingLevel>` produced by `LoggingHandler::level_receiver`
//! and publishes matching events as `notifications/message` to the `NotificationBus`.
//!
//! ## SPORT
//! MASTER-MCP-PRIMITIVES.md: logging handler status Done
//! MASTER-CRATES.md: cascade-mcp — logging.rs

use std::sync::atomic::{AtomicU8, Ordering};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokio::sync::watch;
use tracing::debug;

use cascade_types::error::{CascadeError, Result};
use cascade_types::mcp::{LoggingLevel, SetLevelRequest};

// ── LoggingHandler ────────────────────────────────────────────────────────────

/// Handles `logging/setLevel` for the MCP server.
///
/// Holds a `watch::Sender<LoggingLevel>`. When the client sends
/// `logging/setLevel`, `handle_set_level` updates the sender so that
/// `McpTracingLayer` — which holds the matching `watch::Receiver` —
/// immediately sees the new threshold.
///
/// ## Usage
/// ```rust,ignore
/// use cascade_mcp::logging::LoggingHandler;
/// use cascade_mcp::tracing_layer::McpTracingLayer;
/// use cascade_mcp::notification::NotificationBus;
///
/// let bus = NotificationBus::new();
/// let handler = LoggingHandler::new();
/// let layer = McpTracingLayer::new(bus, handler.level_receiver());
/// // Install `layer` with tracing_subscriber and register `handler` with the
/// // HandlerRegistry for "logging/setLevel".
/// ```
pub struct LoggingHandler {
    tx: watch::Sender<LoggingLevel>,
    rx: watch::Receiver<LoggingLevel>,
}

impl LoggingHandler {
    /// Create with default level `Warning`.
    pub fn new() -> Self {
        let (tx, rx) = watch::channel(LoggingLevel::Warning);
        Self { tx, rx }
    }

    /// Create with an explicit initial level.
    pub fn with_level(initial: LoggingLevel) -> Self {
        let (tx, rx) = watch::channel(initial);
        Self { tx, rx }
    }

    /// Return a `watch::Receiver<LoggingLevel>` to pass to `McpTracingLayer`.
    ///
    /// Multiple clones may be created; all see the same level updates.
    pub fn level_receiver(&self) -> watch::Receiver<LoggingLevel> {
        self.rx.clone()
    }

    /// Current log level.
    pub fn level(&self) -> LoggingLevel {
        self.rx.borrow().clone()
    }

    /// Handle `logging/setLevel` — deserialise `SetLevelRequest`, update
    /// the watch channel, and return an empty JSON object `{}`.
    pub fn handle_set_level(&self, params: Option<Value>) -> Result<Value> {
        let params = params.unwrap_or(Value::Null);

        // Accept both `{ "level": "debug" }` (MCP spec) and the raw string
        // for ergonomic testing.
        let level: LoggingLevel = serde_json::from_value::<SetLevelRequest>(params.clone())
            .map(|r| r.level)
            .or_else(|_| {
                // Fallback: bare string value
                serde_json::from_value::<LoggingLevel>(params.clone())
            })
            .map_err(|_| CascadeError::ConfigParse {
                path: "<logging/setLevel>".into(),
                detail: format!("invalid SetLevelRequest: {params}"),
            })?;

        self.tx
            .send(level.clone())
            .map_err(|_| CascadeError::ConfigParse {
                path: "<logging/setLevel>".into(),
                detail: "level watch channel closed".into(),
            })?;

        debug!(level = ?level, "MCP log level updated via logging/setLevel");

        Ok(serde_json::json!({}))
    }
}

impl Default for LoggingHandler {
    fn default() -> Self {
        Self::new()
    }
}

// ── Legacy: LogLevel (kept for McpLogger) ─────────────────────────────────────

/// MCP log severity levels (syslog order — lower is more severe).
///
/// This enum mirrors `cascade_types::mcp::LoggingLevel` but is retained here
/// for `McpLogger` back-compat. New code should use `LoggingLevel` from
/// `cascade_types`.
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

// ── Legacy: log message notification ─────────────────────────────────────────

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

// ── Legacy: McpLogger ─────────────────────────────────────────────────────────

/// Manages the MCP log level gate and log message emission (legacy path).
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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::mcp::LoggingLevel;

    // ── LoggingHandler: setLevel changes receiver ─────────────────────────

    #[test]
    fn set_level_updates_receiver() {
        let handler = LoggingHandler::new();
        assert_eq!(handler.level(), LoggingLevel::Warning);

        let rx = handler.level_receiver();
        assert_eq!(*rx.borrow(), LoggingLevel::Warning);

        let params = serde_json::json!({ "level": "debug" });
        handler.handle_set_level(Some(params)).unwrap();

        assert_eq!(handler.level(), LoggingLevel::Debug);
        assert_eq!(*rx.borrow(), LoggingLevel::Debug);
    }

    #[test]
    fn set_level_all_variants() {
        let handler = LoggingHandler::new();
        let cases = [
            ("emergency", LoggingLevel::Emergency),
            ("alert", LoggingLevel::Alert),
            ("critical", LoggingLevel::Critical),
            ("error", LoggingLevel::Error),
            ("warning", LoggingLevel::Warning),
            ("notice", LoggingLevel::Notice),
            ("info", LoggingLevel::Info),
            ("debug", LoggingLevel::Debug),
        ];
        for (name, expected) in cases {
            let params = serde_json::json!({ "level": name });
            handler.handle_set_level(Some(params)).unwrap();
            assert_eq!(handler.level(), expected, "setLevel({name}) failed");
        }
    }

    #[test]
    fn set_level_invalid_returns_error() {
        let handler = LoggingHandler::new();
        let params = serde_json::json!({ "level": "nonsense" });
        let result = handler.handle_set_level(Some(params));
        assert!(result.is_err(), "invalid level should return Err");
    }

    // ── McpLogger legacy: level gate ──────────────────────────────────────

    #[test]
    fn legacy_logger_level_gate() {
        let logger = McpLogger::new();
        // Default warning — info should not emit
        assert!(!logger.should_emit(LogLevel::Info));
        assert!(logger.should_emit(LogLevel::Warning));
        assert!(logger.should_emit(LogLevel::Error));

        // Set to debug
        logger
            .set_level(&serde_json::json!({ "level": "debug" }))
            .unwrap();
        assert!(logger.should_emit(LogLevel::Debug));
        assert!(logger.should_emit(LogLevel::Info));
    }

    #[test]
    fn legacy_format_notification_gates_level() {
        let logger = McpLogger::new(); // default = Warning
        let msg = McpLogMessage {
            level: LogLevel::Info,
            logger: None,
            data: serde_json::json!("test"),
        };
        assert!(
            logger.format_notification(msg).is_none(),
            "Info below Warning should produce None"
        );

        let msg2 = McpLogMessage {
            level: LogLevel::Error,
            logger: Some("cascade-test".into()),
            data: serde_json::json!("error message"),
        };
        let notif = logger.format_notification(msg2);
        assert!(notif.is_some(), "Error at Warning threshold should emit");
        let s = notif.unwrap();
        assert!(s.contains("notifications/message"));
        assert!(s.contains("error message"));
    }
}
