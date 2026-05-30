//! MCP Prompts — registered prompt templates for common Cascade workflows.
//!
//! Prompts are pre-built templates with named parameters that clients
//! (Claude Code, Claude Desktop) can invoke to get structured context
//! injection into a conversation.
//!
//! ## Registered prompts
//!
//! | Name | Description |
//! |------|-------------|
//! | `cascade/search` | Search context injection template |
//! | `cascade/tier_context` | Load tier instructions into conversation |
//! | `cascade/code_review` | Code review with codebase context |
//! | `cascade/inbox_triage` | Inbox message triage workflow |
//!
//! ## MCP spec
//!
//! - `prompts/list` → list all prompts with argument schemas
//! - `prompts/get` → instantiate a prompt with arguments

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use crate::server::JsonRpcError;

// ── Prompt types ──────────────────────────────────────────────────────────────

/// A prompt argument definition.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptArgument {
    pub name: String,
    pub description: String,
    pub required: bool,
}

/// A registered MCP prompt.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct McpPrompt {
    pub name: String,
    pub description: String,
    pub arguments: Vec<PromptArgument>,
}

/// An instantiated prompt message.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptMessage {
    pub role: String,
    pub content: PromptContent,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptContent {
    #[serde(rename = "type")]
    pub kind: String,
    pub text: String,
}

// ── PromptRegistry ────────────────────────────────────────────────────────────

/// Handles `prompts/list` and `prompts/get` for the MCP server.
///
/// Prompts are static (defined in source); no dynamic registration at runtime.
pub struct PromptRegistry;

impl PromptRegistry {
    /// Handle `prompts/list` as a static function.
    ///
    /// Returns JSON `{ "prompts": [...] }`.
    pub fn list_static() -> std::result::Result<Value, JsonRpcError> {
        let prompts = vec![
            McpPrompt {
                name: "cascade/search".into(),
                description:
                    "Search the Cascade corpus and inject relevant context into the conversation."
                        .into(),
                arguments: vec![
                    PromptArgument {
                        name: "query".into(),
                        description: "What to search for".into(),
                        required: true,
                    },
                    PromptArgument {
                        name: "project".into(),
                        description: "Limit search to this project".into(),
                        required: false,
                    },
                ],
            },
            McpPrompt {
                name: "cascade/tier_context".into(),
                description: "Load a cascade tier instruction file into the conversation context."
                    .into(),
                arguments: vec![PromptArgument {
                    name: "tier".into(),
                    description: "Tier identifier (gci/asi/ppc/prc/pac)".into(),
                    required: true,
                }],
            },
            McpPrompt {
                name: "cascade/code_review".into(),
                description: "Start a code review session with codebase context pre-loaded.".into(),
                arguments: vec![
                    PromptArgument {
                        name: "project".into(),
                        description: "Project to load context from".into(),
                        required: true,
                    },
                    PromptArgument {
                        name: "focus".into(),
                        description: "File or module to focus on".into(),
                        required: false,
                    },
                ],
            },
            McpPrompt {
                name: "cascade/inbox_triage".into(),
                description: "Triage PCI inbox messages for a project.".into(),
                arguments: vec![PromptArgument {
                    name: "project".into(),
                    description: "Project to triage inbox for".into(),
                    required: true,
                }],
            },
        ];

        Ok(serde_json::json!({ "prompts": prompts }))
    }

    /// Handle `prompts/get` as a static function.
    ///
    /// Params: `{ "name": "cascade/search", "arguments": { "query": "..." } }`
    /// Returns: `{ "description": "...", "messages": [...] }`
    pub fn get_static(params: &Value) -> std::result::Result<Value, JsonRpcError> {
        let name = params
            .get("name")
            .and_then(|v| v.as_str())
            .ok_or_else(|| JsonRpcError::invalid_params("missing 'name' in prompts/get"))?;

        let args = params.get("arguments").cloned().unwrap_or_default();
        debug!(prompt = name, "prompts/get");

        match name {
            "cascade/search" => {
                let query = args
                    .get("query")
                    .and_then(|v| v.as_str())
                    .unwrap_or("(no query)");
                let project = args
                    .get("project")
                    .and_then(|v| v.as_str())
                    .unwrap_or("all projects");
                Ok(serde_json::json!({
                    "description": format!("Search cascade corpus for '{query}' in {project}"),
                    "messages": [{
                        "role": "user",
                        "content": {
                            "type": "text",
                            "text": format!("Please search the Cascade corpus for '{}' (project: {}) and summarize the most relevant results.", query, project)
                        }
                    }]
                }))
            }
            "cascade/tier_context" => {
                let tier = args.get("tier").and_then(|v| v.as_str()).unwrap_or("gci");
                Ok(serde_json::json!({
                    "description": format!("Load {tier} tier instructions"),
                    "messages": [{
                        "role": "user",
                        "content": {
                            "type": "text",
                            "text": format!("Load and summarize the '{}' cascade tier instructions.", tier)
                        }
                    }]
                }))
            }
            "cascade/code_review" => {
                let project = args
                    .get("project")
                    .and_then(|v| v.as_str())
                    .unwrap_or("(project)");
                let focus = args
                    .get("focus")
                    .and_then(|v| v.as_str())
                    .unwrap_or("(all files)");
                Ok(serde_json::json!({
                    "description": format!("Code review for {project}/{focus}"),
                    "messages": [{
                        "role": "user",
                        "content": {
                            "type": "text",
                            "text": format!("Review the code in '{}' for project '{}'. Use cascade.search_codebase to load context, then provide a structured review.", focus, project)
                        }
                    }]
                }))
            }
            "cascade/inbox_triage" => {
                let project = args
                    .get("project")
                    .and_then(|v| v.as_str())
                    .unwrap_or("(project)");
                Ok(serde_json::json!({
                    "description": format!("Triage inbox for {project}"),
                    "messages": [{
                        "role": "user",
                        "content": {
                            "type": "text",
                            "text": format!("Use cascade.inbox.list to fetch messages for '{}', then triage each message: classify by type and priority, suggest action.", project)
                        }
                    }]
                }))
            }
            _ => Err(JsonRpcError::not_found(format!("Prompt not found: {name}"))),
        }
    }
}
