//! WIT binding types for the `tool-integration` interface (mirrors `wit/tool-integration.wit`).
//!
//! Constraints: must round-trip through JSON without field loss.
//! SPORT: cascade-plugins / WIT bindings

use serde::{Deserialize, Serialize};

/// Mirrors WIT `record tool-call`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ToolCall {
    pub call_id: String,
    pub tool_name: String,
    pub args_json: String,
}

/// Mirrors WIT `record tool-result`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ToolResult {
    pub call_id: String,
    pub output: String,
    pub data_json: Option<String>,
}

/// Mirrors WIT `variant tool-error`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ToolError {
    UnknownTool { name: String },
    InvalidArgs { reason: String },
    PermissionDenied { reason: String },
    ResourceExhausted,
    Internal { message: String },
}
