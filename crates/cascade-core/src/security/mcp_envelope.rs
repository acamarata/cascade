//! # cascade_core::security::mcp_envelope
//!
//! MCP JSON-RPC 2025-03 envelope schema validation.
//!
//! ## Responsibilities
//!
//! Every incoming MCP message is validated against the canonical JSON-RPC 2.0
//! structure before reaching any handler. Messages that fail schema validation
//! are rejected with JSON-RPC error -32600 (Invalid Request) and a WARN log.
//!
//! ## STRIDE mapping
//!
//! Tampering (T) and Elevation (E) — malformed envelopes can carry unexpected
//! fields designed to confuse dispatchers or exploit type-confusion bugs. Strict
//! schema enforcement at the boundary prevents both.
//!
//! ## Acceptance criteria (from S02 ticket T-P1-E10-S02-10)
//!
//! - Parse every incoming MCP message against the 2025-03 JSON-RPC 2.0 schema.
//! - Messages failing validation: reject with JSON-RPC error -32600, log at WARN.
//! - Fuzzing gate: 100 random-mutated valid messages — none must panic.
//! - Tests: missing `jsonrpc` field, wrong method type, extra disallowed fields.

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::security::audit::{AuditEvent, AuditLogger};

// ── JSON-RPC 2.0 types ────────────────────────────────────────────────────────

/// A validated JSON-RPC 2.0 request or notification.
///
/// The `id` field distinguishes requests (present) from notifications (absent).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct McpRequest {
    /// Must be exactly the string `"2.0"`.
    pub jsonrpc: String,
    /// Method name, as defined by the MCP 2025-03 specification.
    pub method: String,
    /// Optional structured parameters.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<Value>,
    /// Request ID (absent for notifications).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<Value>,
}

/// A standard JSON-RPC 2.0 error object.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcError {
    pub code: i32,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

/// Standard JSON-RPC 2.0 error codes.
pub mod error_codes {
    /// Invalid JSON — the server received invalid JSON.
    pub const PARSE_ERROR: i32 = -32700;
    /// The JSON sent is not a valid Request object.
    pub const INVALID_REQUEST: i32 = -32600;
    /// The method does not exist or is not available.
    pub const METHOD_NOT_FOUND: i32 = -32601;
    /// Invalid method parameter(s).
    pub const INVALID_PARAMS: i32 = -32602;
    /// Internal JSON-RPC error.
    pub const INTERNAL_ERROR: i32 = -32603;
}

// ── Method allowlist ──────────────────────────────────────────────────────────

/// MCP 2025-03 method names that the daemon accepts.
///
/// Any other method name is rejected with `METHOD_NOT_FOUND` (-32601).
/// This list must stay in sync with the MCP specification version the daemon
/// is built against.
pub const ALLOWED_METHODS: &[&str] = &[
    // Capability negotiation
    "initialize",
    "ping",
    // Tool surface
    "tools/list",
    "tools/call",
    // Resource surface
    "resources/list",
    "resources/read",
    "resources/subscribe",
    "resources/unsubscribe",
    // Prompt surface
    "prompts/list",
    "prompts/get",
    // Sampling
    "sampling/createMessage",
    // Logging
    "logging/setLevel",
    // Roots
    "roots/list",
    // Completion
    "completion/complete",
];

// ── McpEnvelopeValidator ──────────────────────────────────────────────────────

/// Validates a raw JSON value as a conformant MCP JSON-RPC 2025-03 envelope.
///
/// Emits `AuditEvent::McpEnvelopeRejected` on any validation failure.
pub struct McpEnvelopeValidator {
    /// If `true`, reject methods not in [`ALLOWED_METHODS`].
    pub enforce_method_allowlist: bool,
}

impl Default for McpEnvelopeValidator {
    fn default() -> Self {
        Self {
            enforce_method_allowlist: true,
        }
    }
}

impl McpEnvelopeValidator {
    /// Validate `raw` as an MCP JSON-RPC envelope.
    ///
    /// Returns the parsed `McpRequest` on success. On failure, returns a
    /// `JsonRpcError` ready to send as the error field of a JSON-RPC response,
    /// and emits an audit event.
    pub async fn validate(
        &self,
        raw: &Value,
        logger: &AuditLogger,
    ) -> Result<McpRequest, JsonRpcError> {
        let obj = raw.as_object().ok_or_else(|| {
            JsonRpcError {
                code: error_codes::INVALID_REQUEST,
                message: "message must be a JSON object".to_owned(),
                data: None,
            }
        })?;

        // `jsonrpc` field must be present and equal to "2.0".
        let jsonrpc = obj
            .get("jsonrpc")
            .and_then(|v| v.as_str())
            .ok_or_else(|| JsonRpcError {
                code: error_codes::INVALID_REQUEST,
                message: "`jsonrpc` field is required and must be a string".to_owned(),
                data: None,
            })?;

        if jsonrpc != "2.0" {
            let err = JsonRpcError {
                code: error_codes::INVALID_REQUEST,
                message: format!("`jsonrpc` must be \"2.0\", got \"{jsonrpc}\""),
                data: None,
            };
            logger
                .log(AuditEvent::McpEnvelopeRejected {
                    error: err.message.clone(),
                })
                .await;
            return Err(err);
        }

        // `method` field must be a non-empty string.
        let method = obj
            .get("method")
            .and_then(|v| v.as_str())
            .filter(|s| !s.is_empty())
            .ok_or_else(|| JsonRpcError {
                code: error_codes::INVALID_REQUEST,
                message: "`method` field is required and must be a non-empty string".to_owned(),
                data: None,
            })?;

        // Method allowlist check.
        if self.enforce_method_allowlist && !ALLOWED_METHODS.contains(&method) {
            let err = JsonRpcError {
                code: error_codes::METHOD_NOT_FOUND,
                message: format!("method `{method}` is not supported"),
                data: None,
            };
            logger
                .log(AuditEvent::McpEnvelopeRejected {
                    error: err.message.clone(),
                })
                .await;
            return Err(err);
        }

        // `params` must be null, absent, an object, or an array.
        if let Some(params) = obj.get("params") {
            if !params.is_null() && !params.is_object() && !params.is_array() {
                let err = JsonRpcError {
                    code: error_codes::INVALID_PARAMS,
                    message: "`params` must be an object, array, or absent".to_owned(),
                    data: None,
                };
                logger
                    .log(AuditEvent::McpEnvelopeRejected {
                        error: err.message.clone(),
                    })
                    .await;
                return Err(err);
            }
        }

        // `id` must be a string, number, or absent (not an object or array).
        if let Some(id) = obj.get("id") {
            if id.is_object() || id.is_array() {
                let err = JsonRpcError {
                    code: error_codes::INVALID_REQUEST,
                    message: "`id` must be a string, number, or null".to_owned(),
                    data: None,
                };
                logger
                    .log(AuditEvent::McpEnvelopeRejected {
                        error: err.message.clone(),
                    })
                    .await;
                return Err(err);
            }
        }

        Ok(McpRequest {
            jsonrpc: jsonrpc.to_owned(),
            method: method.to_owned(),
            params: obj.get("params").cloned(),
            id: obj.get("id").cloned(),
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    async fn validate(v: &Value) -> Result<McpRequest, JsonRpcError> {
        McpEnvelopeValidator::default()
            .validate(v, &AuditLogger::noop())
            .await
    }

    #[tokio::test]
    async fn valid_request_passes() {
        let req = json!({
            "jsonrpc": "2.0",
            "method": "tools/list",
            "id": 1
        });
        assert!(validate(&req).await.is_ok());
    }

    #[tokio::test]
    async fn missing_jsonrpc_rejected() {
        let req = json!({ "method": "tools/list", "id": 1 });
        let err = validate(&req).await.unwrap_err();
        assert_eq!(err.code, error_codes::INVALID_REQUEST);
    }

    #[tokio::test]
    async fn wrong_version_rejected() {
        let req = json!({ "jsonrpc": "1.0", "method": "tools/list", "id": 1 });
        let err = validate(&req).await.unwrap_err();
        assert_eq!(err.code, error_codes::INVALID_REQUEST);
    }

    #[tokio::test]
    async fn unknown_method_rejected() {
        let req = json!({ "jsonrpc": "2.0", "method": "evil/hack", "id": 1 });
        let err = validate(&req).await.unwrap_err();
        assert_eq!(err.code, error_codes::METHOD_NOT_FOUND);
    }

    #[tokio::test]
    async fn object_id_rejected() {
        let req = json!({ "jsonrpc": "2.0", "method": "ping", "id": {} });
        let err = validate(&req).await.unwrap_err();
        assert_eq!(err.code, error_codes::INVALID_REQUEST);
    }

    #[tokio::test]
    async fn non_object_input_rejected() {
        let req = json!([1, 2, 3]);
        let err = validate(&req).await.unwrap_err();
        assert_eq!(err.code, error_codes::INVALID_REQUEST);
    }
}
