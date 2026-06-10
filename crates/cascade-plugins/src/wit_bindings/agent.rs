//! WIT binding types for the `agent` interface (mirrors `wit/agent.wit`).
//!
//! Constraints: must round-trip through JSON without field loss.
//! SPORT: cascade-plugins / WIT bindings

use serde::{Deserialize, Serialize};

/// Mirrors WIT `record context-entry`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ContextEntry {
    pub key: String,
    pub value: String,
}

/// Mirrors WIT `record agent-context`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AgentContext {
    pub session_id: String,
    pub metadata: Vec<ContextEntry>,
    pub history: Vec<String>,
}

/// Mirrors WIT `record tool-call-request`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ToolCallRequest {
    pub call_id: String,
    pub tool_name: String,
    pub args_json: String,
}

/// Mirrors WIT `record agent-response`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AgentResponse {
    pub text: String,
    pub tool_calls: Vec<ToolCallRequest>,
    pub is_final: bool,
}

/// Mirrors WIT `variant agent-error`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum AgentError {
    EmptyPrompt,
    ResourceExhausted,
    Internal { message: String },
}
