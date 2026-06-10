//! # MCP 2025-03-26 capability and server-info types
//!
//! Defines the types exchanged during the MCP `initialize` handshake.
//! All types use `#[serde(rename_all = "camelCase")]` to match MCP JSON field
//! names exactly (e.g. `protocolVersion`, `listChanged`).
//!
//! ## Constant
//!
//! [`MCP_PROTOCOL_VERSION`] — the spec string `"2025-03-26"`.

use serde::{Deserialize, Serialize};
use serde_json::Value;

// ── Protocol version ──────────────────────────────────────────────────────────

/// The MCP protocol version implemented by this crate.
///
/// Clients and servers must advertise this string in [`InitializeParams`] /
/// [`InitializeResult`] respectively.
pub const MCP_PROTOCOL_VERSION: &str = "2025-03-26";

// ── Implementation info ───────────────────────────────────────────────────────

/// Identifies a client or server implementation (name + version).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Implementation {
    /// Human-readable implementation name.
    pub name: String,
    /// Semver version string.
    pub version: String,
}

// ── Capability sub-structs ────────────────────────────────────────────────────

/// Server capability: prompts list-changed notifications.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptsCapability {
    /// Server will emit `notifications/prompts/list_changed` when the prompt list changes.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub list_changed: Option<bool>,
}

/// Server capability: resource subscriptions and list-changed notifications.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ResourcesCapability {
    /// Server supports `resources/subscribe` / `resources/unsubscribe`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub subscribe: Option<bool>,
    /// Server will emit `notifications/resources/list_changed`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub list_changed: Option<bool>,
}

/// Server capability: tools list-changed notifications.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolsCapability {
    /// Server will emit `notifications/tools/list_changed`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub list_changed: Option<bool>,
}

/// Client capability: roots list-changed notifications.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RootsCapability {
    /// Client will emit `notifications/roots/list_changed`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub list_changed: Option<bool>,
}

// ── ServerCapabilities ────────────────────────────────────────────────────────

/// Capabilities advertised by the MCP server during initialization.
///
/// All fields are optional. A server starts with all `None` (no capabilities)
/// and adds them as crate features register.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ServerCapabilities {
    /// Structured logging support (`logging/setLevel`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub logging: Option<Value>,
    /// Prompt list and retrieval support.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub prompts: Option<PromptsCapability>,
    /// Resource list, read, and subscription support.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub resources: Option<ResourcesCapability>,
    /// Tool list and invocation support.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tools: Option<ToolsCapability>,
    /// Vendor-specific experimental capabilities.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub experimental: Option<Value>,
}

// ── ClientCapabilities ────────────────────────────────────────────────────────

/// Capabilities advertised by the MCP client during initialization.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ClientCapabilities {
    /// File-system roots the client exposes to the server.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roots: Option<RootsCapability>,
    /// LLM sampling capability (client can forward sampling requests).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sampling: Option<Value>,
    /// Vendor-specific experimental capabilities.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub experimental: Option<Value>,
}

// ── Initialize handshake ──────────────────────────────────────────────────────

/// Parameters for the `initialize` request (sent by the client).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeParams {
    /// MCP protocol version the client supports (should be [`MCP_PROTOCOL_VERSION`]).
    pub protocol_version: String,
    /// Client capability declaration.
    pub capabilities: ClientCapabilities,
    /// Client implementation identity.
    pub client_info: Implementation,
}

/// Result of the `initialize` request (sent by the server).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeResult {
    /// MCP protocol version the server is using.
    pub protocol_version: String,
    /// Server capability declaration.
    pub capabilities: ServerCapabilities,
    /// Server implementation identity.
    pub server_info: Implementation,
    /// Optional human-readable instructions for using this server.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub instructions: Option<String>,
}

impl InitializeResult {
    /// Construct an `InitializeResult` with the canonical protocol version.
    pub fn new(capabilities: ServerCapabilities, server_info: Implementation) -> Self {
        Self {
            protocol_version: MCP_PROTOCOL_VERSION.into(),
            capabilities,
            server_info,
            instructions: None,
        }
    }
}

// ── Lifecycle messages ────────────────────────────────────────────────────────

/// Params for the `notifications/initialized` notification (no fields).
///
/// Sent by the client after it has processed the `initialize` result.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct InitializedNotificationParams {}

/// Params for the `shutdown` request (no fields).
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct ShutdownParams {}

/// Result of the `shutdown` request (empty).
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct ShutdownResult {}

/// Params for the `ping` request (no fields).
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct PingParams {}

/// Response to `ping` (empty — presence of response is the pong).
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct PongResult {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mcp_protocol_version_constant() {
        assert_eq!(MCP_PROTOCOL_VERSION, "2025-03-26");
    }

    #[test]
    fn initialize_params_camelcase_round_trip() {
        let params = InitializeParams {
            protocol_version: MCP_PROTOCOL_VERSION.into(),
            capabilities: ClientCapabilities {
                roots: Some(RootsCapability {
                    list_changed: Some(true),
                }),
                sampling: None,
                experimental: None,
            },
            client_info: Implementation {
                name: "cascade".into(),
                version: "0.1.0".into(),
            },
        };

        let json_str = serde_json::to_string(&params).unwrap();

        // Verify camelCase field names in the wire format
        assert!(json_str.contains("protocolVersion"), "expected protocolVersion in: {json_str}");
        assert!(json_str.contains("clientInfo"), "expected clientInfo in: {json_str}");
        assert!(json_str.contains("listChanged"), "expected listChanged in: {json_str}");
        // snake_case must NOT appear
        assert!(!json_str.contains("protocol_version"), "snake_case leaked: {json_str}");
        assert!(!json_str.contains("client_info"), "snake_case leaked: {json_str}");

        // Round-trip
        let back: InitializeParams = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back, params);
    }

    #[test]
    fn initialize_result_camelcase_round_trip() {
        let result = InitializeResult {
            protocol_version: MCP_PROTOCOL_VERSION.into(),
            capabilities: ServerCapabilities {
                tools: Some(ToolsCapability {
                    list_changed: Some(false),
                }),
                ..Default::default()
            },
            server_info: Implementation {
                name: "cascade-mcp".into(),
                version: "0.1.0".into(),
            },
            instructions: Some("Use cascade MCP for context routing.".into()),
        };

        let json_str = serde_json::to_string(&result).unwrap();

        assert!(json_str.contains("protocolVersion"), "expected protocolVersion in: {json_str}");
        assert!(json_str.contains("serverInfo"), "expected serverInfo in: {json_str}");
        // None fields must be absent (skip_serializing_if = None)
        assert!(!json_str.contains("\"resources\""), "null resources must be absent: {json_str}");
        assert!(!json_str.contains("\"prompts\""), "null prompts must be absent: {json_str}");

        let back: InitializeResult = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back, result);
    }

    #[test]
    fn server_capabilities_default_is_empty() {
        let caps = ServerCapabilities::default();
        let json_str = serde_json::to_string(&caps).unwrap();
        // All None fields omitted — result is empty object
        assert_eq!(json_str, "{}", "default ServerCapabilities must serialize to {{}}");
    }

    #[test]
    fn initialize_result_helper_uses_canonical_version() {
        let result = InitializeResult::new(
            ServerCapabilities::default(),
            Implementation { name: "test".into(), version: "1.0.0".into() },
        );
        assert_eq!(result.protocol_version, MCP_PROTOCOL_VERSION);
    }
}
