//! McpServer error type.
//!
//! ## Purpose
//! Provides a typed error surface for McpServer operations, separate from the
//! JSON-RPC wire-level [`JsonRpcError`] carried in protocol responses.
//!
//! ## Inputs / Outputs
//! Conversion impls map `McpServerError` → [`cascade_types::mcp::JsonRpcError`] for
//! use in protocol responses.
//!
//! ## Constraints
//! - Uses `thiserror` for automatic `Display` + `Error` impls.
//! - Never carries sensitive data (keys, tokens) in error messages.
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-mcp — error.rs

use thiserror::Error;

use cascade_types::mcp::{ErrorCode, JsonRpcError};

/// Errors that can occur during McpServer operation.
#[derive(Debug, Error)]
pub enum McpServerError {
    /// Client sent an unrecognised or unsupported protocol version.
    #[error("Unsupported protocol version: {version}")]
    UnsupportedProtocolVersion { version: String },

    /// A request arrived in an invalid server state (e.g. resource request before initialized).
    #[error("Method {method} not allowed in current server state")]
    WrongState { method: String },

    /// The requested JSON-RPC method is not registered.
    #[error("Method not found: {method}")]
    MethodNotFound { method: String },

    /// Request parameters could not be parsed or are invalid.
    #[error("Invalid params: {detail}")]
    InvalidParams { detail: String },

    /// Internal server error.
    #[error("Internal error: {detail}")]
    Internal { detail: String },

    /// Re-initialization attempt (spec prohibits it).
    #[error("Server already initialized — re-initialization is not allowed")]
    AlreadyInitialized,

    /// Authentication failed.
    #[error("Authentication failed: {detail}")]
    AuthFailed { detail: String },

    /// The URI is a known resource kind, but nothing backs it right now
    /// (for example a tier file that does not exist on this machine).
    ///
    /// Distinct from `InvalidParams`, which means the URI itself is not a
    /// resource this server serves at all.
    #[error("Resource not found: {uri}")]
    ResourceNotFound { uri: String },
}

impl From<McpServerError> for JsonRpcError {
    fn from(e: McpServerError) -> Self {
        match &e {
            McpServerError::UnsupportedProtocolVersion { .. } => {
                JsonRpcError::new(ErrorCode::InvalidRequest, e.to_string())
            }
            McpServerError::WrongState { .. } => {
                JsonRpcError::new(ErrorCode::InvalidRequest, e.to_string())
            }
            McpServerError::MethodNotFound { .. } => {
                JsonRpcError::new(ErrorCode::MethodNotFound, e.to_string())
            }
            McpServerError::InvalidParams { .. } => {
                JsonRpcError::new(ErrorCode::InvalidParams, e.to_string())
            }
            McpServerError::Internal { .. } => {
                JsonRpcError::new(ErrorCode::InternalError, e.to_string())
            }
            McpServerError::AlreadyInitialized => {
                JsonRpcError::new(ErrorCode::InvalidRequest, e.to_string())
            }
            McpServerError::AuthFailed { .. } => {
                JsonRpcError::new(ErrorCode::Unauthorized, e.to_string())
            }
            McpServerError::ResourceNotFound { .. } => {
                JsonRpcError::new(ErrorCode::ResourceNotFound, e.to_string())
            }
        }
    }
}
