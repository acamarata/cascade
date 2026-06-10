//! MCP Progress notifications — long-running operation progress tokens.
//!
//! When a tool call triggers a long-running operation (index rebuild, large
//! corpus search, embedding batch), the server emits `notifications/progress`
//! messages so the client can display a progress indicator.
//!
//! ## Wire format
//!
//! ```json
//! {
//!   "jsonrpc": "2.0",
//!   "method": "notifications/progress",
//!   "params": {
//!     "progressToken": "rebuild-abc123",
//!     "progress": 42,
//!     "total": 100
//!   }
//! }
//! ```
//!
//! ## Usage
//!
//! Tool handlers receive a `ProgressEmitter` reference. They call
//! `emitter.start(token)` at the beginning and `emitter.update(token, n, total)`
//! as work progresses. The emitter queues notifications; the server loop
//! drains the queue and writes them to the transport.
//!
//! ## Token lifecycle
//!
//! Tokens are issued by the client in the original tool call request
//! (in `_meta.progressToken`). If no token is present, progress notifications
//! are silently dropped.

use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tracing::debug;

// ── Progress types ────────────────────────────────────────────────────────────

/// A progress notification ready to send to the client.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProgressNotification {
    /// The progress token issued by the client.
    pub progress_token: String,
    /// Current progress value (0-based).
    pub progress: u64,
    /// Total steps, if known.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub total: Option<u64>,
}

impl ProgressNotification {
    /// Serialize as a `notifications/progress` JSON-RPC notification string.
    pub fn to_notification(&self) -> String {
        let notif = serde_json::json!({
            "jsonrpc": "2.0",
            "method": "notifications/progress",
            "params": self,
        });
        serde_json::to_string(&notif).unwrap_or_default()
    }
}

// ── ProgressEmitter ───────────────────────────────────────────────────────────

/// Emits MCP progress notifications for long-running operations.
///
/// The emitter holds a `mpsc::Sender`; the server loop holds the receiver
/// and writes notifications to the transport as they arrive.
///
/// Clone-cheap: the sender is Arc-wrapped.
#[derive(Clone)]
pub struct ProgressEmitter {
    tx: mpsc::Sender<ProgressNotification>,
}

impl ProgressEmitter {
    /// Create a new emitter.
    ///
    /// Returns `(emitter, receiver)`. The receiver must be consumed by the
    /// server loop; dropping it silently discards further notifications.
    pub fn new_with_channel() -> (Self, mpsc::Receiver<ProgressNotification>) {
        let (tx, rx) = mpsc::channel(256);
        (Self { tx }, rx)
    }

    /// Create an emitter that discards all notifications.
    ///
    /// Used when no client progress token is present.
    pub fn new() -> Self {
        let (tx, _) = mpsc::channel(1);
        Self { tx }
    }

    /// Emit a progress update.
    ///
    /// `progress` and `total` are in the same unit (e.g. chunks indexed).
    /// If the server loop has dropped the receiver, this is a no-op.
    pub fn update(&self, token: impl Into<String>, progress: u64, total: Option<u64>) {
        let notif = ProgressNotification {
            progress_token: token.into(),
            progress,
            total,
        };
        debug!(
            token = %notif.progress_token,
            progress = notif.progress,
            total = ?notif.total,
            "progress update"
        );
        // Non-blocking: if the channel is full, drop the notification rather
        // than blocking the calling task.
        let _ = self.tx.try_send(notif);
    }

    /// Convenience: report completion (progress == total).
    pub fn complete(&self, token: impl Into<String>, total: u64) {
        self.update(token, total, Some(total));
    }
}

impl Default for ProgressEmitter {
    fn default() -> Self {
        Self::new()
    }
}
