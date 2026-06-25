//! `ResourceRegistry` — handles all four MCP resource methods, plus
//! `McpHandler` wrappers and the convenience `register_resource_handlers` function.

use std::sync::Arc;

use async_trait::async_trait;
use serde_json::Value;
use tracing::debug;

use crate::error::McpServerError;
use crate::handler::McpHandler;
use crate::notification::NotificationBus;

use super::backend::{build_catalog, validate_uri_safety, FsContentBackend};
use super::subscription::SubscriptionStore;
use super::types::{ContentBackend, TextResourceContents, PAGE_SIZE};

// ── ResourceRegistry ─────────────────────────────────────────────────────────

/// Handles all four MCP resource methods for the cascade MCP server.
///
/// Holds a [`ContentBackend`] (injectable for testing) and a
/// [`SubscriptionStore`]. Use [`ResourceRegistry::new`] for production
/// (filesystem backend) or [`ResourceRegistry::with_backend`] for tests.
pub struct ResourceRegistry {
    pub(super) backend: Arc<dyn ContentBackend>,
    pub(super) subscriptions: SubscriptionStore,
    pub(super) _bus: Option<NotificationBus>,
}

impl ResourceRegistry {
    /// Create a production registry backed by the filesystem.
    pub fn new() -> Self {
        Self {
            backend: Arc::new(FsContentBackend),
            subscriptions: SubscriptionStore::new(),
            _bus: None,
        }
    }

    /// Create a registry with an injected backend (for tests or cascade-core integration).
    pub fn with_backend(backend: Arc<dyn ContentBackend>) -> Self {
        Self {
            backend,
            subscriptions: SubscriptionStore::new(),
            _bus: None,
        }
    }

    /// Attach a notification bus.
    pub fn with_notification_bus(mut self, bus: NotificationBus) -> Self {
        self._bus = Some(bus);
        self
    }

    /// Return a reference to the subscription store.
    pub fn subscriptions(&self) -> &SubscriptionStore {
        &self.subscriptions
    }

    // ── resources/list ──────────────────────────────────────────────────────

    /// Handle `resources/list`.
    pub async fn list(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        debug!("resources/list");

        let cursor_val = params
            .and_then(|p| p.get("cursor"))
            .and_then(|c| c.as_str());

        let offset: usize = cursor_val.and_then(|c| c.parse().ok()).unwrap_or(0);

        let catalog = build_catalog();
        let total = catalog.len();
        let page: Vec<&super::types::McpResource> =
            catalog.iter().skip(offset).take(PAGE_SIZE).collect();
        let next_offset = offset + PAGE_SIZE;

        let mut result = serde_json::json!({
            "resources": page,
        });
        if next_offset < total {
            result["nextCursor"] = Value::String(next_offset.to_string());
        }
        Ok(result)
    }

    // ── resources/read ───────────────────────────────────────────────────────

    /// Handle `resources/read`.
    pub async fn read(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        let uri = extract_uri(params)?;
        debug!(uri, "resources/read");

        let mime_type = if uri.starts_with("cascade://inbox/")
            || uri == "cascade://project_state"
            || uri == "cascade://quota_state"
            || uri == "cascade://pbd/current"
        {
            "application/json"
        } else if uri.starts_with("cascade://pbd/") {
            "text/yaml"
        } else {
            "text/markdown"
        };

        let text = self.backend.read_uri(uri).await?.unwrap_or_default();
        let contents = TextResourceContents {
            uri: uri.to_string(),
            mime_type: mime_type.to_string(),
            text,
        };

        Ok(serde_json::json!({ "contents": [contents] }))
    }

    // ── resources/subscribe ──────────────────────────────────────────────────

    /// Handle `resources/subscribe`.
    pub async fn subscribe(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        let uri = extract_uri(params)?;
        let connection_id = params
            .and_then(|p| p.get("connectionId"))
            .and_then(|v| v.as_str())
            .unwrap_or("default");

        debug!(uri, connection_id, "resources/subscribe");
        validate_uri_safety(uri)?;
        self.subscriptions.subscribe(connection_id, uri).await;

        Ok(serde_json::json!({}))
    }

    // ── resources/unsubscribe ────────────────────────────────────────────────

    /// Handle `resources/unsubscribe`.
    pub async fn unsubscribe(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        let uri = extract_uri(params)?;
        let connection_id = params
            .and_then(|p| p.get("connectionId"))
            .and_then(|v| v.as_str())
            .unwrap_or("default");

        debug!(uri, connection_id, "resources/unsubscribe");
        self.subscriptions.unsubscribe(connection_id, uri).await;

        Ok(serde_json::json!({}))
    }
}

impl Default for ResourceRegistry {
    fn default() -> Self {
        Self::new()
    }
}

// ── McpHandler wrappers ───────────────────────────────────────────────────────

/// `McpHandler` wrapper for `resources/list`.
pub struct ResourcesListHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesListHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.list(params.as_ref()).await
    }
}

/// `McpHandler` wrapper for `resources/read`.
pub struct ResourcesReadHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesReadHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.read(params.as_ref()).await
    }
}

/// `McpHandler` wrapper for `resources/subscribe`.
pub struct ResourcesSubscribeHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesSubscribeHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.subscribe(params.as_ref()).await
    }
}

/// `McpHandler` wrapper for `resources/unsubscribe`.
pub struct ResourcesUnsubscribeHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesUnsubscribeHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.unsubscribe(params.as_ref()).await
    }
}

// ── Convenience: register all four handlers ───────────────────────────────────

/// Register all four resource handlers into the given [`crate::handler::HandlerRegistry`].
pub async fn register_resource_handlers(
    registry: &crate::handler::HandlerRegistry,
    resources: Arc<ResourceRegistry>,
) {
    registry
        .register(
            "resources/list",
            ResourcesListHandler(Arc::clone(&resources)),
        )
        .await;
    registry
        .register(
            "resources/read",
            ResourcesReadHandler(Arc::clone(&resources)),
        )
        .await;
    registry
        .register(
            "resources/subscribe",
            ResourcesSubscribeHandler(Arc::clone(&resources)),
        )
        .await;
    registry
        .register(
            "resources/unsubscribe",
            ResourcesUnsubscribeHandler(Arc::clone(&resources)),
        )
        .await;
}

// ── Shared helper ─────────────────────────────────────────────────────────────

/// Extract `"uri"` from JSON params.
pub(super) fn extract_uri(params: Option<&Value>) -> Result<&str, McpServerError> {
    params
        .and_then(|p| p.get("uri"))
        .and_then(|v| v.as_str())
        .ok_or_else(|| McpServerError::InvalidParams {
            detail: "missing or non-string 'uri' field in params".into(),
        })
}
