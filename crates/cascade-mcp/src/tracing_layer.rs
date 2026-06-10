//! McpTracingLayer — tracing-subscriber bridge for MCP log notifications.
//!
//! ## Purpose
//! Bridges the daemon's `tracing` instrumentation to MCP clients. When
//! installed, each tracing event is converted to a `notifications/message`
//! payload and broadcast on the `NotificationBus`. Events below the
//! MCP-configured log level (held in a `tokio::sync::watch::Receiver`) are
//! silently dropped before any allocation or serialisation.
//!
//! ## Inputs
//! - `tracing` events emitted by any span on the current subscriber stack.
//! - `watch::Receiver<LoggingLevel>` — current MCP minimum level, updated by
//!   `logging/setLevel` via the `LoggingHandler`.
//!
//! ## Outputs
//! - `JsonRpcNotification<LoggingMessageNotification>` broadcast to
//!   `NotificationBus` for each event at or above the minimum level.
//!   Forwarding is **best-effort**: if the bus channel is full the notification
//!   is dropped (never blocks the calling thread).
//!
//! ## Constraints
//! - Secret-pattern redaction: values matching `REDACT_PATTERNS` are replaced
//!   with `"[REDACTED]"` before serialisation.
//! - Rate cap: at most `RATE_CAP_PER_SECOND` notifications per second across
//!   all levels combined. Excess events are dropped, not queued.
//! - Level mapping: `TRACE` collapses to `Debug` (MCP has no Trace level).
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-mcp — tracing_layer.rs

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use serde_json::Value;
use tokio::sync::watch;
use tracing::field::{Field, Visit};
use tracing::{Event, Subscriber};
use tracing_subscriber::layer::Context;
use tracing_subscriber::Layer;

use cascade_types::mcp::{JsonRpcNotification, LoggingLevel, LoggingMessageNotification};

use crate::notification::NotificationBus;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum notifications forwarded per second (rate cap).
/// Excess events are dropped, never queued.
pub const RATE_CAP_PER_SECOND: u64 = 100;

/// Field names whose values are unconditionally redacted.
const REDACT_FIELDS: &[&str] = &[
    "password",
    "secret",
    "token",
    "api_key",
    "apikey",
    "authorization",
    "credential",
    "private_key",
    "access_token",
    "refresh_token",
];

// ── Level mapping ─────────────────────────────────────────────────────────────

/// Map a `tracing::Level` to the MCP `LoggingLevel` (RFC-5424 order).
/// `TRACE` collapses to `Debug` (MCP has no Trace level per spec).
fn tracing_to_mcp(level: &tracing::Level) -> LoggingLevel {
    match *level {
        tracing::Level::ERROR => LoggingLevel::Error,
        tracing::Level::WARN => LoggingLevel::Warning,
        tracing::Level::INFO => LoggingLevel::Info,
        tracing::Level::DEBUG | tracing::Level::TRACE => LoggingLevel::Debug,
    }
}

/// RFC-5424 numeric severity — lower = more severe.
/// Used for the level gate comparison.
fn level_severity(level: &LoggingLevel) -> u8 {
    match level {
        LoggingLevel::Emergency => 0,
        LoggingLevel::Alert => 1,
        LoggingLevel::Critical => 2,
        LoggingLevel::Error => 3,
        LoggingLevel::Warning => 4,
        LoggingLevel::Notice => 5,
        LoggingLevel::Info => 6,
        LoggingLevel::Debug => 7,
    }
}

/// Returns `true` if `event_level` is at or above `min_level` (i.e. should be
/// emitted). In RFC-5424 lower severity number = more severe.
/// A client that sets min=Warning wants Warning, Error, Critical, Alert, Emergency.
pub fn should_emit(event_level: &LoggingLevel, min_level: &LoggingLevel) -> bool {
    level_severity(event_level) <= level_severity(min_level)
}

// ── Rate limiter ──────────────────────────────────────────────────────────────

/// Simple token-bucket rate limiter using atomic operations only.
/// Shared across all layers on the same bus.
pub struct RateLimiter {
    /// Unix-epoch second of the current window.
    window_sec: AtomicU64,
    /// Count of notifications emitted in the current window.
    count: AtomicU64,
    /// Maximum notifications per second.
    cap: u64,
}

impl RateLimiter {
    pub fn new(cap: u64) -> Self {
        Self {
            window_sec: AtomicU64::new(0),
            count: AtomicU64::new(0),
            cap,
        }
    }

    /// Returns `true` if this notification may proceed; `false` if it should
    /// be dropped. Uses a 1-second sliding window.
    pub fn allow(&self) -> bool {
        // Approximate epoch-seconds using a monotonic tick (no system clock needed).
        // We start from a fixed reference so unit tests stay deterministic.
        static REF: std::sync::OnceLock<Instant> = std::sync::OnceLock::new();
        let ref_instant = REF.get_or_init(Instant::now);
        let now_sec = ref_instant.elapsed().as_secs();

        let prev = self.window_sec.load(Ordering::Relaxed);
        if now_sec != prev {
            // New second — reset window. Relaxed CAS is fine (we only need
            // approximate rate limiting, not strict consistency).
            if self
                .window_sec
                .compare_exchange(prev, now_sec, Ordering::Relaxed, Ordering::Relaxed)
                .is_ok()
            {
                self.count.store(1, Ordering::Relaxed);
                return true;
            }
        }

        let prev_count = self.count.fetch_add(1, Ordering::Relaxed);
        prev_count < self.cap
    }
}

// ── Field visitor ─────────────────────────────────────────────────────────────

/// Collects tracing event fields into a JSON object, redacting sensitive keys.
struct FieldVisitor {
    message: Option<String>,
    fields: serde_json::Map<String, Value>,
}

impl FieldVisitor {
    fn new() -> Self {
        Self {
            message: None,
            fields: serde_json::Map::new(),
        }
    }

    /// Returns `true` if a field name matches a redaction pattern
    /// (case-insensitive substring check).
    fn should_redact(name: &str) -> bool {
        let lower = name.to_lowercase();
        REDACT_FIELDS.iter().any(|p| lower.contains(p))
    }

    fn insert(&mut self, field: &Field, value: Value) {
        let name = field.name();
        if name == "message" {
            self.message = value.as_str().map(|s| s.to_owned());
        } else if Self::should_redact(name) {
            self.fields.insert(name.to_owned(), Value::String("[REDACTED]".into()));
        } else {
            self.fields.insert(name.to_owned(), value);
        }
    }
}

impl Visit for FieldVisitor {
    fn record_str(&mut self, field: &Field, value: &str) {
        if field.name() == "message" {
            if Self::should_redact("message") {
                self.message = Some("[REDACTED]".into());
            } else {
                self.message = Some(value.to_owned());
            }
        } else if Self::should_redact(field.name()) {
            self.fields
                .insert(field.name().to_owned(), Value::String("[REDACTED]".into()));
        } else {
            self.fields
                .insert(field.name().to_owned(), Value::String(value.to_owned()));
        }
    }

    fn record_debug(&mut self, field: &Field, value: &dyn std::fmt::Debug) {
        self.insert(field, Value::String(format!("{value:?}")));
    }

    fn record_i64(&mut self, field: &Field, value: i64) {
        self.insert(field, Value::Number(value.into()));
    }

    fn record_u64(&mut self, field: &Field, value: u64) {
        let v = serde_json::Number::from(value);
        self.insert(field, Value::Number(v));
    }

    fn record_f64(&mut self, field: &Field, value: f64) {
        let v = serde_json::Number::from_f64(value).unwrap_or(serde_json::Number::from(0));
        self.insert(field, Value::Number(v));
    }

    fn record_bool(&mut self, field: &Field, value: bool) {
        self.insert(field, Value::Bool(value));
    }

    fn record_error(&mut self, field: &Field, value: &(dyn std::error::Error + 'static)) {
        self.insert(field, Value::String(value.to_string()));
    }
}

// ── McpTracingLayer ───────────────────────────────────────────────────────────

/// A `tracing_subscriber::Layer` that forwards tracing events as MCP
/// `notifications/message` to a `NotificationBus`.
///
/// # Usage
/// ```rust,ignore
/// use tracing_subscriber::{Registry, prelude::*};
/// use cascade_mcp::tracing_layer::McpTracingLayer;
/// use cascade_mcp::notification::NotificationBus;
///
/// let bus = NotificationBus::new();
/// let (_, level_rx) = tokio::sync::watch::channel(cascade_types::mcp::LoggingLevel::Warning);
/// let layer = McpTracingLayer::new(bus, level_rx);
/// let subscriber = Registry::default().with(layer);
/// tracing::subscriber::set_global_default(subscriber).unwrap();
/// ```
pub struct McpTracingLayer {
    bus: NotificationBus,
    level_rx: watch::Receiver<LoggingLevel>,
    rate: Arc<RateLimiter>,
}

impl McpTracingLayer {
    /// Create a new layer.
    ///
    /// - `bus` — notification bus; notifications are broadcast here.
    /// - `level_rx` — watch receiver updated by `LoggingHandler::set_level`.
    pub fn new(bus: NotificationBus, level_rx: watch::Receiver<LoggingLevel>) -> Self {
        Self {
            bus,
            level_rx,
            rate: Arc::new(RateLimiter::new(RATE_CAP_PER_SECOND)),
        }
    }

    /// Create with a custom rate cap (useful for testing).
    pub fn with_rate_cap(
        bus: NotificationBus,
        level_rx: watch::Receiver<LoggingLevel>,
        cap: u64,
    ) -> Self {
        Self {
            bus,
            level_rx,
            rate: Arc::new(RateLimiter::new(cap)),
        }
    }
}

impl<S: Subscriber> Layer<S> for McpTracingLayer {
    fn on_event(&self, event: &Event<'_>, _ctx: Context<'_, S>) {
        let mcp_level = tracing_to_mcp(event.metadata().level());

        // Level gate — read current minimum from watch channel (non-blocking).
        let min_level = self.level_rx.borrow().clone();
        if !should_emit(&mcp_level, &min_level) {
            return;
        }

        // Rate cap.
        if !self.rate.allow() {
            return;
        }

        // Collect fields.
        let mut visitor = FieldVisitor::new();
        event.record(&mut visitor);

        let data = if visitor.fields.is_empty() {
            // Simple string payload when there are no extra fields.
            let msg = visitor
                .message
                .unwrap_or_else(|| event.metadata().name().to_owned());
            Value::String(msg)
        } else {
            let mut obj = visitor.fields;
            if let Some(msg) = visitor.message {
                obj.insert("message".into(), Value::String(msg));
            }
            Value::Object(obj)
        };

        let notification = LoggingMessageNotification {
            level: mcp_level,
            logger: Some(event.metadata().target().to_owned()),
            data,
        };

        // Serialise notification to Value so NotificationBus can broadcast it.
        let notif_value = match serde_json::to_value(&notification) {
            Ok(v) => v,
            Err(_) => return,
        };

        let notif: JsonRpcNotification<Value> =
            JsonRpcNotification::new("notifications/message", Some(notif_value));

        // Best-effort: drop if channel is full (broadcast always succeeds but
        // drops lagged receivers).
        self.bus.broadcast(notif);
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    use std::time::Duration;

    use tokio::sync::watch;
    use tracing_subscriber::prelude::*;

    use cascade_types::mcp::LoggingLevel;

    use crate::notification::NotificationBus;

    // ── Level gate matrix ─────────────────────────────────────────────────

    #[test]
    fn level_gate_matrix() {
        // min=Warning: only Warning and more severe pass
        assert!(should_emit(&LoggingLevel::Warning, &LoggingLevel::Warning));
        assert!(should_emit(&LoggingLevel::Error, &LoggingLevel::Warning));
        assert!(should_emit(&LoggingLevel::Critical, &LoggingLevel::Warning));
        assert!(should_emit(&LoggingLevel::Alert, &LoggingLevel::Warning));
        assert!(should_emit(&LoggingLevel::Emergency, &LoggingLevel::Warning));
        assert!(!should_emit(&LoggingLevel::Notice, &LoggingLevel::Warning));
        assert!(!should_emit(&LoggingLevel::Info, &LoggingLevel::Warning));
        assert!(!should_emit(&LoggingLevel::Debug, &LoggingLevel::Warning));

        // min=Debug: everything passes
        assert!(should_emit(&LoggingLevel::Debug, &LoggingLevel::Debug));
        assert!(should_emit(&LoggingLevel::Info, &LoggingLevel::Debug));
        assert!(should_emit(&LoggingLevel::Emergency, &LoggingLevel::Debug));

        // min=Emergency: only Emergency passes
        assert!(should_emit(&LoggingLevel::Emergency, &LoggingLevel::Emergency));
        assert!(!should_emit(&LoggingLevel::Alert, &LoggingLevel::Emergency));
        assert!(!should_emit(&LoggingLevel::Debug, &LoggingLevel::Emergency));
    }

    // ── setLevel changes what the layer emits ─────────────────────────────

    #[tokio::test]
    async fn set_level_gates_events() {
        let bus = NotificationBus::new();
        let mut rx = bus.subscribe();

        let (tx, level_rx) = watch::channel(LoggingLevel::Warning);
        let layer = McpTracingLayer::new(bus, level_rx);

        let subscriber = tracing_subscriber::registry().with(layer);
        let _guard = tracing::subscriber::set_default(subscriber);

        // Info event with min=Warning → dropped
        tracing::info!("should be dropped");

        // No message should arrive.
        assert!(rx.try_recv().is_err(), "info should be dropped at Warning level");

        // Lower threshold to Info
        tx.send(LoggingLevel::Info).unwrap();

        // Small yield so the watch receiver is refreshed
        tokio::time::sleep(Duration::from_millis(1)).await;

        tracing::info!("should pass now");

        let msg = rx.recv().await.expect("info event should arrive");
        assert_eq!(msg["method"], "notifications/message");
        let params = &msg["params"];
        assert_eq!(params["level"], "info");
    }

    // ── Redaction ─────────────────────────────────────────────────────────

    #[tokio::test]
    async fn secret_fields_are_redacted() {
        let bus = NotificationBus::new();
        let mut rx = bus.subscribe();

        let (_, level_rx) = watch::channel(LoggingLevel::Debug);
        let layer = McpTracingLayer::new(bus, level_rx);

        let subscriber = tracing_subscriber::registry().with(layer);
        let _guard = tracing::subscriber::set_default(subscriber);

        tracing::debug!(password = "hunter2", api_key = "sk-secret-123", "login attempt");

        let msg = rx.recv().await.expect("debug event should arrive");
        let data = &msg["params"]["data"];

        assert_eq!(data["password"], "[REDACTED]", "password must be redacted");
        assert_eq!(data["api_key"], "[REDACTED]", "api_key must be redacted");
        // message text itself is not redacted
        assert_eq!(data["message"], "login attempt");
    }

    // ── Rate cap ──────────────────────────────────────────────────────────

    #[tokio::test]
    async fn rate_cap_drops_excess_events() {
        let cap = 5u64;

        let bus = NotificationBus::new();
        let mut rx = bus.subscribe();

        let (_, level_rx) = watch::channel(LoggingLevel::Debug);
        let layer = McpTracingLayer::with_rate_cap(bus, level_rx, cap);

        let subscriber = tracing_subscriber::registry().with(layer);
        let _guard = tracing::subscriber::set_default(subscriber);

        // Emit cap + 5 events in the same second
        for i in 0..(cap + 5) {
            tracing::debug!("event {}", i);
        }

        // Drain the channel — we should get at most `cap` messages.
        let mut count = 0u64;
        while rx.try_recv().is_ok() {
            count += 1;
        }

        assert!(
            count <= cap,
            "expected at most {cap} messages, got {count}"
        );
        // And at least some messages got through.
        assert!(count > 0, "expected at least one message through rate cap");
    }

    // ── Level mapping covers all tracing levels ───────────────────────────

    #[test]
    fn tracing_level_mapping() {
        use tracing::Level;
        assert_eq!(tracing_to_mcp(&Level::ERROR), LoggingLevel::Error);
        assert_eq!(tracing_to_mcp(&Level::WARN), LoggingLevel::Warning);
        assert_eq!(tracing_to_mcp(&Level::INFO), LoggingLevel::Info);
        assert_eq!(tracing_to_mcp(&Level::DEBUG), LoggingLevel::Debug);
        assert_eq!(tracing_to_mcp(&Level::TRACE), LoggingLevel::Debug);
    }

    // ── Plain string payload when no extra fields ─────────────────────────

    #[tokio::test]
    async fn plain_message_serialised_as_string() {
        let bus = NotificationBus::new();
        let mut rx = bus.subscribe();

        let (_, level_rx) = watch::channel(LoggingLevel::Debug);
        let layer = McpTracingLayer::new(bus, level_rx);

        let subscriber = tracing_subscriber::registry().with(layer);
        let _guard = tracing::subscriber::set_default(subscriber);

        tracing::info!("hello world");

        let msg = rx.recv().await.expect("message should arrive");
        let data = &msg["params"]["data"];
        assert!(data.is_string(), "plain message with no fields should be a JSON string");
        assert_eq!(data.as_str().unwrap(), "hello world");
    }
}
