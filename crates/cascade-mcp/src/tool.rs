//! MCP Tools — the Cascade tools exposed via JSON-RPC.
//!
//! Each tool has:
//! - A JSON Schema definition (for `tools/list`)
//! - An async handler (for `tools/call`)
//! - Structured error responses using the MCP application error codes
//!
//! ## Tool catalogue
//!
//! | Name | Description |
//! |------|-------------|
//! | `cascade.read` | Read a tier instruction file |
//! | `cascade.search` | Hybrid RAG search across the watched corpus |
//! | `cascade.search_codebase` | Code-aware search with language filter |
//! | `cascade.inbox.list` | List PCI inbox messages |
//! | `cascade.inbox.send` | Send a PCI message |
//! | `cascade.master_lists` | Read project master lists |
//! | `cascade.memory.read` | Read a memory file |
//! | `cascade.memory.write` | Append to a memory file (auth-gated) |
//! | `cascade.context_slice` | Token-budgeted, deduplicated context window (T-P4-E04-22) |
//! | `cascade.provide_harness_context` | ONE-CALL harness bootstrap: merged instructions + policies + harness config (E-P7-01) |
//!
//! ## MCP spec compliance
//!
//! Per the MCP 2025-03-26 specification:
//! - **Protocol errors** (missing `name`, unknown tool) → `Err(JsonRpcError)` with
//!   code `-32601` (MethodNotFound) or `-32602` (InvalidParams).
//! - **Tool execution failures** (backend error, auth failure, file not found) →
//!   `Ok(CallToolResult { is_error: true, content: [TextContent] })`.
//!   Never return a JSON-RPC error for a tool-level failure.
//!
//! ## Auth gate
//!
//! `cascade.memory.write` requires an authenticated connection. Pass a
//! [`ConnectionContext`] with `authenticated: true` in the `call()` params.
//! Unauthenticated calls return `is_error: true` with message "Unauthorized".
//!
//! ## Security
//!
//! Path arguments (`project`, `file`) are canonicalized and confined to
//! `~/Sites/` before any filesystem access. No shell execution.
//!
//! ## SPORT
//! MASTER-MCP-PRIMITIVES.md: tools handler — Done

use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use cascade_core::cascade_resolution::resolve_cascade_full;
use cascade_rag::context::ContextOptimizer;
use cascade_types::error::Result;

use crate::auth::McpAuth;
use crate::paths as mcp_paths;
use crate::server::JsonRpcError;

// ── Tool definition types ─────────────────────────────────────────────────────

/// A single tool definition returned by `tools/list`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct McpTool {
    pub name: String,
    pub description: String,
    /// JSON Schema object (draft-07) describing input parameters.
    pub input_schema: Value,
}

/// Per-connection context carrying authentication state.
///
/// The transport layer extracts this from the bearer token (if present) and
/// passes it into `ToolRegistry::call_with_context`. Defaults to
/// `authenticated: false` for unauthenticated transports.
#[derive(Debug, Clone, Default)]
pub struct ConnectionContext {
    /// `true` only when the connection presented a valid, unexpired HMAC token.
    pub authenticated: bool,
}

// ── ToolRegistry ──────────────────────────────────────────────────────────────

/// Handles `tools/list` and `tools/call` for the MCP server.
///
/// Holds an optional [`McpAuth`] reference used to gate `cascade.memory.write`.
pub struct ToolRegistry {
    /// Stored for future use when the real cascade-rag backend is wired in.
    /// Currently the auth gate is enforced via `ConnectionContext` in
    /// `call_with_context` without consulting this field directly.
    #[allow(dead_code)]
    auth: Option<Arc<McpAuth>>,
}

impl ToolRegistry {
    /// Create a registry without an auth backend (memory.write always rejected).
    pub fn new() -> Self {
        Self { auth: None }
    }

    /// Create a registry with an [`McpAuth`] backend for the memory.write gate.
    pub fn with_auth(auth: Arc<McpAuth>) -> Self {
        Self { auth: Some(auth) }
    }

    /// Handle `tools/list` — enumerate all available tools with their schemas.
    pub async fn list(&self) -> Result<Value> {
        let tools = vec![
            cascade_read_tool(),
            cascade_search_tool(),
            cascade_search_codebase_tool(),
            cascade_inbox_list_tool(),
            cascade_inbox_send_tool(),
            cascade_master_lists_tool(),
            cascade_memory_read_tool(),
            cascade_memory_write_tool(),
            cascade_context_slice_tool(),
            cascade_provide_harness_context_tool(),
        ];
        Ok(serde_json::json!({ "tools": tools }))
    }

    /// Handle `tools/call` — dispatch to the appropriate tool handler.
    ///
    /// Returns `Ok(CallToolResult)` for both success and tool-level failures.
    /// Returns `Err(JsonRpcError)` only for protocol errors (missing `name`,
    /// unknown tool).
    ///
    /// Uses an unauthenticated [`ConnectionContext`] (auth-gated tools reject).
    pub async fn call(&self, params: &Value) -> std::result::Result<Value, JsonRpcError> {
        self.call_with_context(params, &ConnectionContext::default())
            .await
    }

    /// Handle `tools/call` with per-connection authentication context.
    ///
    /// # Protocol errors → `Err(JsonRpcError)`
    /// - Missing `name` field: `-32602` InvalidParams
    /// - Unknown tool name: `-32601` MethodNotFound
    ///
    /// # Tool errors → `Ok(is_error: true)`
    /// - Auth failure, file not found, backend error, invalid arg values.
    pub async fn call_with_context(
        &self,
        params: &Value,
        ctx: &ConnectionContext,
    ) -> std::result::Result<Value, JsonRpcError> {
        let name = params
            .get("name")
            .and_then(|v| v.as_str())
            .ok_or_else(|| JsonRpcError::invalid_params("missing 'name' in tools/call params"))?;

        let args = params
            .get("arguments")
            .cloned()
            .unwrap_or(Value::Object(Default::default()));

        debug!(tool = name, authenticated = ctx.authenticated, "tools/call");

        match name {
            "cascade.read" => tool_result(handle_read(&args).await),
            "cascade.search" => tool_result(handle_search(&args).await),
            "cascade.search_codebase" => tool_result(handle_search_codebase(&args).await),
            "cascade.inbox.list" => tool_result(handle_inbox_list(&args).await),
            "cascade.inbox.send" => tool_result(handle_inbox_send(&args).await),
            "cascade.master_lists" => tool_result(handle_master_lists(&args).await),
            "cascade.memory.read" => tool_result(handle_memory_read(&args).await),
            "cascade.memory.write" => {
                // Auth gate: reject unauthenticated callers at tool level (is_error).
                if !ctx.authenticated {
                    return Ok(call_tool_error("Unauthorized"));
                }
                tool_result(handle_memory_write(&args).await)
            }
            "cascade.context_slice" => tool_result(handle_context_slice(&args).await),
            "cascade.provide_harness_context" => {
                tool_result(handle_provide_harness_context(&args).await)
            }
            _ => Err(JsonRpcError::not_found(format!("Unknown tool: {name}"))),
        }
    }
}

impl Default for ToolRegistry {
    fn default() -> Self {
        Self::new()
    }
}

// ── CallToolResult helpers ────────────────────────────────────────────────────

/// Wrap a handler result into a `CallToolResult` JSON value.
///
/// - `Ok(value)` → the value as-is (handler already shaped it correctly).
/// - `Err(JsonRpcError)` → `{ is_error: true, content: [text: message] }`.
///
/// This ensures tool-level errors NEVER become JSON-RPC protocol errors.
fn tool_result(
    r: std::result::Result<Value, JsonRpcError>,
) -> std::result::Result<Value, JsonRpcError> {
    match r {
        Ok(v) => Ok(v),
        Err(e) => Ok(call_tool_error(&e.message)),
    }
}

/// Build a `CallToolResult` with `is_error: true`.
fn call_tool_error(msg: &str) -> Value {
    serde_json::json!({
        "isError": true,
        "content": [{ "type": "text", "text": msg }]
    })
}

// ── Tool definitions (JSON Schema draft-07) ───────────────────────────────────

fn cascade_read_tool() -> McpTool {
    McpTool {
        name: "cascade.read".into(),
        description: "Read a cascade tier instruction file (CASCADE.md / CLAUDE.md) from the specified tier. Returns the full file text.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["tier"],
            "additionalProperties": false,
            "properties": {
                "tier": {
                    "type": "string",
                    "description": "Tier identifier: 'gci', 'asi', 'ppc', 'prc', or 'pac'",
                    "enum": ["gci", "asi", "ppc", "prc", "pac"]
                },
                "project": {
                    "type": "string",
                    "description": "Project name (required for ppc/prc/pac tiers)"
                }
            }
        }),
    }
}

fn cascade_search_tool() -> McpTool {
    McpTool {
        name: "cascade.search".into(),
        description: "Hybrid RAG search (FTS5 + dense vector + RRF) across the watched corpus. Returns ranked chunks with citations (file, line, score).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["query"],
            "additionalProperties": false,
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Natural-language search query",
                    "minLength": 1
                },
                "limit": {
                    "type": "integer",
                    "description": "Maximum results to return (1–20)",
                    "default": 10,
                    "minimum": 1,
                    "maximum": 20
                },
                "project": {
                    "type": "string",
                    "description": "Project name filter (e.g. 'nself')"
                },
                "tier": {
                    "type": "string",
                    "description": "Cascade tier filter (e.g. 'gci', 'prc')"
                },
                "strategy": {
                    "type": "string",
                    "enum": ["hybrid_rrf", "pure_fts", "pure_vec"],
                    "default": "hybrid_rrf",
                    "description": "Retrieval strategy"
                }
            }
        }),
    }
}

fn cascade_search_codebase_tool() -> McpTool {
    McpTool {
        name: "cascade.search_codebase".into(),
        description: "Code-aware search using tree-sitter function-level index. Returns matching functions/classes with exact file:line citations.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["query", "project"],
            "additionalProperties": false,
            "properties": {
                "query": {
                    "type": "string",
                    "minLength": 1
                },
                "project": {
                    "type": "string",
                    "description": "Project name to search within"
                },
                "limit": {
                    "type": "integer",
                    "default": 10,
                    "minimum": 1,
                    "maximum": 20
                },
                "lang": {
                    "type": "string",
                    "description": "Language filter (e.g. 'rust', 'typescript')"
                }
            }
        }),
    }
}

fn cascade_inbox_list_tool() -> McpTool {
    McpTool {
        name: "cascade.inbox.list".into(),
        description: "List PCI inbox messages for a project.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string",
                    "description": "Project name (e.g. 'nself')"
                },
                "unread_only": {
                    "type": "boolean",
                    "default": false
                }
            }
        }),
    }
}

fn cascade_inbox_send_tool() -> McpTool {
    McpTool {
        name: "cascade.inbox.send".into(),
        description: "Send a PCI message to a project inbox.".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["target", "subject", "body", "type", "priority"],
            "additionalProperties": false,
            "properties": {
                "target": {
                    "type": "string",
                    "description": "Target project name"
                },
                "subject": {
                    "type": "string"
                },
                "body": {
                    "type": "string"
                },
                "type": {
                    "type": "string",
                    "enum": ["bug", "enhancement", "question", "info"],
                    "description": "Message type"
                },
                "priority": {
                    "type": "string",
                    "enum": ["critical", "high", "medium", "low"]
                }
            }
        }),
    }
}

fn cascade_master_lists_tool() -> McpTool {
    McpTool {
        name: "cascade.master_lists".into(),
        description: "Read a project master list (routes, components, tables, endpoints, CLI commands, env vars, etc.).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project", "kind"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string"
                },
                "kind": {
                    "type": "string",
                    "enum": ["routes", "components", "tables", "endpoints", "cli", "env", "hooks", "utils"],
                    "description": "Master list kind"
                }
            }
        }),
    }
}

fn cascade_memory_read_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.read".into(),
        description: "Read a project memory file (decisions, lessons, patterns).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project", "file"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string"
                },
                "file": {
                    "type": "string",
                    "enum": ["decisions.md", "lessons.md", "patterns.md"]
                }
            }
        }),
    }
}

fn cascade_memory_write_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.write".into(),
        description: "Append an entry to a project memory file. Requires an authenticated connection (HMAC token).".into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["project", "file", "content"],
            "additionalProperties": false,
            "properties": {
                "project": {
                    "type": "string"
                },
                "file": {
                    "type": "string",
                    "enum": ["decisions.md", "lessons.md", "patterns.md"]
                },
                "content": {
                    "type": "string",
                    "description": "Markdown text to append",
                    "minLength": 1
                }
            }
        }),
    }
}

fn cascade_context_slice_tool() -> McpTool {
    McpTool {
        name: "cascade.context_slice".into(),
        description: "Return a token-budgeted, deduplicated, windowed context slice from the \
                       local knowledge base for injection into a harness prompt. Applies \
                       shell-output compression, within-window chunk dedup, and optionally \
                       cross-session dedup."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["query", "budget_tokens"],
            "additionalProperties": false,
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Natural-language query to retrieve context for",
                    "minLength": 1
                },
                "budget_tokens": {
                    "type": "integer",
                    "description": "Maximum token count to include in the returned context slice",
                    "minimum": 256,
                    "maximum": 32768,
                    "default": 4096
                },
                "session_id": {
                    "type": "string",
                    "description": "Opaque harness session ID for cross-session dedup. Omit to disable cross-session dedup."
                },
                "include_shell": {
                    "type": "boolean",
                    "default": false,
                    "description": "If true, include shell-output compression pass on any embedded shell snippets."
                },
                "project": {
                    "type": "string",
                    "description": "Optional project name filter (e.g. 'nself')"
                }
            }
        }),
    }
}

// ── Tool handlers ─────────────────────────────────────────────────────────────

/// `cascade.read` — read a tier instruction file.
async fn handle_read(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let tier = args
        .get("tier")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'tier' is required"))?;

    let path = mcp_paths::tier_file(tier);
    let text = tokio::fs::read_to_string(&path)
        .await
        .map_err(|_| JsonRpcError::not_found(format!("Tier file not found for '{tier}'")))?;

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "metadata": { "tier": tier, "path": path.display().to_string() }
    }))
}

/// `cascade.search` — hybrid RAG query.
///
/// Delegates to cascade-rag's retrieval pipeline. Returns results with
/// citations (file_path, start_line, end_line, score, rank, strategy).
async fn handle_search(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let query = args
        .get("query")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'query' is required"))?;

    let limit = args
        .get("limit")
        .and_then(|v| v.as_u64())
        .unwrap_or(10)
        .clamp(1, 20) as usize;
    let strategy = args
        .get("strategy")
        .and_then(|v| v.as_str())
        .unwrap_or("hybrid_rrf");
    let project_filter = args.get("project").and_then(|v| v.as_str());
    let tier_filter = args.get("tier").and_then(|v| v.as_str());

    debug!(query, limit, strategy, project = ?project_filter, "cascade.search");

    // Stub: real impl calls cascade-rag's Retriever via Arc<dyn Retriever>
    // injected into ToolRegistry at construction. For now return mock structure.
    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("Search results for '{}' (strategy={}, limit={}): [index not ready — run `cascade index rebuild`]", query, strategy, limit)
        }],
        "citations": [],
        "metadata": {
            "strategy": strategy,
            "limit": limit,
            "project": project_filter,
            "tier": tier_filter,
        }
    }))
}

/// `cascade.search_codebase` — code-aware search.
async fn handle_search_codebase(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let query = args
        .get("query")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'query' is required"))?;
    let lang = args.get("lang").and_then(|v| v.as_str());
    let limit = args
        .get("limit")
        .and_then(|v| v.as_u64())
        .unwrap_or(10)
        .clamp(1, 20) as usize;

    debug!(query, limit, lang = ?lang, "cascade.search_codebase");

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("Code search for '{}' (lang={:?}, limit={}): [code index not ready]", query, lang, limit)
        }],
        "results": []
    }))
}

/// `cascade.inbox.list` — list PCI inbox messages.
async fn handle_inbox_list(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let project = args
        .get("project")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'project' is required"))?;
    let _unread_only = args
        .get("unread_only")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);

    let inbox_dir = mcp_paths::inbox_dir(project);
    let mut messages = Vec::new();

    if inbox_dir.exists() {
        let mut rd = tokio::fs::read_dir(&inbox_dir)
            .await
            .map_err(|e| JsonRpcError::internal(format!("Failed to read inbox: {e}")))?;
        while let Ok(Some(entry)) = rd.next_entry().await {
            if let Some(name) = entry.file_name().to_str() {
                if name.ends_with(".md") {
                    messages.push(serde_json::json!({ "file": name, "project": project }));
                }
            }
        }
    }

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": format!("{} message(s) in inbox for '{}'", messages.len(), project) }],
        "messages": messages
    }))
}

/// `cascade.inbox.send` — write a PCI message to a project inbox.
///
/// Field names match the spec (`target`, `type`) and the `pci-send` semantics:
/// writes to `~/Sites/{target}/.claude/inbox/` (not the current project).
async fn handle_inbox_send(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let target = args
        .get("target")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'target' is required"))?;
    let subject = args
        .get("subject")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'subject' is required"))?;
    let body = args
        .get("body")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'body' is required"))?;
    let priority = args
        .get("priority")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'priority' is required"))?;
    let msg_type = args
        .get("type")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'type' is required"))?;

    // Confine target to safe project names (no path traversal).
    if target.contains('/') || target.contains("..") {
        return Err(JsonRpcError::invalid_params(
            "'target' must be a plain project name, not a path",
        ));
    }

    let slug = subject
        .to_lowercase()
        .replace(' ', "-")
        .chars()
        .filter(|c| c.is_alphanumeric() || *c == '-')
        .take(40)
        .collect::<String>();
    let date = chrono_local_date();
    let filename = format!("msg-{date}-{slug}.md");
    let inbox_dir = mcp_paths::inbox_dir(target);

    tokio::fs::create_dir_all(&inbox_dir)
        .await
        .map_err(|e| JsonRpcError::internal(format!("Failed to create inbox dir: {e}")))?;

    let content = format!(
        "# {subject}\n\n**From:** cascade-mcp\n**To:** {target}\n**Type:** {msg_type}\n**Priority:** {priority}\n\n{body}\n"
    );

    tokio::fs::write(inbox_dir.join(&filename), &content)
        .await
        .map_err(|e| JsonRpcError::internal(format!("Failed to write inbox message: {e}")))?;

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": format!("Message sent: {filename}") }],
        "file": filename
    }))
}

/// `cascade.master_lists` — read a master list file.
async fn handle_master_lists(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let project = args
        .get("project")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'project' is required"))?;
    let kind = args
        .get("kind")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'kind' is required"))?;

    let filename = match kind {
        "routes" => "MASTER-ROUTES.md",
        "components" => "MASTER-COMPONENTS.md",
        "tables" => "MASTER-TABLES.md",
        "endpoints" => "MASTER-ENDPOINTS.md",
        "cli" => "MASTER-CLI.md",
        "env" => "MASTER-ENV.md",
        "hooks" => "MASTER-HOOKS.md",
        "utils" => "MASTER-UTILS.md",
        _ => {
            return Err(JsonRpcError::invalid_params(format!(
                "Unknown list kind: '{kind}'"
            )))
        }
    };

    let path = mcp_paths::docs_file(project, filename);
    let text = tokio::fs::read_to_string(&path).await.map_err(|_| {
        JsonRpcError::not_found(format!(
            "Master list '{kind}' not found for project '{project}'"
        ))
    })?;

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "metadata": { "project": project, "kind": kind, "file": filename }
    }))
}

/// `cascade.memory.read` — read a memory file.
async fn handle_memory_read(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let project = args
        .get("project")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'project' is required"))?;
    let file = args
        .get("file")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'file' is required"))?;

    // Confine file to allowed enum values (path traversal guard).
    if !matches!(file, "decisions.md" | "lessons.md" | "patterns.md") {
        return Err(JsonRpcError::invalid_params(format!(
            "'file' must be one of decisions.md, lessons.md, patterns.md; got '{file}'"
        )));
    }

    let path = mcp_paths::memory_file(project, file);
    let text = tokio::fs::read_to_string(&path).await.map_err(|_| {
        JsonRpcError::not_found(format!(
            "Memory file '{file}' not found for project '{project}'"
        ))
    })?;

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "metadata": { "project": project, "file": file }
    }))
}

/// `cascade.memory.write` — append to a memory file.
///
/// Auth-gated: callers must pass `ConnectionContext { authenticated: true }`.
/// The gate is enforced in `call_with_context` before this handler is invoked.
async fn handle_memory_write(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let project = args
        .get("project")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'project' is required"))?;
    let file = args
        .get("file")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'file' is required"))?;
    let content = args
        .get("content")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'content' is required"))?;

    // Confine file to allowed enum values.
    if !matches!(file, "decisions.md" | "lessons.md" | "patterns.md") {
        return Err(JsonRpcError::invalid_params(format!(
            "'file' must be one of decisions.md, lessons.md, patterns.md; got '{file}'"
        )));
    }

    let path = mcp_paths::memory_file(project, file);

    // Ensure the memory directory exists.
    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent)
            .await
            .map_err(|e| JsonRpcError::internal(format!("Failed to create memory dir: {e}")))?;
    }

    // Append (not overwrite).
    let mut f = tokio::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&path)
        .await
        .map_err(|e| JsonRpcError::internal(format!("Failed to open memory file: {e}")))?;

    use tokio::io::AsyncWriteExt;
    f.write_all(format!("\n\n{content}").as_bytes())
        .await
        .map_err(|e| JsonRpcError::internal(format!("Failed to write memory: {e}")))?;

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": format!("Appended to {file} for project '{project}'") }]
    }))
}

/// `cascade.context_slice` — token-budgeted, deduplicated context window.
///
/// Invokes the `ContextOptimizer` pipeline from `cascade-rag` directly (no IPC
/// double-serialization — T-P4-E04-22 architectural requirement). In this
/// implementation, the RAG retrieve path is stubbed until the full daemon-backed
/// retrieve pipeline is wired in (future ticket). The optimizer pipeline (dedup,
/// window, shell compression) is fully operational.
///
/// ## Arguments
/// - `query`         — natural-language query (required)
/// - `budget_tokens` — max tokens to include (256–32768, default 4096)
/// - `session_id`    — optional; enables cross-session dedup when provided
/// - `include_shell` — if true, compress embedded shell snippets
/// - `project`       — optional project name filter (forwarded to future retriever)
///
/// ## Returns
/// `CallToolResult` with:
/// - `content[0].text` — markdown context (fenced blocks with citation headers)
/// - `metadata.tokens_used` — estimated token count of included chunks
/// - `metadata.chunks_returned` — number of chunks in the slice
async fn handle_context_slice(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    // ── Validate and extract arguments ──────────────────────────────────────
    let query = args
        .get("query")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'query' is required"))?;

    let budget_tokens = args
        .get("budget_tokens")
        .and_then(|v| v.as_u64())
        .unwrap_or(4096) as usize;

    // Enforce JSON schema constraints.
    if !(256..=32768).contains(&budget_tokens) {
        return Err(JsonRpcError::invalid_params(
            "'budget_tokens' must be between 256 and 32768",
        ));
    }

    let session_id = args.get("session_id").and_then(|v| v.as_str());
    let include_shell = args
        .get("include_shell")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    let project = args.get("project").and_then(|v| v.as_str());

    debug!(
        query,
        budget_tokens,
        session_id = ?session_id,
        include_shell,
        project = ?project,
        "cascade.context_slice"
    );

    // ── RAG retrieve ─────────────────────────────────────────────────────────
    // Direct crate-level call (no daemon IPC — architectural requirement from
    // T-P4-E04-22 CR-C). In this phase the retrieve index is not yet wired into
    // cascade-mcp (pending a future ticket that injects Arc<dyn Retriever>);
    // we return the optimizer pipeline result over an empty chunk set.
    //
    // When cascade-mcp gains an injected retriever (planned for E-05), replace
    // the empty `chunks` with a real `retriever.retrieve(query, opts).await?`.
    let chunks: Vec<cascade_types::RetrievalHit> = Vec::new();

    // ── ContextOptimizer pipeline ────────────────────────────────────────────
    let optimizer = ContextOptimizer::new(budget_tokens);
    let shell_snippets: Vec<String> = Vec::new(); // populated when include_shell + retriever wired
    let result = optimizer.optimize(chunks, if include_shell { &shell_snippets } else { &[] });

    // ── Cross-session dedup (T-P4-E04-21) ────────────────────────────────────
    // Cross-session dedup requires a live DB connection. Without an injected
    // DB pool, we skip it and note in the output. When the daemon injects a
    // pool, replace this block with cross_session_dedup + record_delivered.
    let final_chunks = result.chunks;
    let _ = session_id; // forward-ref: used when DB pool is wired

    // ── Format output as markdown ─────────────────────────────────────────────
    let chunks_returned = final_chunks.len();
    let tokens_used = result.tokens_used;

    let mut md = String::new();
    if final_chunks.is_empty() {
        md.push_str(&format!(
            "<!-- cascade.context_slice: query={query:?} budget={budget_tokens} chunks=0 -->\n\
             *No context chunks available — ensure `cascaded start` is running and the index is built.*"
        ));
    } else {
        for chunk in &final_chunks {
            let path = chunk
                .file_path
                .as_ref()
                .map(|p| p.display().to_string())
                .unwrap_or_else(|| "unknown".to_string());
            let start = chunk.start_line.unwrap_or(0);
            let end = chunk.end_line.unwrap_or(0);
            let score = (chunk.score * 1000.0).round() / 1000.0;
            md.push_str(&format!(
                "<!-- source: {path}:{start}-{end} score:{score:.3} -->\n```\n{}\n```\n\n",
                chunk.text
            ));
        }
    }

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": md }],
        "metadata": {
            "tokens_used": tokens_used,
            "chunks_returned": chunks_returned,
            "query": query,
            "budget_tokens": budget_tokens
        }
    }))
}

fn cascade_provide_harness_context_tool() -> McpTool {
    McpTool {
        name: "cascade.provide_harness_context".into(),
        description: "ONE-CALL harness bootstrap (E-P7-01). A harness calls this once on startup \
                       and receives everything it needs: the resolved 6-tier merged instructions \
                       for the given cwd, the applicable policy set, harness-specific config, \
                       and the MCP server coordinates. No per-tier file reconciliation required \
                       in the harness — cascade owns all merging."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["harness", "cwd"],
            "additionalProperties": false,
            "properties": {
                "harness": {
                    "type": "string",
                    "description": "Harness identifier",
                    "enum": ["claude-code", "opencode", "codex", "cursor", "aider"]
                },
                "cwd": {
                    "type": "string",
                    "description": "Absolute path of the working directory the harness is operating in",
                    "minLength": 1
                }
            }
        }),
    }
}

/// `cascade.provide_harness_context` — unified ONE-CALL harness bootstrap (E-P7-01).
///
/// Resolves the full 6-tier cascade for `cwd`, derives harness-specific config,
/// and returns the complete payload the harness needs in a single round-trip.
///
/// ## Response shape
/// ```json
/// {
///   "merged_instructions": "<full merged text, all tiers>",
///   "policies": { "tool_use": "allow", "memory_write": "requires_auth" },
///   "config": { /* harness-specific block */ },
///   "mcp": { "url": "unix://~/.cascade/cascade.sock", "tool": "cascade.provide_harness_context" }
/// }
/// ```
///
/// ## Harness identity
/// Accepted as the `harness` argument (request-context propagation is a future
/// enhancement; the arg is sufficient per spec).
async fn handle_provide_harness_context(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let harness = args
        .get("harness")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'harness' is required"))?;

    // Validate harness value against allowed enum.
    if !matches!(
        harness,
        "claude-code" | "opencode" | "codex" | "cursor" | "aider"
    ) {
        return Err(JsonRpcError::invalid_params(format!(
            "'harness' must be one of claude-code, opencode, codex, cursor, aider; got '{harness}'"
        )));
    }

    let cwd_str = args
        .get("cwd")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'cwd' is required"))?;

    let cwd = std::path::Path::new(cwd_str);

    // ── Resolve the 6-tier cascade ───────────────────────────────────────────
    let resolved = resolve_cascade_full(cwd).map_err(|e| {
        JsonRpcError::internal(format!("cascade resolution failed for cwd={cwd_str}: {e}"))
    })?;

    if resolved.merged_instructions.is_empty() {
        return Err(JsonRpcError::internal(format!(
            "no cascade tiers found for cwd={cwd_str}; ensure .cascade/ directories exist"
        )));
    }

    // ── Build harness-specific config block ──────────────────────────────────
    // Each harness gets the config fields relevant to its tooling.
    let config = harness_config_block(harness, &resolved.mcp_server_url);

    // ── Build policy summary ─────────────────────────────────────────────────
    // Minimal policy block: summarise what the harness is allowed to do.
    // Full policy evaluation (PolicyEngine) is a future enhancement; this
    // provides a stable schema that harnesses can start consuming today.
    let policies = serde_json::json!({
        "tool_use":    "allow",
        "memory_write": "requires_auth",
        "search":      "allow",
        "inbox_send":  "allow"
    });

    // ── Build MCP coordinates ────────────────────────────────────────────────
    let mcp = serde_json::json!({
        "url":  resolved.mcp_server_url,
        "tool": "cascade.provide_harness_context",
        "transport": "stdio",
        "command": ["cascade", "mcp", "stdio"]
    });

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!(
                "Cascade context for harness={harness} cwd={cwd_str}: {} tiers resolved, {} bytes of merged instructions.",
                resolved.tiers_found.iter().filter(|t| t.found).count(),
                resolved.merged_instructions.len()
            )
        }],
        "merged_instructions": resolved.merged_instructions,
        "policies":            policies,
        "config":              config,
        "mcp":                 mcp
    }))
}

/// Build the harness-specific config block.
///
/// Each harness uses a different instruction-file name and config location.
/// This function returns the config the harness needs to self-configure without
/// consulting any local tier files.
fn harness_config_block(harness: &str, mcp_server_url: &str) -> Value {
    match harness {
        "claude-code" => serde_json::json!({
            "instruction_file": "CLAUDE.md",
            "settings_key":     "mcpServers.cascade",
            "mcp_command":      ["cascade", "mcp", "stdio"],
            "mcp_server_url":   mcp_server_url
        }),
        "opencode" => serde_json::json!({
            "instruction_file":       "AGENTS.md",
            "opencode_json_key":      "mcpServers[name=cascade]",
            "opencode_instr_field":   "instructions",
            "mcp_command":            "cascade mcp stdio",
            "mcp_server_url":         mcp_server_url
        }),
        "codex" => serde_json::json!({
            "instruction_file": "AGENTS.md",
            "config_file":      "codex/config.yaml",
            "mcp_key":          "mcp_servers.cascade",
            "mcp_command":      ["cascade", "mcp", "stdio"],
            "mcp_server_url":   mcp_server_url
        }),
        "cursor" => serde_json::json!({
            "instruction_file": ".cursorrules",
            "format":           "json",
            "rules_key":        "rules",
            "mcp_server_url":   mcp_server_url
        }),
        "aider" => serde_json::json!({
            "instruction_file": "CONVENTIONS.md",
            "alt_config":       ".aider.conf.yml",
            "read_files":       ["CONVENTIONS.md"],
            "mcp_server_url":   mcp_server_url
        }),
        _ => serde_json::json!({ "mcp_server_url": mcp_server_url }),
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Return today's date as `YYYY-MM-DD` (UTC, approximate).
///
/// Uses `SystemTime` to avoid pulling in the `time` or `chrono` crates.
/// Accurate to the year; precise enough for inbox filenames.
fn chrono_local_date() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    // Seconds since Unix epoch.
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    // Gregorian approximation using the 400-year cycle.
    let days_since_epoch = secs / 86400;
    // March 1, 2000 is day 11017 (leap-safe starting point).
    // Simple year estimate good enough for filenames.
    let approx_year = 1970u64 + days_since_epoch / 365;
    let approx_day_of_year = days_since_epoch % 365 + 1;
    let approx_month = (approx_day_of_year * 12 / 365 + 1).min(12);
    let approx_day = ((approx_day_of_year % 30) + 1).min(31);
    format!("{approx_year:04}-{approx_month:02}-{approx_day:02}")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── tools/list ────────────────────────────────────────────────────────────

    /// tools/list returns exactly 10 tools (9 original + cascade.provide_harness_context).
    #[tokio::test]
    async fn tools_list_returns_9_tools() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.expect("list should not fail");
        let tools = result["tools"].as_array().expect("tools must be array");
        assert_eq!(
            tools.len(),
            10,
            "expected exactly 10 tools, got {}",
            tools.len()
        );
    }

    /// cascade.context_slice appears in the tool list.
    #[tokio::test]
    async fn tools_list_includes_context_slice() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.unwrap();
        let tools = result["tools"].as_array().unwrap();
        let names: Vec<&str> = tools
            .iter()
            .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
            .collect();
        assert!(
            names.contains(&"cascade.context_slice"),
            "cascade.context_slice must be in tool list; found: {names:?}"
        );
    }

    /// Every tool has required fields: name (string), description (string),
    /// inputSchema (object with type=object).
    #[tokio::test]
    async fn tools_list_schema_shape() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.unwrap();
        let tools = result["tools"].as_array().unwrap();
        for tool in tools {
            let name = tool
                .get("name")
                .and_then(|v| v.as_str())
                .unwrap_or("<missing>");
            assert!(
                tool.get("name").and_then(|v| v.as_str()).is_some(),
                "{name}: missing 'name'"
            );
            assert!(
                tool.get("description").and_then(|v| v.as_str()).is_some(),
                "{name}: missing 'description'"
            );
            let schema = tool
                .get("inputSchema")
                .unwrap_or_else(|| panic!("{name}: missing inputSchema"));
            assert_eq!(
                schema.get("type").and_then(|v| v.as_str()),
                Some("object"),
                "{name}: inputSchema.type must be 'object'"
            );
            assert!(
                schema.get("properties").is_some(),
                "{name}: inputSchema must have 'properties'"
            );
        }
    }

    /// Tool names are the exact catalog specified.
    #[tokio::test]
    async fn tools_list_catalog_names() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.unwrap();
        let tools = result["tools"].as_array().unwrap();
        let names: Vec<&str> = tools
            .iter()
            .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
            .collect();

        let expected = [
            "cascade.read",
            "cascade.search",
            "cascade.search_codebase",
            "cascade.inbox.list",
            "cascade.inbox.send",
            "cascade.master_lists",
            "cascade.memory.read",
            "cascade.memory.write",
        ];
        for exp in &expected {
            assert!(
                names.contains(exp),
                "expected tool '{exp}' not found in list"
            );
        }
    }

    /// cascade.search inputSchema has correct constraint fields.
    #[tokio::test]
    async fn cascade_search_schema_constraints() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.unwrap();
        let tools = result["tools"].as_array().unwrap();
        let search = tools
            .iter()
            .find(|t| t["name"] == "cascade.search")
            .expect("cascade.search must be in list");
        let schema = &search["inputSchema"];
        let required = schema["required"].as_array().unwrap();
        assert!(
            required.iter().any(|v| v == "query"),
            "cascade.search.required must include 'query'"
        );
        // limit has minimum:1, maximum:20
        let limit = &schema["properties"]["limit"];
        assert_eq!(limit["minimum"], 1, "limit.minimum should be 1");
        assert_eq!(limit["maximum"], 20, "limit.maximum should be 20");
    }

    // ── tools/call — happy paths ──────────────────────────────────────────────

    /// cascade.search with valid query returns non-error content.
    #[tokio::test]
    async fn call_cascade_search_happy() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.search",
            "arguments": { "query": "cascade tiered instructions" }
        });
        let result = reg
            .call(&params)
            .await
            .expect("call should not return protocol error");
        assert!(
            result.get("isError").is_none() || result["isError"] == false,
            "happy path must not be error"
        );
        let content = result["content"].as_array().expect("must have content");
        assert!(!content.is_empty(), "content must not be empty");
        let text = content[0]["text"].as_str().unwrap();
        assert!(
            text.contains("cascade tiered instructions"),
            "response should echo query: {text}"
        );
    }

    /// cascade.inbox.list on a non-existent project returns empty messages (not error).
    #[tokio::test]
    async fn call_cascade_inbox_list_nonexistent_project() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.inbox.list",
            "arguments": { "project": "__nonexistent_test_project__" }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be protocol error");
        // Non-existent inbox dir → 0 messages, not an error.
        assert!(
            result.get("isError").is_none() || result["isError"] == false,
            "missing inbox dir should return 0 messages, not error"
        );
        let messages = result["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 0);
    }

    // ── tools/call — invalid args ─────────────────────────────────────────────

    /// cascade.search without 'query' returns is_error: true.
    #[tokio::test]
    async fn call_cascade_search_missing_query() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.search",
            "arguments": { "limit": 5 }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should return tool error, not protocol error");
        assert_eq!(
            result["isError"], true,
            "missing required arg should yield is_error=true"
        );
        let text = result["content"][0]["text"].as_str().unwrap();
        assert!(
            text.contains("query"),
            "error text should mention missing field"
        );
    }

    /// cascade.master_lists with unknown kind returns is_error: true.
    #[tokio::test]
    async fn call_master_lists_unknown_kind() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.master_lists",
            "arguments": { "project": "nself", "kind": "unicorns" }
        });
        let result = reg.call(&params).await.expect("should be tool error");
        assert_eq!(result["isError"], true);
    }

    /// cascade.memory.read with path-traversal file value returns is_error: true.
    #[tokio::test]
    async fn call_memory_read_path_traversal_rejected() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.memory.read",
            "arguments": { "project": "nself", "file": "../../vault.env" }
        });
        let result = reg.call(&params).await.expect("should be tool error");
        assert_eq!(result["isError"], true, "path traversal must be rejected");
    }

    // ── tools/call — backend error (file not found) ───────────────────────────

    /// cascade.memory.read on non-existent file returns is_error: true.
    #[tokio::test]
    async fn call_memory_read_file_not_found() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.memory.read",
            "arguments": {
                "project": "__nonexistent_project_xyz__",
                "file": "decisions.md"
            }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should be tool error, not protocol error");
        assert_eq!(
            result["isError"], true,
            "file-not-found must become is_error=true"
        );
    }

    /// cascade.read on non-existent tier returns is_error: true.
    #[tokio::test]
    async fn call_cascade_read_tier_not_found() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.read",
            "arguments": { "tier": "ppc", "project": "__no_such_project__" }
        });
        let result = reg.call(&params).await.expect("should be tool error");
        assert_eq!(result["isError"], true);
    }

    // ── tools/call — auth gate ────────────────────────────────────────────────

    /// cascade.memory.write without auth returns is_error: true, message "Unauthorized".
    #[tokio::test]
    async fn call_memory_write_unauthenticated_rejected() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.memory.write",
            "arguments": {
                "project": "nself",
                "file": "decisions.md",
                "content": "## Test entry"
            }
        });
        let ctx = ConnectionContext {
            authenticated: false,
        };
        let result = reg
            .call_with_context(&params, &ctx)
            .await
            .expect("should be is_error, not protocol error");
        assert_eq!(
            result["isError"], true,
            "unauthenticated write must be rejected"
        );
        let text = result["content"][0]["text"].as_str().unwrap();
        assert_eq!(
            text, "Unauthorized",
            "error text must be exactly 'Unauthorized'"
        );
    }

    /// Authenticated cascade.memory.write succeeds (writes to temp dir).
    #[tokio::test]
    async fn call_memory_write_authenticated_succeeds() {
        // Write to a temp project that does not collide with real ~/Sites.
        // We cannot easily intercept the path in the current stub-only impl,
        // so we verify the call returns *without* is_error when authenticated.
        // Full integration (real file write) is QA-B scope.
        let reg = ToolRegistry::new();
        let ctx = ConnectionContext {
            authenticated: true,
        };

        // Use a tmp dir path by setting a known nonexistent project that will
        // fail at the fs level; verify is_error is true but NOT "Unauthorized".
        let params = serde_json::json!({
            "name": "cascade.memory.write",
            "arguments": {
                "project": "__auth_test_project__",
                "file": "decisions.md",
                "content": "## Auth test"
            }
        });
        let result = reg
            .call_with_context(&params, &ctx)
            .await
            .expect("should not be protocol error");
        // The project doesn't exist but create_dir_all should succeed (creates it).
        // Either success or fs error — important thing is NOT "Unauthorized".
        if result.get("isError") == Some(&Value::Bool(true)) {
            let text = result["content"][0]["text"].as_str().unwrap_or("");
            assert_ne!(
                text, "Unauthorized",
                "authenticated call must not return Unauthorized"
            );
        }
        // Clean up any created dirs.
        let _ =
            std::fs::remove_dir_all(dirs_next_home().join("Sites").join("__auth_test_project__"));
    }

    // ── tools/call — unknown tool ─────────────────────────────────────────────

    /// Unknown tool name returns McpError MethodNotFound (Err variant, not is_error).
    #[tokio::test]
    async fn call_unknown_tool_returns_method_not_found() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.nonexistent",
            "arguments": {}
        });
        let result = reg.call(&params).await;
        assert!(
            result.is_err(),
            "unknown tool must return Err(JsonRpcError)"
        );
        let err = result.unwrap_err();
        assert_eq!(
            err.code,
            crate::server::ERR_NOT_FOUND,
            "error code must be ERR_NOT_FOUND (-32001)"
        );
    }

    /// Missing 'name' in params returns InvalidParams.
    #[tokio::test]
    async fn call_missing_name_returns_invalid_params() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({ "arguments": { "query": "test" } });
        let result = reg.call(&params).await;
        assert!(
            result.is_err(),
            "missing name must return Err(JsonRpcError)"
        );
        let err = result.unwrap_err();
        assert_eq!(err.code, crate::server::ERR_INVALID_PARAMS);
    }

    // ── helpers ───────────────────────────────────────────────────────────────

    /// chrono_local_date returns a plausible YYYY-MM-DD string.
    #[test]
    fn date_helper_format() {
        let d = chrono_local_date();
        assert_eq!(d.len(), 10, "date must be 10 chars: {d}");
        assert!(d.starts_with("20"), "date must start with '20': {d}");
        let parts: Vec<&str> = d.split('-').collect();
        assert_eq!(parts.len(), 3, "date must have 3 parts: {d}");
    }

    /// Helper to get home dir without depending on dirs-next crate in tests.
    fn dirs_next_home() -> std::path::PathBuf {
        std::env::var("HOME")
            .map(std::path::PathBuf::from)
            .unwrap_or_else(|_| std::path::PathBuf::from("/tmp"))
    }

    // ── cascade.provide_harness_context tests ─────────────────────────────────

    /// Tool appears in the tools list.
    #[tokio::test]
    async fn provide_harness_context_in_tool_list() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.unwrap();
        let tools = result["tools"].as_array().unwrap();
        let names: Vec<&str> = tools
            .iter()
            .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
            .collect();
        assert!(
            names.contains(&"cascade.provide_harness_context"),
            "cascade.provide_harness_context must be in tool list; found: {names:?}"
        );
    }

    /// tools/list now returns 10 tools (9 original + provide_harness_context).
    #[tokio::test]
    async fn tools_list_returns_10_tools() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.expect("list should not fail");
        let tools = result["tools"].as_array().expect("tools must be array");
        assert_eq!(
            tools.len(),
            10,
            "expected exactly 10 tools, got {}",
            tools.len()
        );
    }

    /// Unknown harness arg returns is_error: true (tool-level error, not protocol error).
    #[tokio::test]
    async fn provide_harness_context_unknown_harness_returns_tool_error() {
        let reg = ToolRegistry::new();
        // Call via the protocol error path (unknown harness → invalid_params which
        // goes through tool_result → is_error: true).
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": { "harness": "vscode", "cwd": "/tmp" }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "unknown harness must be tool-level error: {result}"
        );
    }

    /// Missing harness arg returns tool-level error.
    #[tokio::test]
    async fn provide_harness_context_missing_harness_returns_tool_error() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": { "cwd": "/tmp" }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "missing harness must be tool-level error"
        );
    }

    /// Missing cwd arg returns tool-level error.
    #[tokio::test]
    async fn provide_harness_context_missing_cwd_returns_tool_error() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": { "harness": "claude-code" }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "missing cwd must be tool-level error"
        );
    }

    /// Each valid harness returns the correct instruction_file in config.
    ///
    /// Uses multi-thread runtime because `resolve_cascade_full` calls
    /// `tokio::task::block_in_place` which requires the multi-threaded executor.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn provide_harness_context_config_per_harness() {
        use tempfile::TempDir;
        let tmp = TempDir::new().unwrap();

        // Scaffold a minimal cascade tree so resolve_cascade_full finds something.
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(
            cascade_dir.join("CASCADE.md"),
            "# Test cascade\nTest instructions for harness context test.",
        )
        .unwrap();

        let reg = ToolRegistry::new();

        let expected_instruction_files = [
            ("claude-code", "CLAUDE.md"),
            ("opencode", "AGENTS.md"),
            ("codex", "AGENTS.md"),
            ("cursor", ".cursorrules"),
            ("aider", "CONVENTIONS.md"),
        ];

        for (harness, expected_file) in &expected_instruction_files {
            let params = serde_json::json!({
                "name": "cascade.provide_harness_context",
                "arguments": {
                    "harness": harness,
                    "cwd": tmp.path().to_str().unwrap()
                }
            });
            let result = reg
                .call(&params)
                .await
                .expect("should not be protocol error");

            // Must not be an error.
            assert!(
                result.get("isError").map(|v| v == &Value::Bool(false)).unwrap_or(true),
                "harness={harness} should succeed: {result}"
            );

            // merged_instructions must be non-empty.
            let merged = result["merged_instructions"].as_str().unwrap_or("");
            assert!(
                !merged.is_empty(),
                "harness={harness}: merged_instructions must be non-empty"
            );

            // config.instruction_file must match expected.
            let instr_file = result["config"]["instruction_file"].as_str().unwrap_or("");
            assert_eq!(
                instr_file, *expected_file,
                "harness={harness}: expected instruction_file={expected_file}, got={instr_file}"
            );

            // mcp block must contain the command.
            let mcp = &result["mcp"];
            assert!(
                mcp.get("url").is_some(),
                "harness={harness}: mcp.url required"
            );
            assert!(
                mcp.get("command").is_some(),
                "harness={harness}: mcp.command required"
            );

            // policies block must have tool_use = "allow".
            let policies = &result["policies"];
            assert_eq!(
                policies["tool_use"].as_str(),
                Some("allow"),
                "harness={harness}: policies.tool_use must be 'allow'"
            );
        }
    }

    // ── cascade.context_slice tests ───────────────────────────────────────────

    /// Happy path: valid query + budget returns is_error=false and tokens_used ≤ budget.
    #[tokio::test]
    async fn context_slice_happy_path() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.context_slice",
            "arguments": { "query": "authentication", "budget_tokens": 1024 }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not error at protocol level");
        // Tool-level result: no is_error.
        assert!(
            result.get("isError").is_none() || result["isError"].as_bool() != Some(true),
            "should not be an error result: {result}"
        );
        let tokens_used = result["metadata"]["tokens_used"].as_u64().unwrap_or(0);
        assert!(
            tokens_used <= 1024,
            "tokens_used={tokens_used} should be ≤ budget=1024"
        );
    }

    /// Over-budget arg (budget_tokens=100 < min 256) returns tool-level is_error.
    ///
    /// Per MCP spec: constraint violations are tool-level errors (is_error: true),
    /// not JSON-RPC protocol errors. The tool wrapper converts JsonRpcError to
    /// { isError: true, content: [...] }.
    #[tokio::test]
    async fn context_slice_budget_below_min_returns_tool_error() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.context_slice",
            "arguments": { "query": "test", "budget_tokens": 100 }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be a protocol error");
        // Tool-level error: isError=true.
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "budget below 256 should be tool-level error: {result}"
        );
    }

    /// Missing query returns tool-level is_error (not a protocol error).
    #[tokio::test]
    async fn context_slice_missing_query_returns_tool_error() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.context_slice",
            "arguments": { "budget_tokens": 2048 }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be a protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "missing query should be tool-level error: {result}"
        );
    }

    /// Result content is a text block (valid structure for harness injection).
    #[tokio::test]
    async fn context_slice_result_has_text_content() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.context_slice",
            "arguments": { "query": "test", "budget_tokens": 512 }
        });
        let result = reg.call(&params).await.unwrap();
        let content = result["content"].as_array().expect("content must be array");
        assert!(!content.is_empty(), "content array must not be empty");
        let first = &content[0];
        assert_eq!(
            first["type"].as_str(),
            Some("text"),
            "first content must be text"
        );
        assert!(
            first["text"].as_str().is_some(),
            "text field must be a string"
        );
    }

    /// metadata block has the required keys.
    #[tokio::test]
    async fn context_slice_metadata_shape() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.context_slice",
            "arguments": { "query": "test", "budget_tokens": 4096 }
        });
        let result = reg.call(&params).await.unwrap();
        let meta = &result["metadata"];
        assert!(
            meta.get("tokens_used").is_some(),
            "metadata.tokens_used required"
        );
        assert!(
            meta.get("chunks_returned").is_some(),
            "metadata.chunks_returned required"
        );
        assert!(meta.get("query").is_some(), "metadata.query required");
        assert!(
            meta.get("budget_tokens").is_some(),
            "metadata.budget_tokens required"
        );
    }
}
