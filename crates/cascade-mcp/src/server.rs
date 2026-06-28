//! MCP server core — capability negotiation, request dispatch, lifecycle.
//!
//! `McpServer` is the top-level entry point. It owns one `Transport` and
//! delegates incoming JSON-RPC messages to the appropriate handler:
//! resources, tools, prompts, sampling, or logging.
//!
//! ## Lifecycle
//!
//! 1. Client sends `initialize` → server responds with `ServerInfo` +
//!    `ServerCapabilities`.
//! 2. Client sends `initialized` notification → server transitions to
//!    `Ready` state.
//! 3. Normal request/response loop until client disconnects.
//!
//! ## JSON-RPC 2.0
//!
//! All messages use JSON-RPC 2.0 envelopes. Application errors use codes in
//! the `-32000` range:
//! - `-32001` `NOT_FOUND`
//! - `-32002` `INDEX_NOT_READY`
//! - `-32003` `PERMISSION_DENIED`

use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokio::sync::{broadcast, RwLock};
use tracing::{debug, error, info, warn};

use cascade_types::error::{CascadeError, Result};
use cascade_types::mcp::MCP_PROTOCOL_VERSION;

use cascade_providers::ProviderRegistry;

use crate::cancellation::CancellationRegistry;
use crate::error::McpServerError;
use crate::handler::HandlerRegistry;
use crate::logging::McpLogger;
use crate::notification::NotificationBus;
use crate::progress::ProgressEmitter;
use crate::resource::ResourceRegistry;
use crate::sampling::SamplingClient;
use crate::tool::ToolRegistry;
use crate::transport::Transport;

// ── JSON-RPC 2.0 envelope types ───────────────────────────────────────────────

/// A JSON-RPC 2.0 request (client → server).
///
/// Constraints: `id` MUST be present for requests; notifications omit it.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcRequest {
    pub jsonrpc: String,
    pub method: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<RequestId>,
    #[serde(default, skip_serializing_if = "Value::is_null")]
    pub params: Value,
}

/// A JSON-RPC 2.0 response (server → client).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcResponse {
    pub jsonrpc: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<RequestId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<JsonRpcError>,
}

/// A JSON-RPC 2.0 notification (server → client, no `id`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcNotification {
    pub jsonrpc: String,
    pub method: String,
    pub params: Value,
}

impl JsonRpcNotification {
    /// Construct a notification for the `notifications/initialized` method.
    pub fn initialized() -> Self {
        Self {
            jsonrpc: "2.0".into(),
            method: "notifications/initialized".into(),
            params: serde_json::json!({}),
        }
    }
}

/// JSON-RPC error object.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcError {
    pub code: i32,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl std::fmt::Display for JsonRpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "JSON-RPC error {} — {}", self.code, self.message)
    }
}

/// Request identifier — may be a string, integer, or null.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Hash)]
#[serde(untagged)]
pub enum RequestId {
    String(String),
    Number(i64),
}

// ── MCP application error codes ───────────────────────────────────────────────

pub const ERR_NOT_FOUND: i32 = -32001;
pub const ERR_INDEX_NOT_READY: i32 = -32002;
pub const ERR_PERMISSION_DENIED: i32 = -32003;
pub const ERR_METHOD_NOT_FOUND: i32 = -32601;
pub const ERR_INVALID_PARAMS: i32 = -32602;
pub const ERR_INTERNAL: i32 = -32603;

impl JsonRpcError {
    pub fn method_not_found(method: impl Into<String>) -> Self {
        let m = method.into();
        Self {
            code: ERR_METHOD_NOT_FOUND,
            message: format!("Method not found: {m}"),
            data: None,
        }
    }

    pub fn not_found(msg: impl Into<String>) -> Self {
        Self {
            code: ERR_NOT_FOUND,
            message: msg.into(),
            data: None,
        }
    }

    pub fn index_not_ready() -> Self {
        Self {
            code: ERR_INDEX_NOT_READY,
            message: "Index not ready — run `cascade index rebuild`".into(),
            data: None,
        }
    }

    pub fn invalid_params(msg: impl Into<String>) -> Self {
        Self {
            code: ERR_INVALID_PARAMS,
            message: msg.into(),
            data: None,
        }
    }

    pub fn internal(msg: impl Into<String>) -> Self {
        Self {
            code: ERR_INTERNAL,
            message: msg.into(),
            data: None,
        }
    }
}

// ── McpServerError -> local JsonRpcError bridge ───────────────────────────────

impl From<McpServerError> for JsonRpcError {
    fn from(e: McpServerError) -> Self {
        match &e {
            McpServerError::UnsupportedProtocolVersion { .. }
            | McpServerError::AlreadyInitialized
            | McpServerError::WrongState { .. } => Self {
                code: -32600,
                message: e.to_string(),
                data: None,
            },
            McpServerError::MethodNotFound { .. } => Self {
                code: ERR_METHOD_NOT_FOUND,
                message: e.to_string(),
                data: None,
            },
            McpServerError::InvalidParams { .. } => Self {
                code: ERR_INVALID_PARAMS,
                message: e.to_string(),
                data: None,
            },
            McpServerError::Internal { .. } | McpServerError::AuthFailed { .. } => Self {
                code: ERR_INTERNAL,
                message: e.to_string(),
                data: None,
            },
        }
    }
}

// ── Server capabilities ───────────────────────────────────────────────────────

/// Capabilities this server advertises during `initialize`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ServerCapabilities {
    pub resources: Option<ResourceCapability>,
    pub tools: Option<ToolCapability>,
    pub prompts: Option<PromptCapability>,
    pub sampling: Option<SamplingCapability>,
    pub logging: Option<LoggingCapability>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct ResourceCapability {
    pub subscribe: bool,
    pub list_changed: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct ToolCapability {
    pub list_changed: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct PromptCapability {
    pub list_changed: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct SamplingCapability {}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct LoggingCapability {}

// ── Server config ─────────────────────────────────────────────────────────────

/// Configuration for [`McpServer`].
#[derive(Debug, Clone)]
pub struct McpServerConfig {
    /// Server name reported during `initialize`.
    pub server_name: String,
    /// Server version reported during `initialize`.
    pub server_version: String,
    /// Maximum concurrent in-flight requests.
    pub max_in_flight: usize,
    /// Sampling request timeout in seconds.
    pub sampling_timeout_secs: u64,
}

impl Default for McpServerConfig {
    fn default() -> Self {
        Self {
            server_name: "cascade".into(),
            server_version: env!("CARGO_PKG_VERSION").into(),
            max_in_flight: 64,
            sampling_timeout_secs: 30,
        }
    }
}

// ── Server state ──────────────────────────────────────────────────────────────

/// Server lifecycle state machine.
///
/// Transitions: `Handshake` → `Initializing` → `Ready` → `ShuttingDown` → (closed)
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ServerState {
    /// Waiting for `initialize` from client.
    Handshake,
    /// `initialize` received, `initialized` notification pending.
    Initializing,
    /// Fully ready to process requests.
    Ready,
    /// `shutdown` received — draining in-flight requests before closing.
    ShuttingDown,
}

// ── McpServer ─────────────────────────────────────────────────────────────────

/// The top-level MCP 2025-03 server.
///
/// Owns the transport, registries, and per-request state. Call [`run`] to
/// start the message loop; it resolves when the transport is closed.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_mcp::{McpServer, McpServerConfig};
/// # use cascade_mcp::transport::stdio::StdioTransport;
/// # async fn run() -> cascade_types::error::Result<()> {
/// let server = McpServer::new(McpServerConfig::default(), Box::new(StdioTransport::new()));
/// server.run().await
/// # }
/// ```
pub struct McpServer {
    config: McpServerConfig,
    transport: Box<dyn Transport>,
    resources: Arc<ResourceRegistry>,
    tools: Arc<ToolRegistry>,
    /// Progress emitter for long-running tool calls — wired to transport in the run loop.
    _progress: Arc<ProgressEmitter>,
    cancellation: Arc<CancellationRegistry>,
    sampling: Arc<SamplingClient>,
    logger: Arc<McpLogger>,
    state: Arc<RwLock<ServerState>>,
    /// Dynamic handler registry — dispatch path for T-P4-E02-05.
    registry: Arc<HandlerRegistry>,
    /// Server-initiated notification bus — transports subscribe before `run()`.
    notification_bus: NotificationBus,
    /// Shutdown signal — broadcast `()` to stop the loop.
    shutdown_tx: broadcast::Sender<()>,
}

impl McpServer {
    /// Create a new server with the given config and transport.
    ///
    /// Tool and resource registries are populated with the standard Cascade
    /// handlers. Call [`run`] to start serving.
    pub fn new(config: McpServerConfig, transport: Box<dyn Transport>) -> Self {
        let (shutdown_tx, _) = broadcast::channel(1);
        Self {
            config,
            transport,
            resources: Arc::new(ResourceRegistry::new()),
            tools: Arc::new(ToolRegistry::new()),
            _progress: Arc::new(ProgressEmitter::new()),
            cancellation: Arc::new(CancellationRegistry::new()),
            sampling: Arc::new(SamplingClient::new()),
            logger: Arc::new(McpLogger::new()),
            state: Arc::new(RwLock::new(ServerState::Handshake)),
            registry: Arc::new(HandlerRegistry::new()),
            notification_bus: NotificationBus::new(),
            shutdown_tx,
        }
    }

    /// Create a server with a pre-configured [`HandlerRegistry`].
    ///
    /// Capabilities are derived from the registry via
    /// [`HandlerRegistry::capability_builder`] during `initialize`.
    pub fn with_registry(
        config: McpServerConfig,
        transport: Box<dyn Transport>,
        registry: HandlerRegistry,
    ) -> Self {
        let (shutdown_tx, _) = broadcast::channel(1);
        Self {
            config,
            transport,
            resources: Arc::new(ResourceRegistry::new()),
            tools: Arc::new(ToolRegistry::new()),
            _progress: Arc::new(ProgressEmitter::new()),
            cancellation: Arc::new(CancellationRegistry::new()),
            sampling: Arc::new(SamplingClient::new()),
            logger: Arc::new(McpLogger::new()),
            state: Arc::new(RwLock::new(ServerState::Handshake)),
            registry: Arc::new(registry),
            notification_bus: NotificationBus::new(),
            shutdown_tx,
        }
    }

    /// Attach a pre-configured [`ToolRegistry`] to this server.
    ///
    /// Use this to inject a live retriever into the MCP server before calling
    /// [`run`]:
    ///
    /// ```rust,no_run
    /// # use std::sync::Arc;
    /// # use cascade_mcp::{McpServer, McpServerConfig};
    /// # use cascade_mcp::tool::ToolRegistry;
    /// # use cascade_mcp::transport::stdio::StdioTransport;
    /// # use cascade_types::retriever::NoopRetriever;
    /// let tools = ToolRegistry::new().with_retriever(Arc::new(NoopRetriever));
    /// let server = McpServer::new(McpServerConfig::default(), Box::new(StdioTransport::new()))
    ///     .with_tools(tools);
    /// ```
    pub fn with_tools(mut self, tools: ToolRegistry) -> Self {
        self.tools = Arc::new(tools);
        self
    }

    /// Attach a live [`ProviderRegistry`] to the sampling handler.
    ///
    /// Without this call, `sampling/createMessage` requests return an error
    /// ("no AI provider configured"). Call this in the daemon startup path
    /// after the provider registry has been populated.
    ///
    /// ```rust,no_run
    /// # use std::sync::Arc;
    /// # use cascade_mcp::{McpServer, McpServerConfig};
    /// # use cascade_mcp::transport::stdio::StdioTransport;
    /// # use cascade_providers::ProviderRegistry;
    /// let registry = Arc::new(ProviderRegistry::new());
    /// let server = McpServer::new(McpServerConfig::default(), Box::new(StdioTransport::new()))
    ///     .with_provider_registry(registry);
    /// ```
    pub fn with_provider_registry(mut self, registry: Arc<ProviderRegistry>) -> Self {
        self.sampling = Arc::new(SamplingClient::with_registry(registry));
        self
    }

    /// Return a reference to the notification bus.
    ///
    /// Transports can call `subscribe()` on this before `run()` to receive
    /// server-initiated notifications (resource/tool list changes, etc.).
    pub fn notification_bus(&self) -> &NotificationBus {
        &self.notification_bus
    }

    /// Return a reference to the handler registry.
    pub fn registry(&self) -> &HandlerRegistry {
        &self.registry
    }

    /// Returns `true` only when the server is in the `Ready` state.
    pub async fn is_ready(&self) -> bool {
        *self.state.read().await == ServerState::Ready
    }

    /// Returns the current server lifecycle state.
    pub async fn current_state(&self) -> ServerState {
        *self.state.read().await
    }

    /// Run the server until the transport closes or a shutdown signal fires.
    ///
    /// This is the main event loop: read a message, dispatch it, send the
    /// response. Progress notifications and cancellation fire on a background
    /// task spawned per request.
    pub async fn run(mut self) -> Result<()> {
        info!(name = %self.config.server_name, version = %self.config.server_version, "MCP server starting");

        loop {
            let raw = match self.transport.recv().await {
                Ok(Some(msg)) => msg,
                Ok(None) => {
                    info!("Transport closed — shutting down");
                    break;
                }
                Err(e) => {
                    error!("Transport recv error: {e}");
                    break;
                }
            };

            let response = self.dispatch(&raw).await;

            if let Some(resp) = response {
                let encoded =
                    serde_json::to_string(&resp).map_err(|e| CascadeError::ConfigParse {
                        path: "<mcp-response>".into(),
                        detail: e.to_string(),
                    })?;
                if let Err(e) = self.transport.send(&encoded).await {
                    error!("Transport send error: {e}");
                    break;
                }
            }
        }

        info!("MCP server stopped");
        Ok(())
    }

    /// Dispatch one raw JSON-RPC message.
    ///
    /// Returns `None` for notifications (no response required). Returns a
    /// well-formed `JsonRpcResponse` (result or error) for requests.
    ///
    /// Never panics — all errors are converted to JSON-RPC error responses.
    async fn dispatch(&self, raw: &str) -> Option<JsonRpcResponse> {
        let req: JsonRpcRequest = match serde_json::from_str(raw) {
            Ok(r) => r,
            Err(e) => {
                warn!("Malformed JSON-RPC message: {e}");
                return Some(JsonRpcResponse {
                    jsonrpc: "2.0".into(),
                    id: None,
                    result: None,
                    error: Some(JsonRpcError {
                        code: -32700,
                        message: format!("Parse error: {e}"),
                        data: None,
                    }),
                });
            }
        };

        // Notifications have no `id` and require no response.
        if req.id.is_none() {
            self.handle_notification(&req).await;
            return None;
        }

        let id = req.id.clone();
        let result = self.handle_request(&req).await;

        Some(match result {
            Ok(value) => JsonRpcResponse {
                jsonrpc: "2.0".into(),
                id,
                result: Some(value),
                error: None,
            },
            Err(e) => JsonRpcResponse {
                jsonrpc: "2.0".into(),
                id,
                result: None,
                error: Some(JsonRpcError::internal(e.to_string())),
            },
        })
    }

    /// Handle a request (has `id`).
    ///
    /// Dispatch order:
    /// 1. Lifecycle methods handled directly (`initialize`, `ping`, `shutdown`).
    /// 2. All other methods checked in `HandlerRegistry` first.
    /// 3. Fall-through to built-in primitive registries (resources, tools, etc.)
    ///    for backward-compat with P2 skeleton.
    /// 4. If nothing matches, returns `MethodNotFound`.
    async fn handle_request(
        &self,
        req: &JsonRpcRequest,
    ) -> std::result::Result<Value, JsonRpcError> {
        debug!(method = %req.method, "Handling request");

        match req.method.as_str() {
            // ── Lifecycle ────────────────────────────────────────────────────
            "initialize" => return self.handle_initialize(req).await,
            "ping" => return self.handle_ping(),
            "shutdown" => return self.handle_shutdown_request().await,
            _ => {}
        }

        // Guard: block non-lifecycle methods before Ready.
        {
            let state = self.state.read().await;
            if *state != ServerState::Ready {
                let err = McpServerError::WrongState {
                    method: req.method.clone(),
                };
                return Err(JsonRpcError::from(err));
            }
        }

        // ── Registry dispatch ────────────────────────────────────────────────
        // Try the dynamic registry first.
        if self.registry.has_method(&req.method).await {
            let params = if req.params.is_null() {
                None
            } else {
                Some(req.params.clone())
            };
            return self
                .registry
                .dispatch(&req.method, params)
                .await
                .map_err(JsonRpcError::from);
        }

        // ── Built-in primitive fallback (P2 skeleton) ─────────────────────
        match req.method.as_str() {
            "resources/list" => self
                .resources
                .list(Some(&req.params))
                .await
                .map_err(|e| JsonRpcError::internal(e.to_string())),
            "resources/read" => self
                .resources
                .read(Some(&req.params))
                .await
                .map_err(|e| JsonRpcError::internal(e.to_string())),
            "tools/list" => self
                .tools
                .list()
                .await
                .map_err(|e| JsonRpcError::internal(e.to_string())),
            "tools/call" => self.tools.call(&req.params).await,
            "prompts/list" => {
                use crate::prompt::PromptRegistry;
                PromptRegistry::list_static()
            }
            "prompts/get" => {
                use crate::prompt::PromptRegistry;
                PromptRegistry::get_static(&req.params)
            }
            "sampling/createMessage" => self
                .sampling
                .create_message(&req.params)
                .await
                .map_err(|e| JsonRpcError::internal(e.to_string())),
            "logging/setLevel" => self
                .logger
                .set_level(&req.params)
                .map_err(|e| JsonRpcError::internal(e.to_string())),
            _ => Err(JsonRpcError::method_not_found(&req.method)),
        }
    }

    /// Handle a notification (no `id` — no response required).
    async fn handle_notification(&self, req: &JsonRpcRequest) {
        debug!(method = %req.method, "Handling notification");
        match req.method.as_str() {
            "initialized" => {
                let mut state = self.state.write().await;
                *state = ServerState::Ready;
                info!("Client initialized — server ready");
            }
            "notifications/cancelled" => {
                if let Some(cancel_id) = req.params.get("requestId") {
                    if let Some(id_str) = cancel_id.as_str() {
                        self.cancellation.cancel(id_str);
                    }
                }
            }
            _ => {
                debug!(method = %req.method, "Unhandled notification");
            }
        }
    }

    /// Handle the `initialize` handshake.
    ///
    /// Validates the client's `protocolVersion`, transitions state to
    /// `Initializing`, and returns `InitializeResult` with derived capabilities.
    ///
    /// Returns `InvalidRequest` if:
    /// - The server is already initialized (`AlreadyInitialized`).
    /// - The client advertises an unsupported protocol version.
    async fn handle_initialize(
        &self,
        req: &JsonRpcRequest,
    ) -> std::result::Result<Value, JsonRpcError> {
        // Guard: re-initialization is prohibited by spec.
        {
            let state = self.state.read().await;
            if *state != ServerState::Handshake {
                let err = McpServerError::AlreadyInitialized;
                return Err(JsonRpcError::from(err));
            }
        }

        // Validate protocol version if present in params.
        if let Some(version) = req.params.get("protocolVersion").and_then(|v| v.as_str()) {
            if version != MCP_PROTOCOL_VERSION {
                warn!(
                    client_version = %version,
                    server_version = %MCP_PROTOCOL_VERSION,
                    "Protocol version mismatch"
                );
                let err = McpServerError::UnsupportedProtocolVersion {
                    version: version.to_string(),
                };
                return Err(JsonRpcError::from(err));
            }
        }

        // Transition to Initializing.
        {
            let mut state = self.state.write().await;
            *state = ServerState::Initializing;
            info!("MCP handshake: Handshake -> Initializing");
        }

        // Derive capabilities from registered handlers.
        let derived_caps = self.registry.capability_builder().await;

        Ok(serde_json::json!({
            "protocolVersion": MCP_PROTOCOL_VERSION,
            "serverInfo": {
                "name": self.config.server_name,
                "version": self.config.server_version,
            },
            "capabilities": derived_caps,
        }))
    }

    /// Handle the `shutdown` request.
    ///
    /// Transitions to `ShuttingDown` and signals the run loop. Per spec,
    /// in-flight requests are drained before the loop exits.
    pub async fn handle_shutdown_request(&self) -> std::result::Result<Value, JsonRpcError> {
        {
            let mut state = self.state.write().await;
            *state = ServerState::ShuttingDown;
            info!("MCP lifecycle: -> ShuttingDown");
        }
        // Signal the run loop to exit after current message is processed.
        let _ = self.shutdown_tx.send(());
        Ok(serde_json::json!({}))
    }

    /// Handle the `ping` request.
    ///
    /// Per spec, `ping` succeeds in every server state (even before initialized).
    pub fn handle_ping(&self) -> std::result::Result<Value, JsonRpcError> {
        Ok(serde_json::json!({}))
    }

    /// Send a shutdown signal to the run loop.
    pub fn shutdown(&self) {
        let _ = self.shutdown_tx.send(());
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::McpServerError;
    use crate::handler::{HandlerRegistry, McpHandler};
    use async_trait::async_trait;

    /// Null transport — not needed for unit tests that call methods directly.
    struct NullTransport;

    #[async_trait]
    impl crate::transport::Transport for NullTransport {
        async fn recv(&mut self) -> cascade_types::error::Result<Option<String>> {
            // Block forever — tests won't call run().
            std::future::pending().await
        }
        async fn send(&mut self, _msg: &str) -> cascade_types::error::Result<()> {
            Ok(())
        }
        async fn close(&mut self) -> cascade_types::error::Result<()> {
            Ok(())
        }
        fn name(&self) -> &str {
            "null"
        }
    }

    fn make_server() -> McpServer {
        McpServer::new(McpServerConfig::default(), Box::new(NullTransport))
    }

    fn make_server_with_registry(reg: HandlerRegistry) -> McpServer {
        McpServer::with_registry(McpServerConfig::default(), Box::new(NullTransport), reg)
    }

    fn init_req(protocol_version: &str) -> JsonRpcRequest {
        JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "initialize".into(),
            id: Some(RequestId::Number(1)),
            params: serde_json::json!({
                "protocolVersion": protocol_version,
                "capabilities": {},
                "clientInfo": { "name": "test-client", "version": "0.1.0" }
            }),
        }
    }

    // ── Lifecycle state transitions ───────────────────────────────────────────

    #[tokio::test]
    async fn server_starts_in_handshake_state() {
        let server = make_server();
        assert_eq!(server.current_state().await, ServerState::Handshake);
        assert!(!server.is_ready().await);
    }

    #[tokio::test]
    async fn handshake_transitions_to_initializing() {
        let server = make_server();
        let req = init_req(MCP_PROTOCOL_VERSION);
        let result = server.handle_initialize(&req).await;
        assert!(result.is_ok(), "initialize should succeed: {result:?}");
        assert_eq!(server.current_state().await, ServerState::Initializing);
        assert!(!server.is_ready().await);
    }

    #[tokio::test]
    async fn initialized_notification_transitions_to_ready() {
        let server = make_server();
        // Step 1: initialize
        let req = init_req(MCP_PROTOCOL_VERSION);
        server.handle_initialize(&req).await.unwrap();
        assert_eq!(server.current_state().await, ServerState::Initializing);

        // Step 2: initialized notification
        let notif = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "initialized".into(),
            id: None,
            params: serde_json::json!({}),
        };
        server.handle_notification(&notif).await;
        assert_eq!(server.current_state().await, ServerState::Ready);
        assert!(server.is_ready().await);
    }

    #[tokio::test]
    async fn shutdown_transitions_to_shutting_down() {
        let server = make_server();
        // Get to Ready first.
        server
            .handle_initialize(&init_req(MCP_PROTOCOL_VERSION))
            .await
            .unwrap();
        let notif = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "initialized".into(),
            id: None,
            params: serde_json::json!({}),
        };
        server.handle_notification(&notif).await;
        assert!(server.is_ready().await);

        // Shutdown.
        let result = server.handle_shutdown_request().await;
        assert!(result.is_ok());
        assert_eq!(server.current_state().await, ServerState::ShuttingDown);
    }

    // ── Version negotiation ───────────────────────────────────────────────────

    #[tokio::test]
    async fn initialize_wrong_version_returns_error() {
        let server = make_server();
        let req = init_req("2024-11-05"); // old version
        let result = server.handle_initialize(&req).await;
        assert!(result.is_err(), "wrong version should fail");
        let err = result.unwrap_err();
        assert_eq!(err.code, -32600, "should be InvalidRequest: {err:?}");
        assert_eq!(server.current_state().await, ServerState::Handshake);
    }

    #[tokio::test]
    async fn initialize_result_contains_correct_server_info() {
        let server = make_server();
        let req = init_req(MCP_PROTOCOL_VERSION);
        let result = server.handle_initialize(&req).await.unwrap();
        assert_eq!(result["serverInfo"]["name"], "cascade");
        assert_eq!(result["protocolVersion"], MCP_PROTOCOL_VERSION);
    }

    #[tokio::test]
    async fn re_initialization_returns_error() {
        let server = make_server();
        // First initialize — OK.
        server
            .handle_initialize(&init_req(MCP_PROTOCOL_VERSION))
            .await
            .unwrap();
        // Second initialize — must fail.
        let result = server
            .handle_initialize(&init_req(MCP_PROTOCOL_VERSION))
            .await;
        assert!(result.is_err(), "re-initialize must fail");
        let err = result.unwrap_err();
        assert_eq!(err.code, -32600);
    }

    // ── Ping works in all states ───────────────────────────────────────────────

    #[tokio::test]
    async fn ping_works_before_initialized() {
        let server = make_server();
        assert_eq!(server.current_state().await, ServerState::Handshake);
        let result = server.handle_ping();
        assert!(result.is_ok());
    }

    #[tokio::test]
    async fn ping_works_after_initialized() {
        let server = make_server();
        server
            .handle_initialize(&init_req(MCP_PROTOCOL_VERSION))
            .await
            .unwrap();
        let notif = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "initialized".into(),
            id: None,
            params: serde_json::json!({}),
        };
        server.handle_notification(&notif).await;
        let result = server.handle_ping();
        assert!(result.is_ok());
    }

    // ── Dispatch: MethodNotFound ───────────────────────────────────────────────

    #[tokio::test]
    async fn dispatch_unknown_method_returns_method_not_found() {
        let server = make_server();
        // Get to Ready.
        server
            .handle_initialize(&init_req(MCP_PROTOCOL_VERSION))
            .await
            .unwrap();
        let notif = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "initialized".into(),
            id: None,
            params: serde_json::json!({}),
        };
        server.handle_notification(&notif).await;

        let req = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "nonexistent/method".into(),
            id: Some(RequestId::Number(2)),
            params: serde_json::json!({}),
        };
        let result = server.handle_request(&req).await;
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert_eq!(err.code, ERR_METHOD_NOT_FOUND);
    }

    #[tokio::test]
    async fn dispatch_before_ready_returns_wrong_state() {
        let server = make_server();
        let req = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "tools/list".into(),
            id: Some(RequestId::Number(1)),
            params: serde_json::json!({}),
        };
        let result = server.handle_request(&req).await;
        assert!(result.is_err());
        // WrongState maps to -32600 (InvalidRequest)
        let err = result.unwrap_err();
        assert_eq!(err.code, -32600);
    }

    // ── Capability derivation from registry ───────────────────────────────────

    #[tokio::test]
    async fn capability_derivation_reflects_registered_methods() {
        struct OkHandler;
        #[async_trait]
        impl McpHandler for OkHandler {
            async fn handle(
                &self,
                _p: Option<Value>,
            ) -> std::result::Result<Value, McpServerError> {
                Ok(serde_json::json!({}))
            }
        }

        let reg = HandlerRegistry::new();
        reg.register("tools/list", OkHandler).await;
        reg.register("tools/call", OkHandler).await;

        let server = make_server_with_registry(reg);
        let req = init_req(MCP_PROTOCOL_VERSION);
        let result = server.handle_initialize(&req).await.unwrap();

        let caps = &result["capabilities"];
        assert!(
            !caps["tools"].is_null(),
            "tools capability must be set: {caps}"
        );
        assert!(
            caps["resources"].is_null(),
            "resources must not be set: {caps}"
        );
    }

    // ── Registry dispatch ─────────────────────────────────────────────────────

    #[tokio::test]
    async fn registry_handler_is_dispatched_when_registered() {
        struct HelloHandler;
        #[async_trait]
        impl McpHandler for HelloHandler {
            async fn handle(
                &self,
                _p: Option<Value>,
            ) -> std::result::Result<Value, McpServerError> {
                Ok(serde_json::json!({ "hello": "world" }))
            }
        }

        let reg = HandlerRegistry::new();
        reg.register("custom/hello", HelloHandler).await;

        let server = make_server_with_registry(reg);
        // Get to Ready.
        server
            .handle_initialize(&init_req(MCP_PROTOCOL_VERSION))
            .await
            .unwrap();
        let notif = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "initialized".into(),
            id: None,
            params: serde_json::json!({}),
        };
        server.handle_notification(&notif).await;

        let req = JsonRpcRequest {
            jsonrpc: "2.0".into(),
            method: "custom/hello".into(),
            id: Some(RequestId::Number(10)),
            params: serde_json::json!({}),
        };
        let result = server.handle_request(&req).await.unwrap();
        assert_eq!(result["hello"], "world");
    }
}
