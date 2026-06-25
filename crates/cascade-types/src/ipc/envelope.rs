// FROZEN — schema version 1. See parent module for protocol contract notes.

//! Core JSON-RPC 2.0 envelope types: version discriminator, request id,
//! request/response envelopes, and error object.

use serde::{Deserialize, Serialize};

// ── Protocol version ──────────────────────────────────────────────────────────

/// Bump this when a non-backward-compatible schema change is unavoidable.
/// Clients can reject connections from daemons with a different `protocol_version`.
pub const PROTOCOL_VERSION: u8 = 1;

// ── Error code constants ──────────────────────────────────────────────────────

/// JSON-RPC 2.0 standard: no handler registered for the requested method.
pub const METHOD_NOT_FOUND: i32 = -32601;

/// JSON-RPC 2.0 standard: params failed validation.
pub const INVALID_PARAMS: i32 = -32602;

/// JSON-RPC 2.0 standard: unhandled daemon-side error.
pub const INTERNAL_ERROR: i32 = -32603;

/// Cascade extension: client tried to reach the daemon but it is not running.
pub const DAEMON_NOT_RUNNING: i32 = -32001;

/// Cascade extension: auth token missing or invalid (populated by T-P2-E03-04).
pub const AUTH_FAILED: i32 = -32002;

/// Cascade extension: the requested resource does not exist.
pub const RESOURCE_NOT_FOUND: i32 = -32003;

// ── Core JSON-RPC 2.0 envelope types ─────────────────────────────────────────

/// The `jsonrpc` version discriminator.
///
/// Always serialises to the string `"2.0"`. Deserialisation rejects any other value.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum JsonRpcVersion {
    /// JSON-RPC protocol version 2.0.
    #[serde(rename = "2.0")]
    V2_0,
}

/// A JSON-RPC 2.0 request `id`.
///
/// Per spec, the id may be a number, a string, or null (for notifications).
/// Framing-level code rejects absent `id` fields — all Cascade requests are
/// call-style (non-notification) so the field must be present.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RequestId {
    /// Numeric id (most common; counters start at 1).
    Number(i64),
    /// String id (useful for correlation with user-visible labels).
    String(String),
    /// Null id (notifications; the daemon sends no response).
    Null,
}

/// A JSON-RPC 2.0 request envelope.
///
/// `P` is the method-specific params struct. Use `serde_json::Value` when the
/// params type is unknown at parse time.
///
/// # Example
///
/// ```rust
/// use cascade_types::ipc::{JsonRpcVersion, Request, RequestId, PingParams, PROTOCOL_VERSION};
/// use serde_json;
///
/// let req = Request {
///     jsonrpc: JsonRpcVersion::V2_0,
///     id: RequestId::Number(1),
///     method: "ping".to_string(),
///     params: Some(PingParams { echo: Some("hello".to_string()) }),
///     protocol_version: PROTOCOL_VERSION,
/// };
/// let json = serde_json::to_string(&req).unwrap();
/// let back: Request<PingParams> = serde_json::from_str(&json).unwrap();
/// assert_eq!(back.id, RequestId::Number(1));
/// ```
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request<P> {
    /// Always `"2.0"`.
    pub jsonrpc: JsonRpcVersion,
    /// Correlation id. Must be present for all Cascade call-style requests.
    pub id: RequestId,
    /// Method name, e.g. `"ping"` or `"cascade.status"`.
    pub method: String,
    /// Method-specific parameters; `None` for methods that take no params.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<P>,
    /// Schema version. Clients set this to [`PROTOCOL_VERSION`].
    /// Daemons may reject mismatches.
    pub protocol_version: u8,
}

/// A JSON-RPC 2.0 response envelope.
///
/// `R` is the method-specific result struct. Per spec, exactly one of `result`
/// and `error` is populated; never both.
///
/// # Example
///
/// ```rust
/// use cascade_types::ipc::{JsonRpcVersion, Response, RequestId, PingResult};
/// use serde_json;
///
/// let resp = Response {
///     jsonrpc: JsonRpcVersion::V2_0,
///     id: RequestId::Number(1),
///     result: Some(PingResult { pong: "hello".to_string() }),
///     error: None,
/// };
/// let json = serde_json::to_string(&resp).unwrap();
/// let back: Response<PingResult> = serde_json::from_str(&json).unwrap();
/// assert_eq!(back.result.unwrap().pong, "hello");
/// ```
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Response<R> {
    /// Always `"2.0"`.
    pub jsonrpc: JsonRpcVersion,
    /// Echoed from the corresponding request.
    pub id: RequestId,
    /// Present on success; absent on error.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<R>,
    /// Present on error; absent on success.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<RpcError>,
}

/// A JSON-RPC 2.0 error object.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RpcError {
    /// Numeric error code. Use the `*` constants defined in this module.
    pub code: i32,
    /// Human-readable error message. Never expose internal stack traces here.
    pub message: String,
    /// Optional structured detail; schema is method-specific.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<serde_json::Value>,
}
