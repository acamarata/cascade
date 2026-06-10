//! MCP Prompts handler — `prompts/list` and `prompts/get`.
//!
//! ## Purpose
//! Implements the MCP prompts primitive. Exposes 3 built-in Cascade prompt
//! templates that clients (Claude Code, Claude Desktop) can invoke to get
//! structured context injection into a conversation.
//!
//! ## Inputs
//! - `prompts/list` — no params → `{ "prompts": [PromptInfo, ...] }`
//! - `prompts/get`  — `{ "name": "cascade-context", "arguments": { ... } }`
//!                  → `GetPromptResult { description, messages }`
//!
//! ## Outputs
//! - `GetPromptResult.messages` — one `PromptMessage` per turn (always role:user)
//! - `GetPromptResult.description` — human-readable summary of what was resolved
//!
//! ## Constraints
//! - Prompts are stateless templates; no IO at list time.
//! - Missing required argument → `JsonRpcError::invalid_params` (−32602).
//! - Unknown prompt name → `JsonRpcError::method_not_found` (−32601).
//! - Thread-safe: `PromptsHandler` is `Send + Sync`.
//!
//! ## SPORT
//! MASTER-MCP-PRIMITIVES.md: prompts handler — Done

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use crate::server::JsonRpcError;

// ── Prompt types ──────────────────────────────────────────────────────────────

/// Argument definition for a prompt template.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptArgument {
    pub name: String,
    pub description: String,
    pub required: bool,
}

/// Prompt metadata returned by `prompts/list`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptInfo {
    pub name: String,
    pub description: String,
    pub arguments: Vec<PromptArgument>,
}

/// A single message in an instantiated prompt.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptMessage {
    pub role: String,
    pub content: PromptTextContent,
}

/// Text content for a prompt message.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PromptTextContent {
    #[serde(rename = "type")]
    pub kind: String,
    pub text: String,
}

/// The return value from `prompts/get`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GetPromptResult {
    pub description: String,
    pub messages: Vec<PromptMessage>,
}

// ── Static prompt catalogue ───────────────────────────────────────────────────

/// The three built-in Cascade prompts.
static PROMPTS: &[(&str, &str, &[(&str, &str, bool)])] = &[
    (
        "cascade-context",
        "Inject Cascade tier instructions into the conversation. \
         Prepend the resolved instruction cascade (GCI → ASI → PPC → PRC) \
         for the specified tier and/or project into the current context.",
        &[
            ("tier", "Cascade tier to load (gci/asi/ppc/prc/pac). Defaults to the resolved cascade for cwd.", false),
            ("project", "Project path to resolve the cascade for. Defaults to cwd.", false),
        ],
    ),
    (
        "cascade-search",
        "Search the Cascade corpus and inject the top-ranked results as context. \
         Uses hybrid RRF search (BGE-M3 + FTS5) over the watched instruction files.",
        &[
            ("query", "What to search for in the Cascade corpus.", true),
            ("limit", "Maximum number of results to include (default: 5).", false),
        ],
    ),
    (
        "cascade-commit-review",
        "Pre-fill a commit review request with the relevant Cascade rules and the \
         provided diff. The AI will review the diff against the cascade commit \
         standards and provide structured feedback.",
        &[
            ("diff", "The git diff to review (output of `git diff HEAD` or similar).", true),
        ],
    ),
];

// ── PromptsHandler ────────────────────────────────────────────────────────────

/// Handles `prompts/list` and `prompts/get` for the MCP server.
///
/// Prompts are stateless templates defined at compile time. Live cascade-core
/// and cascade-rag integration is wired in the daemon layer (P4 Wave 3+).
pub struct PromptsHandler;

impl PromptsHandler {
    /// Create a new `PromptsHandler`.
    pub fn new() -> Self {
        Self
    }

    /// Handle `prompts/list`.
    ///
    /// Returns `{ "prompts": [PromptInfo, ...] }` — exactly 3 entries.
    pub fn list(&self) -> Result<Value, JsonRpcError> {
        let infos: Vec<PromptInfo> = PROMPTS
            .iter()
            .map(|(name, description, args)| PromptInfo {
                name: name.to_string(),
                description: description.to_string(),
                arguments: args
                    .iter()
                    .map(|(arg_name, arg_desc, required)| PromptArgument {
                        name: arg_name.to_string(),
                        description: arg_desc.to_string(),
                        required: *required,
                    })
                    .collect(),
            })
            .collect();

        Ok(serde_json::json!({ "prompts": infos }))
    }

    /// Handle `prompts/get`.
    ///
    /// Params: `{ "name": "cascade-context", "arguments": { "tier": "gci" } }`
    /// Returns: serialised [`GetPromptResult`].
    pub fn get(&self, params: &Value) -> Result<Value, JsonRpcError> {
        let name = params
            .get("name")
            .and_then(|v| v.as_str())
            .ok_or_else(|| JsonRpcError::invalid_params("missing 'name' in prompts/get"))?;

        debug!(prompt = name, "prompts/get");

        let args = params.get("arguments").cloned().unwrap_or(Value::Object(Default::default()));

        let result = match name {
            "cascade-context" => self.get_cascade_context(&args),
            "cascade-search" => self.get_cascade_search(&args),
            "cascade-commit-review" => self.get_cascade_commit_review(&args),
            _ => Err(JsonRpcError::method_not_found(format!("Prompt not found: {name}"))),
        }?;

        serde_json::to_value(result).map_err(|e| JsonRpcError::internal(e.to_string()))
    }

    // ── Template fillers ──────────────────────────────────────────────────────

    fn get_cascade_context(&self, args: &Value) -> Result<GetPromptResult, JsonRpcError> {
        let tier = args
            .get("tier")
            .and_then(|v| v.as_str())
            .unwrap_or("resolved");
        let project = args
            .get("project")
            .and_then(|v| v.as_str())
            .unwrap_or("cwd");

        let description = format!("Load {tier} cascade tier instructions for project '{project}'");

        let text = format!(
            "Please load and inject the Cascade tier instructions for tier '{}' \
             (project: '{}') into this conversation. \
             Use the cascade.read tool to fetch the instruction file, then \
             summarise the key rules that apply to the current task.",
            tier, project
        );

        Ok(GetPromptResult {
            description,
            messages: vec![user_message(text)],
        })
    }

    fn get_cascade_search(&self, args: &Value) -> Result<GetPromptResult, JsonRpcError> {
        let query = args
            .get("query")
            .and_then(|v| v.as_str())
            .ok_or_else(|| {
                JsonRpcError::invalid_params("missing required argument 'query' for cascade-search")
            })?;
        let limit = args
            .get("limit")
            .and_then(|v| v.as_u64())
            .unwrap_or(5);

        let description = format!("Search Cascade corpus for '{query}' (limit: {limit})");

        let text = format!(
            "Please search the Cascade corpus for '{}' using cascade.search, \
             returning at most {} results. \
             Format the results as a numbered list with the source path and a \
             brief excerpt for each hit, then summarise the most relevant context.",
            query, limit
        );

        Ok(GetPromptResult {
            description,
            messages: vec![user_message(text)],
        })
    }

    fn get_cascade_commit_review(&self, args: &Value) -> Result<GetPromptResult, JsonRpcError> {
        let diff = args
            .get("diff")
            .and_then(|v| v.as_str())
            .ok_or_else(|| {
                JsonRpcError::invalid_params(
                    "missing required argument 'diff' for cascade-commit-review",
                )
            })?;

        let description = "Commit review against Cascade rules".to_string();

        let text = format!(
            "Review the following git diff against the Cascade commit standards \
             (see GCI § Git Conventions and the project's CLAUDE.md). \
             Check for: (1) correct commit message format, (2) no accidental secret \
             or credential exposure, (3) no AI-attribution footers, (4) scope matches \
             changed files, (5) test coverage for changed code.\n\n\
             Diff:\n```diff\n{diff}\n```\n\n\
             Provide structured feedback with a pass/fail verdict for each check."
        );

        Ok(GetPromptResult {
            description,
            messages: vec![user_message(text)],
        })
    }
}

impl Default for PromptsHandler {
    fn default() -> Self {
        Self::new()
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn user_message(text: String) -> PromptMessage {
    PromptMessage {
        role: "user".to_string(),
        content: PromptTextContent {
            kind: "text".to_string(),
            text,
        },
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn handler() -> PromptsHandler {
        PromptsHandler::new()
    }

    // ── prompts/list ──────────────────────────────────────────────────────────

    #[test]
    fn list_returns_exactly_3_prompts() {
        let h = handler();
        let result = h.list().unwrap();
        let prompts = result["prompts"].as_array().expect("prompts must be an array");
        assert_eq!(prompts.len(), 3, "expected exactly 3 prompts, got {}", prompts.len());
    }

    #[test]
    fn list_contains_expected_names() {
        let h = handler();
        let result = h.list().unwrap();
        let prompts = result["prompts"].as_array().unwrap();
        let names: Vec<&str> = prompts
            .iter()
            .map(|p| p["name"].as_str().unwrap())
            .collect();
        assert!(names.contains(&"cascade-context"), "missing cascade-context");
        assert!(names.contains(&"cascade-search"), "missing cascade-search");
        assert!(names.contains(&"cascade-commit-review"), "missing cascade-commit-review");
    }

    #[test]
    fn list_prompts_have_arguments_field() {
        let h = handler();
        let result = h.list().unwrap();
        let prompts = result["prompts"].as_array().unwrap();
        for p in prompts {
            assert!(p["arguments"].is_array(), "prompt {} missing 'arguments'", p["name"]);
        }
    }

    // ── prompts/get — cascade-context ─────────────────────────────────────────

    #[test]
    fn get_cascade_context_with_tier_arg() {
        let h = handler();
        let params = serde_json::json!({
            "name": "cascade-context",
            "arguments": { "tier": "gci" }
        });
        let result = h.get(&params).unwrap();
        let messages = result["messages"].as_array().expect("messages must be array");
        assert!(!messages.is_empty(), "messages must be non-empty");
        assert_eq!(messages[0]["role"].as_str().unwrap(), "user");
        let text = messages[0]["content"]["text"].as_str().unwrap();
        assert!(!text.is_empty(), "message text must be non-empty");
        assert!(text.contains("gci"), "text should mention the requested tier");
    }

    #[test]
    fn get_cascade_context_without_args_uses_defaults() {
        let h = handler();
        let params = serde_json::json!({ "name": "cascade-context" });
        let result = h.get(&params).unwrap();
        let messages = result["messages"].as_array().unwrap();
        assert!(!messages.is_empty());
    }

    // ── prompts/get — cascade-search ─────────────────────────────────────────

    #[test]
    fn get_cascade_search_with_query() {
        let h = handler();
        let params = serde_json::json!({
            "name": "cascade-search",
            "arguments": { "query": "commit message format" }
        });
        let result = h.get(&params).unwrap();
        let messages = result["messages"].as_array().unwrap();
        assert!(!messages.is_empty());
        let text = messages[0]["content"]["text"].as_str().unwrap();
        assert!(text.contains("commit message format"));
    }

    #[test]
    fn get_cascade_search_missing_required_query_returns_invalid_params() {
        let h = handler();
        let params = serde_json::json!({
            "name": "cascade-search",
            "arguments": {}
        });
        let err = h.get(&params).unwrap_err();
        assert_eq!(err.code, crate::server::ERR_INVALID_PARAMS, "expected -32602 invalid_params");
        assert!(err.message.contains("query"), "error should mention missing field");
    }

    #[test]
    fn get_cascade_search_with_custom_limit() {
        let h = handler();
        let params = serde_json::json!({
            "name": "cascade-search",
            "arguments": { "query": "test", "limit": 10 }
        });
        let result = h.get(&params).unwrap();
        let text = result["messages"][0]["content"]["text"].as_str().unwrap();
        assert!(text.contains("10"), "text should mention the limit");
    }

    // ── prompts/get — cascade-commit-review ──────────────────────────────────

    #[test]
    fn get_cascade_commit_review_with_diff() {
        let h = handler();
        let params = serde_json::json!({
            "name": "cascade-commit-review",
            "arguments": { "diff": "+fn hello() {}" }
        });
        let result = h.get(&params).unwrap();
        let messages = result["messages"].as_array().unwrap();
        assert!(!messages.is_empty());
        let text = messages[0]["content"]["text"].as_str().unwrap();
        assert!(text.contains("+fn hello()"), "diff should be embedded in message");
    }

    #[test]
    fn get_cascade_commit_review_missing_diff_returns_invalid_params() {
        let h = handler();
        let params = serde_json::json!({
            "name": "cascade-commit-review",
            "arguments": {}
        });
        let err = h.get(&params).unwrap_err();
        assert_eq!(err.code, crate::server::ERR_INVALID_PARAMS);
        assert!(err.message.contains("diff"));
    }

    // ── prompts/get — unknown prompt ─────────────────────────────────────────

    #[test]
    fn get_unknown_prompt_returns_method_not_found() {
        let h = handler();
        let params = serde_json::json!({ "name": "no-such-prompt" });
        let err = h.get(&params).unwrap_err();
        assert_eq!(
            err.code,
            crate::server::ERR_METHOD_NOT_FOUND,
            "expected -32601 method_not_found"
        );
        assert!(err.message.contains("no-such-prompt"));
    }

    #[test]
    fn get_missing_name_returns_invalid_params() {
        let h = handler();
        let params = serde_json::json!({ "arguments": {} });
        let err = h.get(&params).unwrap_err();
        assert_eq!(err.code, crate::server::ERR_INVALID_PARAMS);
    }
}
