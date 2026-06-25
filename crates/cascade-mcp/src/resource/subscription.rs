//! Per-connection subscription store for `resources/subscribe` / `resources/unsubscribe`.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use tokio::sync::Mutex;

/// Manages `resources/subscribe` and `resources/unsubscribe` per connection.
///
/// Connection IDs are opaque strings supplied by the transport layer.
/// Thread-safe; clone-cheap (inner `Arc<Mutex<...>>`).
#[derive(Clone, Default)]
pub struct SubscriptionStore {
    inner: Arc<Mutex<HashMap<String, HashSet<String>>>>,
}

impl SubscriptionStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// Add `uri` to `connection_id`'s subscription set.
    /// Returns `true` if the URI was newly inserted.
    pub async fn subscribe(&self, connection_id: &str, uri: &str) -> bool {
        let mut map = self.inner.lock().await;
        map.entry(connection_id.to_string())
            .or_default()
            .insert(uri.to_string())
    }

    /// Remove `uri` from `connection_id`'s subscription set.
    /// Returns `true` if the URI was present and removed.
    pub async fn unsubscribe(&self, connection_id: &str, uri: &str) -> bool {
        let mut map = self.inner.lock().await;
        if let Some(set) = map.get_mut(connection_id) {
            let removed = set.remove(uri);
            if set.is_empty() {
                map.remove(connection_id);
            }
            return removed;
        }
        false
    }

    /// Returns `true` if `connection_id` is subscribed to `uri`.
    pub async fn is_subscribed(&self, connection_id: &str, uri: &str) -> bool {
        let map = self.inner.lock().await;
        map.get(connection_id)
            .map(|set| set.contains(uri))
            .unwrap_or(false)
    }

    /// All URIs subscribed by `connection_id`.
    pub async fn subscriptions_for(&self, connection_id: &str) -> HashSet<String> {
        let map = self.inner.lock().await;
        map.get(connection_id).cloned().unwrap_or_default()
    }

    /// Remove all subscriptions for a connection (e.g. on disconnect).
    pub async fn remove_connection(&self, connection_id: &str) {
        let mut map = self.inner.lock().await;
        map.remove(connection_id);
    }
}
