//! HandlerRegistry — dynamic MCP method registration.
//!
//! ## Purpose
//! Decouples the server core (lifecycle, state machine) from primitive
//! implementations (resources, tools, prompts, sampling, logging). Primitive
//! modules register themselves before the server starts; the server then
//! dispatches incoming requests through the registry.
//!
//! ## Inputs
//! - `register(method, handler)` — called at server setup time to bind a
//!   method name to an async handler.
//!
//! ## Outputs
//! - `dispatch(method, params)` — routes a request to the registered handler,
//!   returning `McpServerError::MethodNotFound` if unregistered.
//! - `capability_builder()` — introspects registered methods and derives
//!   `ServerCapabilities` by scanning for known method prefixes.
//!
//! ## Constraints
//! - Thread-safe: uses `Arc<Mutex<HashMap<...>>>` internally.
//! - Duplicate registration replaces the previous handler and logs a warning.
//! - Handler trait uses `async_trait` for object safety (Rust ≥ 1.75 RPITIT
//!   is not yet stable for dyn objects at the time of this crate's edition).
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-mcp — handler.rs

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use serde_json::Value;
use tokio::sync::Mutex;
use tracing::{debug, warn};

use cascade_types::mcp::{
    PromptsCapability, ResourcesCapability, ServerCapabilities, ToolsCapability,
};

use crate::error::McpServerError;

// ── McpHandler trait ──────────────────────────────────────────────────────────

/// An async handler for a single MCP method.
///
/// Each registered handler receives the raw `params` value from the JSON-RPC
/// request and returns either a serializable result or a typed error.
///
/// Use `async_trait` to implement this trait on structs, e.g.:
/// ```rust,ignore
/// use async_trait::async_trait;
/// use serde_json::Value;
/// use cascade_mcp::handler::McpHandler;
/// use cascade_mcp::error::McpServerError;
///
/// struct MyHandler;
///
/// #[async_trait]
/// impl McpHandler for MyHandler {
///     async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
///         Ok(serde_json::json!({ "ok": true }))
///     }
/// }
/// ```
#[async_trait]
pub trait McpHandler: Send + Sync {
    /// Handle a single method invocation.
    ///
    /// `params` is the raw JSON params from the JSON-RPC request, or `None`
    /// if the request had no `params` field.
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError>;
}

// ── HandlerRegistry ───────────────────────────────────────────────────────────

/// Registry that maps MCP method names to async handlers.
///
/// Thread-safe: can be cloned and shared across tasks. All mutations are
/// serialised through the inner `Mutex`.
#[derive(Clone)]
pub struct HandlerRegistry {
    handlers: Arc<Mutex<HashMap<String, Arc<dyn McpHandler>>>>,
}

impl HandlerRegistry {
    /// Create a new empty registry.
    pub fn new() -> Self {
        Self {
            handlers: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    /// Register a handler for `method`.
    ///
    /// If a handler was already registered for this method, it is replaced
    /// and a warning is logged. Returns `&mut Self` for builder-style chaining
    /// (must call on an owned value since we take `&mut self`).
    pub async fn register(&self, method: impl Into<String>, handler: impl McpHandler + 'static) {
        let method = method.into();
        let mut map = self.handlers.lock().await;
        if map.contains_key(&method) {
            warn!(method = %method, "HandlerRegistry: overwriting existing handler");
        } else {
            debug!(method = %method, "HandlerRegistry: registered handler");
        }
        map.insert(method, Arc::new(handler));
    }

    /// Dispatch a request to the registered handler for `method`.
    ///
    /// Returns `McpServerError::MethodNotFound` if no handler is registered.
    pub async fn dispatch(
        &self,
        method: &str,
        params: Option<Value>,
    ) -> Result<Value, McpServerError> {
        let handler = {
            let map = self.handlers.lock().await;
            map.get(method).cloned()
        };

        match handler {
            Some(h) => h.handle(params).await,
            None => Err(McpServerError::MethodNotFound {
                method: method.to_string(),
            }),
        }
    }

    /// Returns `true` if a handler for `method` is registered.
    pub async fn has_method(&self, method: &str) -> bool {
        let map = self.handlers.lock().await;
        map.contains_key(method)
    }

    /// Returns the list of all registered method names.
    pub async fn methods(&self) -> Vec<String> {
        let map = self.handlers.lock().await;
        map.keys().cloned().collect()
    }

    /// Derive [`ServerCapabilities`] from the currently registered methods.
    ///
    /// Scans registered method names for known prefixes and sets the
    /// corresponding capability flags:
    /// - `resources/` → `ResourcesCapability`
    /// - `tools/` → `ToolsCapability`
    /// - `prompts/` → `PromptsCapability`
    /// - `logging/` → sets `logging` to a non-None value
    ///
    /// The `list_changed` flag is `None` (not advertised) by default because
    /// the notification bus drives those — transports set it if needed.
    pub async fn capability_builder(&self) -> ServerCapabilities {
        let map = self.handlers.lock().await;
        let methods: Vec<&str> = map.keys().map(|s| s.as_str()).collect();

        let has_resources = methods.iter().any(|m| m.starts_with("resources/"));
        let has_tools = methods.iter().any(|m| m.starts_with("tools/"));
        let has_prompts = methods.iter().any(|m| m.starts_with("prompts/"));
        let has_logging = methods.iter().any(|m| m.starts_with("logging/"));
        let has_sampling = methods.iter().any(|m| m.starts_with("sampling/"));

        ServerCapabilities {
            resources: if has_resources {
                Some(ResourcesCapability {
                    subscribe: None,
                    list_changed: None,
                })
            } else {
                None
            },
            tools: if has_tools {
                Some(ToolsCapability { list_changed: None })
            } else {
                None
            },
            prompts: if has_prompts {
                Some(PromptsCapability { list_changed: None })
            } else {
                None
            },
            logging: if has_logging {
                Some(serde_json::json!({}))
            } else {
                None
            },
            experimental: if has_sampling {
                Some(serde_json::json!({ "sampling": {} }))
            } else {
                None
            },
        }
    }
}

impl Default for HandlerRegistry {
    fn default() -> Self {
        Self::new()
    }
}

impl std::fmt::Debug for HandlerRegistry {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("HandlerRegistry").finish_non_exhaustive()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    struct OkHandler(Value);

    #[async_trait]
    impl McpHandler for OkHandler {
        async fn handle(&self, _params: Option<Value>) -> Result<Value, McpServerError> {
            Ok(self.0.clone())
        }
    }

    struct EchoHandler;

    #[async_trait]
    impl McpHandler for EchoHandler {
        async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
            Ok(params.unwrap_or(Value::Null))
        }
    }

    #[tokio::test]
    async fn registry_dispatch_registered_handler() {
        let reg = HandlerRegistry::new();
        reg.register("tools/list", OkHandler(serde_json::json!({ "tools": [] }))).await;

        let result = reg.dispatch("tools/list", None).await.unwrap();
        assert_eq!(result["tools"], serde_json::json!([]));
    }

    #[tokio::test]
    async fn registry_dispatch_unregistered_returns_method_not_found() {
        let reg = HandlerRegistry::new();
        let err = reg.dispatch("tools/call", None).await.unwrap_err();
        assert!(matches!(err, McpServerError::MethodNotFound { method } if method == "tools/call"));
    }

    #[tokio::test]
    async fn registry_dispatch_with_params() {
        let reg = HandlerRegistry::new();
        reg.register("resources/read", EchoHandler).await;

        let params = serde_json::json!({ "uri": "cascade://memory/decisions.md" });
        let result = reg.dispatch("resources/read", Some(params.clone())).await.unwrap();
        assert_eq!(result, params);
    }

    #[tokio::test]
    async fn registry_duplicate_registration_replaces() {
        let reg = HandlerRegistry::new();
        reg.register("tools/list", OkHandler(serde_json::json!({ "tools": [] }))).await;
        reg.register("tools/list", OkHandler(serde_json::json!({ "tools": ["a"] }))).await;

        let result = reg.dispatch("tools/list", None).await.unwrap();
        // Second registration wins.
        assert_eq!(result["tools"][0], "a");
    }

    #[tokio::test]
    async fn capability_builder_tools_when_tools_registered() {
        let reg = HandlerRegistry::new();
        reg.register("tools/list", OkHandler(Value::Null)).await;
        reg.register("tools/call", OkHandler(Value::Null)).await;

        let caps = reg.capability_builder().await;
        assert!(caps.tools.is_some(), "tools capability must be set");
        assert!(caps.resources.is_none(), "resources must not be set");
        assert!(caps.prompts.is_none(), "prompts must not be set");
    }

    #[tokio::test]
    async fn capability_builder_resources_when_resources_registered() {
        let reg = HandlerRegistry::new();
        reg.register("resources/list", OkHandler(Value::Null)).await;
        reg.register("resources/read", OkHandler(Value::Null)).await;

        let caps = reg.capability_builder().await;
        assert!(caps.resources.is_some(), "resources capability must be set");
        assert!(caps.tools.is_none(), "tools must not be set");
    }

    #[tokio::test]
    async fn capability_builder_empty_registry_all_none() {
        let reg = HandlerRegistry::new();
        let caps = reg.capability_builder().await;
        assert!(caps.tools.is_none());
        assert!(caps.resources.is_none());
        assert!(caps.prompts.is_none());
        assert!(caps.logging.is_none());
    }

    #[tokio::test]
    async fn registry_has_method() {
        let reg = HandlerRegistry::new();
        assert!(!reg.has_method("tools/list").await);
        reg.register("tools/list", OkHandler(Value::Null)).await;
        assert!(reg.has_method("tools/list").await);
    }
}
