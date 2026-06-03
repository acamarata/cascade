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

use crate::cancellation::CancellationRegistry;
use crate::logging::McpLogger;
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
pub const ERR_INVALID_PARAMS: i32 = -32602;
pub const ERR_INTERNAL: i32 = -32603;

impl JsonRpcError {
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ServerState {
    /// Waiting for `initialize` from client.
    Handshake,
    /// `initialize` received, `initialized` notification pending.
    Initializing,
    /// Fully ready to process requests.
    Ready,
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
    progress: Arc<ProgressEmitter>,
    cancellation: Arc<CancellationRegistry>,
    sampling: Arc<SamplingClient>,
    logger: Arc<McpLogger>,
    state: Arc<RwLock<ServerState>>,
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
            progress: Arc::new(ProgressEmitter::new()),
            cancellation: Arc::new(CancellationRegistry::new()),
            sampling: Arc::new(SamplingClient::new()),
            logger: Arc::new(McpLogger::new()),
            state: Arc::new(RwLock::new(ServerState::Handshake)),
            shutdown_tx,
        }
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
    async fn handle_request(
        &self,
        req: &JsonRpcRequest,
    ) -> std::result::Result<Value, JsonRpcError> {
        debug!(method = %req.method, "Handling request");

        match req.method.as_str() {
            "initialize" => self.handle_initialize(req).await,
            "resources/list" => self
                .resources
                .list()
                .await
                .map_err(|e| JsonRpcError::internal(e.to_string())),
            "resources/read" => self
                .resources
                .read(&req.params)
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
            _ => Err(JsonRpcError {
                code: -32601,
                message: format!("Method not found: {}", req.method),
                data: None,
            }),
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
    /// Returns `ServerInfo` + `ServerCapabilities`. Must complete <100ms.
    async fn handle_initialize(
        &self,
        _req: &JsonRpcRequest,
    ) -> std::result::Result<Value, JsonRpcError> {
        let mut state = self.state.write().await;
        *state = ServerState::Initializing;

        let caps = ServerCapabilities {
            resources: Some(ResourceCapability {
                subscribe: false,
                list_changed: false,
            }),
            tools: Some(ToolCapability {
                list_changed: false,
            }),
            prompts: Some(PromptCapability {
                list_changed: false,
            }),
            sampling: Some(SamplingCapability {}),
            logging: Some(LoggingCapability {}),
        };

        Ok(serde_json::json!({
            "protocolVersion": "2025-03",
            "serverInfo": {
                "name": self.config.server_name,
                "version": self.config.server_version,
            },
            "capabilities": caps,
        }))
    }

    /// Send a shutdown signal to the run loop.
    pub fn shutdown(&self) {
        let _ = self.shutdown_tx.send(());
    }
}
