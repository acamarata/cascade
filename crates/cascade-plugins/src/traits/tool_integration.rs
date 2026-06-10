//! ToolIntegration plugin trait — host-side plugin-facing API.
//!
//! Purpose: define the `ToolIntegration` trait that a WASM plugin must satisfy to
//!   be registered as a callable tool in the cascade-plugins host. Designed to map
//!   directly to the MCP tool shape so cascade-mcp can bridge it without a wrapper.
//! Inputs: `ToolCall` — tool name + JSON arguments conforming to `schema()`.
//! Outputs: `ToolResult` — JSON result value or error.
//! Constraints: all public types are `Serialize + Deserialize`; trait is object-safe;
//!   `Send + Sync + 'static` required; `schema()` returns valid JSON Schema object.
//! SPORT: cascade-plugins / tool_integration plugin trait (T-P4-E03-02)

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use thiserror::Error;

// ── I/O shapes ────────────────────────────────────────────────────────────────

/// A tool invocation request.
///
/// `arguments` must conform to the JSON Schema returned by `ToolIntegration::schema()`.
/// The host validates this before calling `ToolIntegration::call()`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolCall {
    /// Tool identifier — must match `ToolIntegration::name()`.
    pub name: String,

    /// JSON arguments payload (JSON Schema object instance).
    pub arguments: serde_json::Value,

    /// Optional correlation ID for tracing and log attribution.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub call_id: Option<String>,
}

/// The result of a tool invocation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolResult {
    /// The result value returned by the tool. Schema is tool-defined.
    pub result: serde_json::Value,

    /// If `true`, the tool result is a human-readable error explanation rather
    /// than a success payload (mirrors the MCP `isError` field).
    #[serde(default)]
    pub is_error: bool,

    /// Optional correlation ID echoed from the `ToolCall`, if provided.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub call_id: Option<String>,
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors that a `ToolIntegration` may return (plugin-level failures).
///
/// Application-level tool errors are expressed via `ToolResult::is_error = true`
/// so the agent can distinguish a plugin crash from a tool reporting failure.
#[derive(Debug, Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ToolIntegrationError {
    /// Network or I/O failure inside the tool.
    #[error("IO error in tool plugin: {message}")]
    Io { message: String },

    /// The arguments JSON did not conform to the tool's schema.
    #[error("parse/validation error in tool plugin: {message}")]
    Parse { message: String },

    /// The tool is temporarily unavailable.
    #[error("tool plugin unavailable: {message}")]
    Unavailable { message: String },
}

// ── Capability declarations ───────────────────────────────────────────────────

/// Capability declaration for a `ToolIntegration` plugin.
///
/// Tools are lightweight by default (64 MiB), but may request more if they
/// need to load data files (e.g. a code-search tool with an in-memory index).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolIntegrationCapabilities {
    /// Whether this tool requires outbound network access.
    #[serde(default)]
    pub needs_net_outbound: bool,

    /// Whether this tool requires filesystem read access.
    #[serde(default)]
    pub needs_fs_read: bool,

    /// Whether this tool requires filesystem write access.
    #[serde(default)]
    pub needs_fs_write: bool,

    /// Requested WASM memory in MiB. Host may cap to `MEM_SMALL_MB` (64) for tools.
    #[serde(default = "default_tool_memory_mb")]
    pub max_memory_mb: u32,
}

fn default_tool_memory_mb() -> u32 {
    64
}

impl Default for ToolIntegrationCapabilities {
    fn default() -> Self {
        Self {
            needs_net_outbound: false,
            needs_fs_read: false,
            needs_fs_write: false,
            max_memory_mb: default_tool_memory_mb(),
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Host-side trait a WASM tool plugin must satisfy.
///
/// The trait maps directly to the MCP tool shape so `cascade-mcp` can bridge
/// any registered `ToolIntegration` to MCP without introducing a wrapper type.
///
/// # MCP alignment
///
/// | MCP field    | Trait method           |
/// |--------------|------------------------|
/// | `name`       | `name()`               |
/// | `description`| `description()`        |
/// | `inputSchema`| `schema()`             |
/// | `call`       | `call(ToolCall)`       |
///
/// # Object-safety
///
/// This trait is object-safe. The `schema()` method returns `serde_json::Value`
/// (not an associated type) so no monomorphization escapes to call sites.
///
/// # Example (mock implementation for tests)
///
/// ```rust
/// # use cascade_plugins::traits::tool_integration::{ToolIntegration, ToolIntegrationCapabilities, ToolIntegrationError, ToolCall, ToolResult};
/// # use async_trait::async_trait;
/// struct EchoTool;
///
/// #[async_trait]
/// impl ToolIntegration for EchoTool {
///     fn name(&self) -> &str { "echo" }
///     fn description(&self) -> &str { "Echoes its input back." }
///     fn schema(&self) -> serde_json::Value {
///         serde_json::json!({ "type": "object", "properties": { "text": { "type": "string" } }, "required": ["text"] })
///     }
///     fn capabilities(&self) -> ToolIntegrationCapabilities { Default::default() }
///
///     async fn call(&self, call: ToolCall) -> Result<ToolResult, ToolIntegrationError> {
///         Ok(ToolResult {
///             result: call.arguments["text"].clone(),
///             is_error: false,
///             call_id: call.call_id,
///         })
///     }
/// }
/// ```
#[async_trait]
pub trait ToolIntegration: Send + Sync + 'static {
    /// Stable tool identifier (must be unique within a plugin registry).
    fn name(&self) -> &str;

    /// Human-readable description of what the tool does.
    ///
    /// Shown to agents in `AgentContext::available_tools` and in MCP tool listings.
    fn description(&self) -> &str;

    /// JSON Schema object describing the tool's input parameters.
    ///
    /// Must be a valid JSON Schema `{ "type": "object", "properties": { ... } }`.
    /// This value is passed directly to MCP as `inputSchema`.
    fn schema(&self) -> serde_json::Value;

    /// Capability declaration — inspected at plugin registration time.
    fn capabilities(&self) -> ToolIntegrationCapabilities;

    /// Invoke the tool with `call.arguments` and return a `ToolResult`.
    ///
    /// On application-level failure (e.g. "file not found"), return
    /// `Ok(ToolResult { is_error: true, result: error_json })` rather than `Err`.
    /// `Err` is reserved for plugin-level crashes and host protocol errors.
    async fn call(&self, call: ToolCall) -> Result<ToolResult, ToolIntegrationError>;
}

// ── Object-safety static assertion ───────────────────────────────────────────

fn _assert_object_safe(_: &dyn ToolIntegration) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    struct MockEchoTool;

    #[async_trait]
    impl ToolIntegration for MockEchoTool {
        fn name(&self) -> &str {
            "echo"
        }

        fn description(&self) -> &str {
            "Echoes the input text back to the caller."
        }

        fn schema(&self) -> serde_json::Value {
            serde_json::json!({
                "type": "object",
                "properties": {
                    "text": { "type": "string", "description": "Text to echo" }
                },
                "required": ["text"]
            })
        }

        fn capabilities(&self) -> ToolIntegrationCapabilities {
            Default::default()
        }

        async fn call(&self, call: ToolCall) -> Result<ToolResult, ToolIntegrationError> {
            let text = call.arguments.get("text").cloned().ok_or_else(|| {
                ToolIntegrationError::Parse {
                    message: "missing required field 'text'".into(),
                }
            })?;
            Ok(ToolResult {
                result: text,
                is_error: false,
                call_id: call.call_id,
            })
        }
    }

    #[tokio::test]
    async fn tool_integration_happy_path() {
        let tool = MockEchoTool;
        let call = ToolCall {
            name: "echo".into(),
            arguments: serde_json::json!({ "text": "hello cascade" }),
            call_id: Some("req-001".into()),
        };
        let result = tool.call(call).await.unwrap();
        assert!(!result.is_error);
        assert_eq!(result.result, serde_json::Value::String("hello cascade".into()));
        assert_eq!(result.call_id, Some("req-001".into()));
    }

    #[tokio::test]
    async fn tool_integration_error_path() {
        let tool = MockEchoTool;
        let call = ToolCall {
            name: "echo".into(),
            arguments: serde_json::json!({}), // missing required 'text'
            call_id: None,
        };
        let err = tool.call(call).await.unwrap_err();
        assert!(matches!(err, ToolIntegrationError::Parse { .. }));
    }

    #[test]
    fn schema_is_valid_json_schema_object() {
        let tool = MockEchoTool;
        let schema = tool.schema();
        assert_eq!(schema["type"], "object");
        assert!(schema["properties"]["text"]["type"].is_string());
        let required = schema["required"].as_array().unwrap();
        assert!(required.iter().any(|v| v == "text"));
    }

    #[test]
    fn tool_call_serde_round_trip() {
        let call = ToolCall {
            name: "search".into(),
            arguments: serde_json::json!({ "query": "how to use wasmtime" }),
            call_id: Some("trace-42".into()),
        };
        let json = serde_json::to_string(&call).unwrap();
        let decoded: ToolCall = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.name, "search");
        assert_eq!(decoded.arguments["query"], "how to use wasmtime");
        assert_eq!(decoded.call_id, Some("trace-42".into()));
    }

    #[test]
    fn tool_result_serde_round_trip() {
        let result = ToolResult {
            result: serde_json::json!({ "matches": 3 }),
            is_error: false,
            call_id: Some("trace-42".into()),
        };
        let json = serde_json::to_string(&result).unwrap();
        let decoded: ToolResult = serde_json::from_str(&json).unwrap();
        assert!(!decoded.is_error);
        assert_eq!(decoded.result["matches"], 3);
    }

    #[test]
    fn tool_error_serde_round_trip() {
        let err = ToolIntegrationError::Io {
            message: "connection refused".into(),
        };
        let json = serde_json::to_string(&err).unwrap();
        let decoded: ToolIntegrationError = serde_json::from_str(&json).unwrap();
        let msg = decoded.to_string();
        assert!(msg.contains("connection refused"));
    }

    #[test]
    fn tool_call_roundtrip_preserves_json_values() {
        let original_args = serde_json::json!({
            "query": "cascade config",
            "limit": 5,
            "filters": ["rust", "toml"]
        });
        let call = ToolCall {
            name: "search".into(),
            arguments: original_args.clone(),
            call_id: None,
        };
        let json = serde_json::to_string(&call).unwrap();
        let decoded: ToolCall = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.arguments, original_args);
    }

    #[test]
    fn trait_object_usable_as_box() {
        let _tool: Box<dyn ToolIntegration> = Box::new(MockEchoTool);
    }
}
