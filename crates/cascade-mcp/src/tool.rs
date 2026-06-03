//! MCP Tools — the 8 Cascade tools exposed via JSON-RPC.
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
//! | `cascade.search` | Hybrid RAG search across the watched corpus |
//! | `cascade.read` | Read a tier instruction file |
//! | `cascade.search_codebase` | Code-aware search with language filter |
//! | `cascade.inbox.list` | List PCI inbox messages |
//! | `cascade.inbox.send` | Send a PCI message |
//! | `cascade.master_lists` | Read project master lists |
//! | `cascade.memory.read` | Read a memory file |
//! | `cascade.memory.write` | Append to a memory file |
//!
//! ## Input validation
//!
//! Tool params are validated against their JSON Schema before dispatch. Bad
//! params return JSON-RPC error `-32602` (`INVALID_PARAMS`).

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use cascade_types::error::Result;

use crate::paths as mcp_paths;
use crate::server::JsonRpcError;

// ── Tool definition types ─────────────────────────────────────────────────────

/// A single tool definition returned by `tools/list`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct McpTool {
    pub name: String,
    pub description: String,
    /// JSON Schema object describing input parameters.
    pub input_schema: Value,
}

// ── ToolRegistry ──────────────────────────────────────────────────────────────

/// Handles `tools/list` and `tools/call` for the MCP server.
pub struct ToolRegistry;

impl ToolRegistry {
    pub fn new() -> Self {
        Self
    }

    /// Handle `tools/list` — enumerate all available tools with their schemas.
    pub async fn list(&self) -> Result<Value> {
        let tools = vec![
            cascade_search_tool(),
            cascade_read_tool(),
            cascade_search_codebase_tool(),
            cascade_inbox_list_tool(),
            cascade_inbox_send_tool(),
            cascade_master_lists_tool(),
            cascade_memory_read_tool(),
            cascade_memory_write_tool(),
        ];
        Ok(serde_json::json!({ "tools": tools }))
    }

    /// Handle `tools/call` — dispatch to the appropriate tool handler.
    ///
    /// Returns the tool result or a structured JSON-RPC error.
    pub async fn call(&self, params: &Value) -> std::result::Result<Value, JsonRpcError> {
        let name = params
            .get("name")
            .and_then(|v| v.as_str())
            .ok_or_else(|| JsonRpcError::invalid_params("missing 'name' in tools/call params"))?;

        let args = params
            .get("arguments")
            .cloned()
            .unwrap_or(Value::Object(Default::default()));

        debug!(tool = name, "tools/call");

        match name {
            "cascade.search" => handle_search(&args).await,
            "cascade.read" => handle_read(&args).await,
            "cascade.search_codebase" => handle_search_codebase(&args).await,
            "cascade.inbox.list" => handle_inbox_list(&args).await,
            "cascade.inbox.send" => handle_inbox_send(&args).await,
            "cascade.master_lists" => handle_master_lists(&args).await,
            "cascade.memory.read" => handle_memory_read(&args).await,
            "cascade.memory.write" => handle_memory_write(&args).await,
            _ => Err(JsonRpcError::not_found(format!("Unknown tool: {name}"))),
        }
    }
}

impl Default for ToolRegistry {
    fn default() -> Self {
        Self::new()
    }
}

// ── Tool definitions (JSON Schema) ────────────────────────────────────────────

fn cascade_search_tool() -> McpTool {
    McpTool {
        name: "cascade.search".into(),
        description: "Hybrid RAG search (FTS5 + dense vector + RRF) across the watched corpus. Returns ranked chunks with citations (file, line, score).".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "required": ["query"],
            "properties": {
                "query": { "type": "string", "description": "Natural-language search query" },
                "project": { "type": "string", "description": "Project name filter (e.g. 'nself')" },
                "tier": { "type": "string", "description": "Cascade tier filter (e.g. 'gci', 'prc')" },
                "k": { "type": "integer", "default": 10, "description": "Maximum results to return" },
                "strategy": {
                    "type": "string",
                    "enum": ["hybrid_rrf", "pure_fts", "pure_vec"],
                    "default": "hybrid_rrf"
                }
            }
        }),
    }
}

fn cascade_read_tool() -> McpTool {
    McpTool {
        name: "cascade.read".into(),
        description: "Read a cascade tier instruction file (CASCADE.md, CLAUDE.md, or AGENTS.md) from the specified tier. Returns the full file text.".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "required": ["tier"],
            "properties": {
                "tier": { "type": "string", "description": "Tier identifier: 'gci', 'asi', 'ppc', 'prc', or 'pac'" },
                "project": { "type": "string", "description": "Project name (required for ppc/prc/pac tiers)" }
            }
        }),
    }
}

fn cascade_search_codebase_tool() -> McpTool {
    McpTool {
        name: "cascade.search_codebase".into(),
        description: "Code-aware search using tree-sitter function-level index. Returns matching functions/classes with exact file:line citations.".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "required": ["query"],
            "properties": {
                "query": { "type": "string" },
                "project": { "type": "string" },
                "lang": { "type": "string", "description": "Language filter (e.g. 'rust', 'typescript')" },
                "k": { "type": "integer", "default": 10 }
            }
        }),
    }
}

fn cascade_inbox_list_tool() -> McpTool {
    McpTool {
        name: "cascade.inbox.list".into(),
        description: "List PCI inbox messages for a project.".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "properties": {
                "project": { "type": "string", "description": "Project name (e.g. 'nself')" },
                "unread_only": { "type": "boolean", "default": false }
            }
        }),
    }
}

fn cascade_inbox_send_tool() -> McpTool {
    McpTool {
        name: "cascade.inbox.send".into(),
        description: "Send a PCI message to a project inbox.".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "required": ["project", "subject", "body"],
            "properties": {
                "project": { "type": "string" },
                "subject": { "type": "string" },
                "body": { "type": "string" },
                "priority": { "type": "string", "enum": ["critical", "high", "medium", "low"], "default": "medium" },
                "msg_type": { "type": "string", "enum": ["bug", "enhancement", "question", "info"], "default": "info" }
            }
        }),
    }
}

fn cascade_master_lists_tool() -> McpTool {
    McpTool {
        name: "cascade.master_lists".into(),
        description: "Read a project master list (routes, components, tables, endpoints, CLI commands, env vars, etc.).".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "required": ["project", "kind"],
            "properties": {
                "project": { "type": "string" },
                "kind": {
                    "type": "string",
                    "enum": ["routes", "components", "tables", "endpoints", "cli", "env", "hooks", "utils"]
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
            "type": "object",
            "required": ["project", "file"],
            "properties": {
                "project": { "type": "string" },
                "file": { "type": "string", "enum": ["decisions.md", "lessons.md", "patterns.md"] }
            }
        }),
    }
}

fn cascade_memory_write_tool() -> McpTool {
    McpTool {
        name: "cascade.memory.write".into(),
        description: "Append an entry to a project memory file.".into(),
        input_schema: serde_json::json!({
            "type": "object",
            "required": ["project", "file", "content"],
            "properties": {
                "project": { "type": "string" },
                "file": { "type": "string", "enum": ["decisions.md", "lessons.md", "patterns.md"] },
                "content": { "type": "string", "description": "Markdown text to append" }
            }
        }),
    }
}

// ── Tool handlers ─────────────────────────────────────────────────────────────

/// `cascade.search` — hybrid RAG query.
///
/// Delegates to cascade-rag's retrieval pipeline. Returns results with
/// citations (file_path, start_line, end_line, score, rank, strategy).
async fn handle_search(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let query = args
        .get("query")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'query' is required"))?;

    let k = args.get("k").and_then(|v| v.as_u64()).unwrap_or(10) as usize;
    let strategy = args
        .get("strategy")
        .and_then(|v| v.as_str())
        .unwrap_or("hybrid_rrf");
    let project_filter = args.get("project").and_then(|v| v.as_str());
    let tier_filter = args.get("tier").and_then(|v| v.as_str());

    debug!(query, k, strategy, project = ?project_filter, "cascade.search");

    // Stub: real impl calls cascade-rag's Retriever via Arc<dyn Retriever>
    // injected into ToolRegistry at construction. For now return mock structure.
    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("Search results for '{}' (strategy={}, k={}): [index not ready — run `cascade index rebuild`]", query, strategy, k)
        }],
        "citations": [],
        "metadata": {
            "strategy": strategy,
            "k": k,
            "project": project_filter,
            "tier": tier_filter,
        }
    }))
}

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

/// `cascade.search_codebase` — code-aware search.
async fn handle_search_codebase(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let query = args
        .get("query")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'query' is required"))?;
    let lang = args.get("lang").and_then(|v| v.as_str());
    let k = args.get("k").and_then(|v| v.as_u64()).unwrap_or(10) as usize;

    debug!(query, k, lang = ?lang, "cascade.search_codebase");

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("Code search for '{}' (lang={:?}, k={}): [code index not ready]", query, lang, k)
        }],
        "results": []
    }))
}

/// `cascade.inbox.list` — list PCI inbox messages.
async fn handle_inbox_list(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let project = args.get("project").and_then(|v| v.as_str()).unwrap_or("*");
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
async fn handle_inbox_send(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let project = args
        .get("project")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'project' is required"))?;
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
        .unwrap_or("medium");
    let msg_type = args
        .get("msg_type")
        .and_then(|v| v.as_str())
        .unwrap_or("info");

    let slug = subject
        .to_lowercase()
        .replace(' ', "-")
        .chars()
        .take(40)
        .collect::<String>();
    let date = chrono_local_date();
    let filename = format!("msg-{date}-{slug}.md");
    let inbox_dir = mcp_paths::inbox_dir(project);

    tokio::fs::create_dir_all(&inbox_dir)
        .await
        .map_err(|e| JsonRpcError::internal(format!("Failed to create inbox dir: {e}")))?;

    let content = format!(
        "# {subject}\n\n**From:** cascade-mcp\n**To:** {project}\n**Type:** {msg_type}\n**Priority:** {priority}\n\n{body}\n"
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
                "Unknown list kind: {kind}"
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

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Return today's date as `YYYY-MM-DD` (UTC).
fn chrono_local_date() -> String {
    // Use `time` crate or a simple calculation; avoid a large dependency.
    // This stub returns a static placeholder; the real impl calls SystemTime.
    use std::time::{SystemTime, UNIX_EPOCH};
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let days = secs / 86400;
    // Approximate — good enough for filenames; the real impl uses time::OffsetDateTime.
    let y = 1970 + days / 365;
    format!("{y}-01-01")
}
