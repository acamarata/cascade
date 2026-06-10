//! MCP notification bus — server-initiated notification fan-out.
//!
//! ## Purpose
//! Decouples the server core from transport details for outbound server
//! notifications (resource list changed, tool list changed, etc.).
//! Transports subscribe to the bus and receive notifications asynchronously.
//!
//! ## Inputs
//! - `broadcast(notification)` — called by primitive handlers when state changes.
//!
//! ## Outputs
//! - Each `subscribe()` call returns a `tokio::sync::broadcast::Receiver` that
//!   yields [`serde_json::Value`] payloads for each emitted notification.
//!
//! ## Constraints
//! - Channel capacity: 256. Slow receivers that fall behind get
//!   `RecvError::Lagged` — they should re-sync by re-listing primitives.
//! - All methods are infallible; a full channel logs a warning and drops the
//!   notification rather than blocking.
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-mcp — notification.rs

use serde_json::Value;
use tokio::sync::broadcast;
use tracing::warn;

use cascade_types::mcp::JsonRpcNotification;

/// Broadcast channel capacity for server-initiated notifications.
const NOTIFICATION_BUS_CAPACITY: usize = 256;

/// Server-initiated notification bus.
///
/// Clone-cheap: all clones share the same underlying broadcast channel.
#[derive(Clone, Debug)]
pub struct NotificationBus {
    tx: broadcast::Sender<Value>,
}

impl NotificationBus {
    /// Create a new bus with a 256-message buffer.
    pub fn new() -> Self {
        let (tx, _) = broadcast::channel(NOTIFICATION_BUS_CAPACITY);
        Self { tx }
    }

    /// Subscribe to the bus.
    ///
    /// Returns a `Receiver` that yields serialized notification payloads. Each
    /// transport connection should call this once and poll the receiver in its
    /// write loop.
    pub fn subscribe(&self) -> broadcast::Receiver<Value> {
        self.tx.subscribe()
    }

    /// Broadcast a notification to all active subscribers.
    ///
    /// Serializes the notification to a JSON `Value` and sends it. If the
    /// channel is full, the oldest message is overwritten and a warning is
    /// logged.
    ///
    /// Returns the number of active subscribers that received the message,
    /// or `0` if there are none.
    pub fn broadcast(&self, notification: JsonRpcNotification<Value>) -> usize {
        match serde_json::to_value(&notification) {
            Ok(value) => self.tx.send(value).unwrap_or_default(),
            Err(e) => {
                warn!(error = %e, method = %notification.method, "Failed to serialize notification");
                0
            }
        }
    }

    /// Returns the number of active subscribers.
    pub fn receiver_count(&self) -> usize {
        self.tx.receiver_count()
    }
}

impl Default for NotificationBus {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn notification_bus_delivers_to_subscriber() {
        let bus = NotificationBus::new();
        let mut rx = bus.subscribe();

        let notif =
            JsonRpcNotification::<Value>::new("notifications/tools/list_changed", Some(serde_json::json!({})));
        let count = bus.broadcast(notif);
        assert_eq!(count, 1);

        let received = rx.recv().await.unwrap();
        assert_eq!(received["method"], "notifications/tools/list_changed");
    }

    #[tokio::test]
    async fn notification_bus_no_receivers_returns_zero() {
        let bus = NotificationBus::new();
        // No subscriber — should return 0, not panic.
        let notif = JsonRpcNotification::<Value>::new("notifications/ping", None);
        let count = bus.broadcast(notif);
        assert_eq!(count, 0);
    }

    #[tokio::test]
    async fn notification_bus_multiple_subscribers() {
        let bus = NotificationBus::new();
        let mut rx1 = bus.subscribe();
        let mut rx2 = bus.subscribe();

        let notif =
            JsonRpcNotification::<Value>::new("notifications/resources/list_changed", Some(serde_json::json!({})));
        let count = bus.broadcast(notif);
        assert_eq!(count, 2);

        rx1.recv().await.unwrap();
        rx2.recv().await.unwrap();
    }
}
