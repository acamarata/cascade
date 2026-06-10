//! # MCP 2025-03-26 JSON-RPC 2.0 base types
//!
//! Implements the JSON-RPC 2.0 wire types required by the Model Context Protocol
//! specification (2025-03-26). All types serialize to/from spec-compliant JSON.
//!
//! ## Types
//!
//! | Type | Purpose |
//! |------|---------|
//! | [`RequestId`] | String, number, or null per JSON-RPC 2.0 §4 |
//! | [`JsonRpcRequest`] | Request object with id + method + params |
//! | [`JsonRpcResponse`] | Response with id + result or error |
//! | [`JsonRpcNotification`] | Notification without id |
//! | [`JsonRpcError`] | Error object with code + message + data |
//! | [`ErrorCode`] | Spec error codes + Cascade extensions |
//! | [`McpMessage`] | Top-level union over the three message kinds |

use serde::{Deserialize, Serialize};
use serde_json::Value;

// ── RequestId ─────────────────────────────────────────────────────────────────

/// JSON-RPC 2.0 request identifier.
///
/// Per spec §4, an id MUST be a String, Number, or null. The `#[serde(untagged)]`
/// attribute ensures correct round-trip through JSON: `"foo"` → `String`,
/// `42` / `42.0` → `Number`, `null` → `Null`.
///
/// Note: JSON-RPC 2.0 discourages fractional numbers for ids; we store as `i64`.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RequestId {
    /// String id, e.g. `"req-1"`.
    String(String),
    /// Numeric id, e.g. `42`. JSON numbers are coerced to i64.
    Number(i64),
    /// Null id (used in error responses where original id cannot be determined).
    #[default]
    Null,
}

// ── JsonRpcRequest ────────────────────────────────────────────────────────────

/// JSON-RPC 2.0 Request object (spec §4.1).
///
/// The `jsonrpc` field is always `"2.0"`. `params` is generic and may be omitted.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct JsonRpcRequest<P = Value> {
    /// JSON-RPC version string — always `"2.0"`.
    pub jsonrpc: String,
    /// Request identifier. Must be present on requests (absent on notifications).
    pub id: RequestId,
    /// RPC method name, e.g. `"initialize"`.
    pub method: String,
    /// Method-specific parameters, omitted if not provided.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<P>,
}

impl<P: Serialize> JsonRpcRequest<P> {
    /// Construct a request with the given id, method and params.
    pub fn new(id: RequestId, method: impl Into<String>, params: Option<P>) -> Self {
        Self {
            jsonrpc: "2.0".into(),
            id,
            method: method.into(),
            params,
        }
    }
}

// ── JsonRpcResponse ───────────────────────────────────────────────────────────

/// JSON-RPC 2.0 Response object (spec §4.2).
///
/// Either `result` or `error` is present; never both.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct JsonRpcResponse<R = Value> {
    /// JSON-RPC version string — always `"2.0"`.
    pub jsonrpc: String,
    /// Mirrors the id from the corresponding request.
    pub id: RequestId,
    /// Success result — present on success, absent on error.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<R>,
    /// Error object — present on failure, absent on success.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<JsonRpcError>,
}

impl<R: Serialize> JsonRpcResponse<R> {
    /// Construct a success response.
    pub fn ok(id: RequestId, result: R) -> Self {
        Self {
            jsonrpc: "2.0".into(),
            id,
            result: Some(result),
            error: None,
        }
    }

    /// Construct an error response.
    pub fn err(id: RequestId, error: JsonRpcError) -> JsonRpcResponse<R> {
        JsonRpcResponse {
            jsonrpc: "2.0".into(),
            id,
            result: None,
            error: Some(error),
        }
    }
}

// ── JsonRpcNotification ───────────────────────────────────────────────────────

/// JSON-RPC 2.0 Notification object (spec §4.1 — no `id` field).
///
/// Notifications are fire-and-forget; the server MUST NOT reply.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct JsonRpcNotification<P = Value> {
    /// JSON-RPC version string — always `"2.0"`.
    pub jsonrpc: String,
    /// Notification method name.
    pub method: String,
    /// Optional notification parameters.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<P>,
}

impl<P: Serialize> JsonRpcNotification<P> {
    /// Construct a notification.
    pub fn new(method: impl Into<String>, params: Option<P>) -> Self {
        Self {
            jsonrpc: "2.0".into(),
            method: method.into(),
            params,
        }
    }
}

// ── JsonRpcError ──────────────────────────────────────────────────────────────

/// JSON-RPC 2.0 Error object (spec §4.2).
///
/// Carries a machine-readable `code`, a human-readable `message`, and an optional
/// `data` payload for additional context.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct JsonRpcError {
    /// Error code — use [`ErrorCode`] variants for standard values.
    pub code: i32,
    /// Short error description.
    pub message: String,
    /// Optional structured error data.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl JsonRpcError {
    /// Construct from an [`ErrorCode`] and a message string.
    pub fn new(code: ErrorCode, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
            data: None,
        }
    }

    /// Attach additional structured data.
    pub fn with_data(mut self, data: Value) -> Self {
        self.data = Some(data);
        self
    }
}

// ── ErrorCode ─────────────────────────────────────────────────────────────────

/// JSON-RPC 2.0 standard error codes (spec §5.1) plus Cascade-specific extensions.
///
/// Standard codes occupy the range -32768 to -32000.
/// Cascade-specific codes start at -32000 (implementation-defined range).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorCode {
    // ── JSON-RPC 2.0 standard ──────────────────────────────────────────────
    /// Parse error: invalid JSON received. Code `-32700`.
    ParseError,
    /// Invalid Request: the JSON sent is not a valid Request object. Code `-32600`.
    InvalidRequest,
    /// Method not found: the method does not exist or is not available. Code `-32601`.
    MethodNotFound,
    /// Invalid params: invalid method parameters. Code `-32602`.
    InvalidParams,
    /// Internal error: internal JSON-RPC error. Code `-32603`.
    InternalError,

    // ── Cascade extensions (-32000 range) ─────────────────────────────────
    /// The requested resource was not found. Code `-32000`.
    ResourceNotFound,
    /// The caller is not authorized for this operation. Code `-32001`.
    Unauthorized,
    /// The request was rate-limited. Code `-32002`.
    RateLimited,
}

impl From<ErrorCode> for i32 {
    fn from(code: ErrorCode) -> i32 {
        match code {
            ErrorCode::ParseError => -32700,
            ErrorCode::InvalidRequest => -32600,
            ErrorCode::MethodNotFound => -32601,
            ErrorCode::InvalidParams => -32602,
            ErrorCode::InternalError => -32603,
            ErrorCode::ResourceNotFound => -32000,
            ErrorCode::Unauthorized => -32001,
            ErrorCode::RateLimited => -32002,
        }
    }
}

// ── McpMessage ────────────────────────────────────────────────────────────────

/// Top-level MCP message union.
///
/// `#[serde(untagged)]` means disambiguation follows JSON-RPC 2.0 field rules:
/// - Request: has `id` + `method`
/// - Notification: has `method`, no `id`
/// - Response: has `id` + `result` or `error`
///
/// Serde tries variants in declaration order. `Request` must come before `Response`
/// because `Response` (with all-`Option` fields) would otherwise greedily match any
/// JSON object that carries an `id`. `Notification` before `Response` for the same
/// reason: a notification has `method` but no `id`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum McpMessage {
    /// JSON-RPC 2.0 request (has `id` + `method`).
    Request(JsonRpcRequest),
    /// JSON-RPC 2.0 notification (has `method`, no `id`).
    Notification(JsonRpcNotification),
    /// JSON-RPC 2.0 response (has `id` + `result`/`error`).
    Response(JsonRpcResponse),
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};

    // ── RequestId round-trips ─────────────────────────────────────────────

    #[test]
    fn request_id_string_round_trip() {
        let id = RequestId::String("req-abc".into());
        let json = serde_json::to_string(&id).unwrap();
        assert_eq!(json, r#""req-abc""#);
        let back: RequestId = serde_json::from_str(&json).unwrap();
        assert_eq!(back, id);
    }

    #[test]
    fn request_id_number_round_trip() {
        let id = RequestId::Number(42);
        let json = serde_json::to_string(&id).unwrap();
        assert_eq!(json, "42");
        let back: RequestId = serde_json::from_str(&json).unwrap();
        assert_eq!(back, id);
    }

    #[test]
    fn request_id_null_round_trip() {
        let id = RequestId::Null;
        let json = serde_json::to_string(&id).unwrap();
        assert_eq!(json, "null");
        let back: RequestId = serde_json::from_str(&json).unwrap();
        assert_eq!(back, id);
    }

    // ── JsonRpcRequest round-trip ─────────────────────────────────────────

    #[test]
    fn jsonrpc_request_round_trip() {
        let req: JsonRpcRequest<()> = JsonRpcRequest::new(RequestId::Number(1), "ping", None);
        let json = serde_json::to_string(&req).unwrap();
        let back: JsonRpcRequest<Value> = serde_json::from_str(&json).unwrap();
        assert_eq!(back.jsonrpc, "2.0");
        assert_eq!(back.id, RequestId::Number(1));
        assert_eq!(back.method, "ping");
    }

    #[test]
    fn jsonrpc_request_with_params_round_trip() {
        let params = json!({"foo": "bar"});
        let req = JsonRpcRequest::new(RequestId::String("x".into()), "test", Some(params.clone()));
        let json_str = serde_json::to_string(&req).unwrap();
        let back: JsonRpcRequest<Value> = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.params, Some(params));
    }

    // ── JsonRpcResponse round-trip ────────────────────────────────────────

    #[test]
    fn jsonrpc_response_ok_round_trip() {
        let resp = JsonRpcResponse::ok(RequestId::Number(1), json!({"status": "ok"}));
        let json_str = serde_json::to_string(&resp).unwrap();
        let back: JsonRpcResponse<Value> = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.jsonrpc, "2.0");
        assert_eq!(back.id, RequestId::Number(1));
        assert!(back.result.is_some());
        assert!(back.error.is_none());
    }

    #[test]
    fn jsonrpc_response_err_round_trip() {
        let err = JsonRpcError::new(ErrorCode::InternalError, "something went wrong");
        let resp: JsonRpcResponse<Value> = JsonRpcResponse::err(RequestId::Number(2), err);
        let json_str = serde_json::to_string(&resp).unwrap();
        let back: JsonRpcResponse<Value> = serde_json::from_str(&json_str).unwrap();
        assert!(back.result.is_none());
        let e = back.error.unwrap();
        assert_eq!(e.code, -32603);
        assert_eq!(e.message, "something went wrong");
    }

    // ── JsonRpcNotification round-trip ────────────────────────────────────

    #[test]
    fn jsonrpc_notification_round_trip() {
        let notif: JsonRpcNotification<Value> =
            JsonRpcNotification::new("notifications/initialized", None);
        let json_str = serde_json::to_string(&notif).unwrap();
        let back: JsonRpcNotification<Value> = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.method, "notifications/initialized");
        assert!(back.params.is_none());
        // Must not contain "id" field
        assert!(!json_str.contains("\"id\""));
    }

    // ── ErrorCode values match spec ───────────────────────────────────────

    #[test]
    fn error_code_values_match_spec() {
        assert_eq!(i32::from(ErrorCode::ParseError), -32700);
        assert_eq!(i32::from(ErrorCode::InvalidRequest), -32600);
        assert_eq!(i32::from(ErrorCode::MethodNotFound), -32601);
        assert_eq!(i32::from(ErrorCode::InvalidParams), -32602);
        assert_eq!(i32::from(ErrorCode::InternalError), -32603);
        // Cascade extensions
        assert_eq!(i32::from(ErrorCode::ResourceNotFound), -32000);
        assert_eq!(i32::from(ErrorCode::Unauthorized), -32001);
        assert_eq!(i32::from(ErrorCode::RateLimited), -32002);
    }

    #[test]
    fn mcp_error_parse_error_round_trip() {
        let err = JsonRpcError::new(ErrorCode::ParseError, "parse error");
        let json_str = serde_json::to_string(&err).unwrap();
        let back: JsonRpcError = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.code, -32700);
        assert_eq!(back.message, "parse error");
        assert!(back.data.is_none());
    }

    // ── McpMessage disambiguation ─────────────────────────────────────────

    #[test]
    fn mcp_message_request_disambiguation() {
        let json_str = r#"{"jsonrpc":"2.0","id":1,"method":"ping"}"#;
        let msg: McpMessage = serde_json::from_str(json_str).unwrap();
        assert!(matches!(msg, McpMessage::Request(_)));
    }

    #[test]
    fn mcp_message_response_disambiguation() {
        let json_str = r#"{"jsonrpc":"2.0","id":1,"result":{"ok":true}}"#;
        let msg: McpMessage = serde_json::from_str(json_str).unwrap();
        assert!(matches!(msg, McpMessage::Response(_)));
    }

    #[test]
    fn mcp_message_notification_disambiguation() {
        let json_str = r#"{"jsonrpc":"2.0","method":"notifications/initialized"}"#;
        let msg: McpMessage = serde_json::from_str(json_str).unwrap();
        assert!(matches!(msg, McpMessage::Notification(_)));
    }
}
