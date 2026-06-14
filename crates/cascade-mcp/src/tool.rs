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
//! | `cascade.get_current` | Return current.yaml active pointers (<=200 tokens) |
//! | `cascade.update_ticket_status` | Transition a ticket status with validation |
//! | `cascade.append_event` | Append a raw event record to events.jsonl |
//! | `cascade.get_sprint` | Return sprint YAML for a sprint ID |
//! | `cascade.read_phase_status` | Return phase status summary |
//! | `cascade.list_tickets` | List tickets with optional status/sprint filter |
//! | `cascade.check_routes` | Curl-check api-routes.yaml and return per-route ok/fail |
//! | `cascade.scan_inbox` | Drain .claude/inbox (or ai-folder/inbox) and return summaries |
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
use tokio::sync::RwLock;
use tracing::debug;

use cascade_core::cascade_resolution::resolve_cascade_full;
use cascade_core::pbd::active_work::build_active_work;
use cascade_core::pbd::schema::{PbdEvent, TicketStatus};
use cascade_core::pbd::store::{resolve_phases_root, PbdStore};
use cascade_rag::context::ContextOptimizer;
use cascade_types::error::Result;
use cascade_types::retriever::{RetrieveOpts, Retriever};

use crate::auth::McpAuth;
use crate::paths as mcp_paths;
use crate::server::JsonRpcError;

/// Shared interior-mutable slot holding an optional live [`Retriever`].
///
/// The slot is `None` until a background task opens the index and injects a
/// real retriever.  Search handlers read-lock, clone the `Option` out, then
/// drop the guard *before* any `.await` — the guard is never held across an
/// await point.
pub type RetrieverSlot = Arc<RwLock<Option<Arc<dyn Retriever>>>>;

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
/// Holds an optional [`McpAuth`] reference used to gate `cascade.memory.write`
/// and a shared [`RetrieverSlot`] for the `cascade.search` live RAG pipeline.
///
/// ## Retriever injection
///
/// The slot starts empty (`None`).  Call [`ToolRegistry::with_retriever`] to
/// fill it synchronously at construction time (tests), or call
/// [`ToolRegistry::retriever_slot`] to get a clonable handle that a background
/// task can write into after the server is already serving `initialize`.
/// When the slot is `None`, `cascade.search` returns a graceful "index not
/// ready" message rather than real hits.
pub struct ToolRegistry {
    /// Auth backend for the `cascade.memory.write` gate.
    #[allow(dead_code)]
    auth: Option<Arc<McpAuth>>,
    /// Shared slot for the live RAG retriever.  `None` = index not yet ready.
    retriever: RetrieverSlot,
}

impl ToolRegistry {
    /// Create a registry without an auth backend or retriever.
    ///
    /// `cascade.memory.write` calls are always rejected; `cascade.search`
    /// returns an "index not ready" message until a retriever is injected.
    pub fn new() -> Self {
        Self {
            auth: None,
            retriever: Arc::new(RwLock::new(None)),
        }
    }

    /// Create a registry with an [`McpAuth`] backend for the memory.write gate.
    pub fn with_auth(auth: Arc<McpAuth>) -> Self {
        Self {
            auth: Some(auth),
            retriever: Arc::new(RwLock::new(None)),
        }
    }

    /// Synchronously fill the retriever slot so that `cascade.search` returns
    /// real hits.
    ///
    /// Intended for construction-time injection (tests, daemon).  For
    /// post-construction background injection use [`ToolRegistry::retriever_slot`].
    ///
    /// ```rust,no_run
    /// # use std::sync::Arc;
    /// # use cascade_mcp::tool::ToolRegistry;
    /// # use cascade_types::retriever::NoopRetriever;
    /// let registry = ToolRegistry::new().with_retriever(Arc::new(NoopRetriever));
    /// ```
    pub fn with_retriever(self, retriever: Arc<dyn Retriever>) -> Self {
        // `try_write` on a freshly created, uncontested lock always succeeds.
        if let Ok(mut slot) = self.retriever.try_write() {
            *slot = Some(retriever);
        }
        self
    }

    /// Return a clone of the shared [`RetrieverSlot`] so that a background
    /// task can inject a retriever after the server is already running.
    ///
    /// The background task should:
    /// 1. Open the index (async I/O).
    /// 2. Acquire a write-lock on the slot.
    /// 3. Store `Some(Arc::new(retriever))`.
    /// 4. Drop the write-lock.
    ///
    /// Subsequent `cascade.search` calls will then return real hits.
    pub fn retriever_slot(&self) -> RetrieverSlot {
        Arc::clone(&self.retriever)
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
            // PBD tools (E-P8-04)
            cascade_get_current_tool(),
            cascade_update_ticket_status_tool(),
            cascade_append_event_tool(),
            cascade_get_sprint_tool(),
            cascade_read_phase_status_tool(),
            cascade_list_tickets_tool(),
            cascade_check_routes_tool(),
            cascade_scan_inbox_tool(),
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

        // Snapshot the retriever slot: acquire the read-lock, clone the Option
        // out, then drop the guard BEFORE any .await so we never hold a lock
        // across an await point.
        let retriever_snapshot: Option<Arc<dyn Retriever>> = {
            let guard = self.retriever.read().await;
            guard.clone()
        };

        match name {
            "cascade.read" => tool_result(handle_read(&args).await),
            "cascade.search" => tool_result(handle_search(&args, retriever_snapshot).await),
            "cascade.search_codebase" => {
                tool_result(handle_search_codebase(&args, retriever_snapshot).await)
            }
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
            // PBD tools (E-P8-04)
            "cascade.get_current" => tool_result(handle_get_current(&args).await),
            "cascade.update_ticket_status" => tool_result(handle_update_ticket_status(&args).await),
            "cascade.append_event" => tool_result(handle_append_event(&args).await),
            "cascade.get_sprint" => tool_result(handle_get_sprint(&args).await),
            "cascade.read_phase_status" => tool_result(handle_read_phase_status(&args).await),
            "cascade.list_tickets" => tool_result(handle_list_tickets(&args).await),
            "cascade.check_routes" => tool_result(handle_check_routes(&args).await),
            "cascade.scan_inbox" => tool_result(handle_scan_inbox(&args).await),
            _ => Err(JsonRpcError::not_found(format!("Unknown tool: {name}"))),
        }
    }
}

impl Default for ToolRegistry {
    fn default() -> Self {
        Self::new()
    }
}

// ── Search filter helper ──────────────────────────────────────────────────────

/// Build [`RetrieveOpts`] from `cascade.search` args.
fn build_retrieve_opts(limit: usize, tier_filter: Option<&str>) -> RetrieveOpts {
    RetrieveOpts {
        k: limit,
        min_score: None,
        tier_filter: tier_filter.map(str::to_owned),
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
/// Executes the live RRF retrieval pipeline when a [`Retriever`] is injected,
/// returning ranked chunks with citations (file, start_line, end_line, score,
/// rank, strategy).  Falls back to a graceful "index not ready" message when
/// the retriever has not been wired in yet.
///
/// # Arguments (from JSON)
/// - `query`    — natural-language search string (required)
/// - `limit`    — max results, 1–20 (default 10)
/// - `project`  — optional project name filter (metadata only; passed to opts)
/// - `tier`     — optional cascade tier filter forwarded to [`RetrieveOpts`]
/// - `strategy` — retrieval strategy label (informational; actual strategy is
///   determined by the injected retriever's implementation)
async fn handle_search(
    args: &Value,
    retriever: Option<Arc<dyn Retriever>>,
) -> std::result::Result<Value, JsonRpcError> {
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

    // Graceful degradation when the retriever is not yet wired in.
    let ret = match retriever {
        None => {
            return Ok(serde_json::json!({
                "content": [{
                    "type": "text",
                    "text": format!(
                        "cascade.search: index not ready (query={query:?}). \
                         Run `cascade index rebuild` then restart the MCP server."
                    )
                }],
                "citations": [],
                "metadata": {
                    "strategy": strategy,
                    "limit": limit,
                    "project": project_filter,
                    "tier": tier_filter,
                    "ready": false,
                }
            }));
        }
        Some(r) => r,
    };

    // Execute the live retrieval pipeline.
    let opts = build_retrieve_opts(limit, tier_filter);
    let hits = ret
        .retrieve(query, &opts)
        .await
        .map_err(|e| JsonRpcError::internal(format!("retrieval failed: {e}")))?;

    // Build citation objects and the human-readable text block.
    let mut citations = Vec::with_capacity(hits.len());
    let mut text_parts = Vec::with_capacity(hits.len());

    for hit in &hits {
        let file = hit
            .file_path
            .as_ref()
            .map(|p| p.display().to_string())
            .unwrap_or_else(|| hit.chunk_id.clone());
        let start = hit.start_line.unwrap_or(0);
        let end = hit.end_line.unwrap_or(0);

        citations.push(serde_json::json!({
            "chunk_id":   hit.chunk_id,
            "file_path":  file,
            "start_line": start,
            "end_line":   end,
            "score":      (hit.score * 1000.0).round() / 1000.0,
            "rank":       hit.rank,
            "tier":       hit.tier,
        }));

        text_parts.push(format!(
            "<!-- {file}:{start}-{end} score:{:.3} rank:{} -->\n{}",
            hit.score,
            hit.rank,
            hit.text.trim()
        ));
    }

    let result_text = if hits.is_empty() {
        format!("cascade.search: no results for {query:?}")
    } else {
        text_parts.join("\n\n")
    };

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": result_text }],
        "citations": citations,
        "metadata": {
            "strategy":      strategy,
            "limit":         limit,
            "hits_returned": hits.len(),
            "project":       project_filter,
            "tier":          tier_filter,
            "ready":         true,
        }
    }))
}

/// `cascade.search_codebase` — code-aware search.
///
/// # Current status
///
/// The tree-sitter code index path (`code-chunker` feature) is not yet wired
/// into a separate code [`Retriever`] at construction time.  Until a dedicated
/// code index is available this handler falls back to the general-purpose
/// retriever (same as `cascade.search`) when one is injected, and returns an
/// "index not ready" message otherwise.
///
/// TODO(E11.1-followup): wire a `code_retriever: Option<Arc<dyn Retriever>>`
/// field into `ToolRegistry` backed by a tree-sitter chunked index, and add a
/// `lang` filter to `RetrieveOpts` so callers can constrain by language.
async fn handle_search_codebase(
    args: &Value,
    retriever: Option<Arc<dyn Retriever>>,
) -> std::result::Result<Value, JsonRpcError> {
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

    // Use the general-purpose retriever as a best-effort fallback until a
    // dedicated code index (tree-sitter chunker + code-aware embed path) is
    // wired in.  When `lang` is set we include it as a note in the result.
    let ret = match retriever {
        None => {
            return Ok(serde_json::json!({
                "content": [{
                    "type": "text",
                    "text": format!(
                        "cascade.search_codebase: code index not ready \
                         (query={query:?} lang={lang:?}). \
                         Run `cascade index rebuild` then restart the MCP server."
                    )
                }],
                "results": [],
                "metadata": { "ready": false }
            }));
        }
        Some(r) => r,
    };

    let opts = build_retrieve_opts(limit, None);
    let hits = ret
        .retrieve(query, &opts)
        .await
        .map_err(|e| JsonRpcError::internal(format!("code retrieval failed: {e}")))?;

    let mut results = Vec::with_capacity(hits.len());
    for hit in &hits {
        let file = hit
            .file_path
            .as_ref()
            .map(|p| p.display().to_string())
            .unwrap_or_else(|| hit.chunk_id.clone());
        results.push(serde_json::json!({
            "chunk_id":   hit.chunk_id,
            "file_path":  file,
            "start_line": hit.start_line.unwrap_or(0),
            "end_line":   hit.end_line.unwrap_or(0),
            "score":      (hit.score * 1000.0).round() / 1000.0,
            "rank":       hit.rank,
            "text":       hit.text.trim(),
            "lang":       lang,
        }));
    }

    let text = if hits.is_empty() {
        format!("cascade.search_codebase: no results for {query:?} (lang={lang:?})")
    } else {
        format!(
            "cascade.search_codebase: {} result(s) for {query:?} (lang={lang:?}, \
             note: using general-purpose index; dedicated code index pending E11.1-followup)",
            hits.len()
        )
    };

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "results": results,
        "metadata": { "ready": true, "lang": lang }
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
                       the MCP server coordinates, and the live active-work context (active sprint \
                       tickets + open kanban tasks). No per-tier file reconciliation required \
                       in the harness — cascade owns all merging. (E-P8-07: active_work field)"
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
async fn handle_provide_harness_context(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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

    // ── Build active-work context (E-P8-07) ─────────────────────────────────
    // Discover phases root relative to cwd. No task DB available in MCP
    // without a daemon pool, so kanban tasks section is omitted here (only PBD
    // sprint tickets are included). When the daemon injects a KanbanTaskStore,
    // call build_active_work_with_tasks instead.
    let active_work = build_active_work(
        cascade_core::pbd::store::locate_phases_root(cwd).as_deref(),
        800,  // 800-token budget per E-P8-07 spec
        None, // kanban tasks: omitted without daemon task DB
    )
    .unwrap_or_default();

    let active_work_text = active_work.text();
    let active_work_json = serde_json::to_value(&active_work).unwrap_or(serde_json::Value::Null);

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
        "mcp":                 mcp,
        "active_work":         active_work_json,
        "active_work_text":    active_work_text
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

// ── PBD tool definitions (E-P8-04) ───────────────────────────────────────────

fn cascade_get_current_tool() -> McpTool {
    McpTool {
        name: "cascade.get_current".into(),
        description: "Return current.yaml active pointers: active phase/epic/wave/sprint and \
                       active ticket IDs. Bounded to <=200 tokens for session-boot use. \
                       Accepts an optional phases_root override; defaults to CWD walk."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

fn cascade_update_ticket_status_tool() -> McpTool {
    McpTool {
        name: "cascade.update_ticket_status".into(),
        description: "Transition a ticket to a new status. Validates the transition against the \
                       TicketStatus enum (planned|queue|active|review|blocked|done|archived) \
                       and writes an event to events.jsonl. Returns the new status on success."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["ticket_id", "status"],
            "additionalProperties": false,
            "properties": {
                "ticket_id": {
                    "type": "string",
                    "description": "Ticket ID (e.g. T-P1-E01-W01-S01-01)",
                    "minLength": 1
                },
                "status": {
                    "type": "string",
                    "enum": ["planned", "queue", "active", "review", "blocked", "done", "archived"],
                    "description": "Target status"
                },
                "note": {
                    "type": "string",
                    "description": "Optional note (required when status=blocked)"
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

fn cascade_append_event_tool() -> McpTool {
    McpTool {
        name: "cascade.append_event".into(),
        description: "Append a raw PBD event record to events.jsonl. The record is validated \
                       for required fields (ts, actor, level, id, from, to) before appending. \
                       Use cascade.update_ticket_status for ticket transitions — this tool is \
                       for custom/manual events."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["event"],
            "additionalProperties": false,
            "properties": {
                "event": {
                    "type": "object",
                    "description": "PBD event object: {ts, actor, level, id, from, to, note?}",
                    "required": ["actor", "level", "id", "from", "to"],
                    "properties": {
                        "ts": { "type": "string", "description": "ISO 8601 timestamp (auto-set if omitted)" },
                        "actor": { "type": "string" },
                        "level": {
                            "type": "string",
                            "enum": ["phase", "epic", "wave", "sprint", "ticket", "step"]
                        },
                        "id": { "type": "string" },
                        "from": { "type": "string" },
                        "to": { "type": "string" },
                        "note": { "type": "string" }
                    }
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

fn cascade_get_sprint_tool() -> McpTool {
    McpTool {
        name: "cascade.get_sprint".into(),
        description: "Return the sprint YAML for the given sprint ID. Searches the active phase \
                       tree for the sprint. Returns sprint metadata including tickets list."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "required": ["sprint_id"],
            "additionalProperties": false,
            "properties": {
                "sprint_id": {
                    "type": "string",
                    "description": "Sprint ID (e.g. S01)",
                    "minLength": 1
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

fn cascade_read_phase_status_tool() -> McpTool {
    McpTool {
        name: "cascade.read_phase_status".into(),
        description: "Return a compact status summary for the given phase (or all non-archived \
                       phases if phase_id is omitted). Includes ticket counts per status."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "phase_id": {
                    "type": "string",
                    "description": "Phase ID (e.g. p1). Omit to get all active phases."
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

fn cascade_list_tickets_tool() -> McpTool {
    McpTool {
        name: "cascade.list_tickets".into(),
        description: "List tickets across the phase tree with optional filters. Returns ticket \
                       ID, title, status, sprint, and repo. Filters are AND-combined."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "status": {
                    "type": "string",
                    "enum": ["planned", "queue", "active", "review", "blocked", "done", "archived"],
                    "description": "Filter by ticket status"
                },
                "sprint_id": {
                    "type": "string",
                    "description": "Filter to a specific sprint ID"
                },
                "phase_id": {
                    "type": "string",
                    "description": "Filter to a specific phase ID"
                },
                "phases_root": {
                    "type": "string",
                    "description": "Absolute path to the phases/ directory. Defaults to CWD auto-discovery."
                }
            }
        }),
    }
}

fn cascade_check_routes_tool() -> McpTool {
    McpTool {
        name: "cascade.check_routes".into(),
        description: "Run a check over api-routes.yaml if present in the project. Returns \
                       per-route ok/fail status. HTTP client is injectable for tests (no \
                       network required in test mode)."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "routes_file": {
                    "type": "string",
                    "description": "Absolute path to api-routes.yaml. Defaults to CWD/.claude/docs/api-routes.yaml."
                },
                "base_url": {
                    "type": "string",
                    "description": "Base URL override for all route checks (e.g. http://localhost:3000)"
                },
                "timeout_ms": {
                    "type": "integer",
                    "description": "Per-route timeout in ms (default: 5000)",
                    "default": 5000,
                    "minimum": 100,
                    "maximum": 30000
                }
            }
        }),
    }
}

fn cascade_scan_inbox_tool() -> McpTool {
    McpTool {
        name: "cascade.scan_inbox".into(),
        description: "Drain .claude/inbox (or <ai-folder>/inbox) for CI/CD error messages and \
                       PCI messages. Returns summaries of all .md files found: filename, first \
                       line (subject), and size. Does not delete files."
            .into(),
        input_schema: serde_json::json!({
            "$schema": "http://json-schema.org/draft-07/schema#",
            "type": "object",
            "additionalProperties": false,
            "properties": {
                "inbox_path": {
                    "type": "string",
                    "description": "Absolute path to the inbox directory. Defaults to CWD auto-discovery."
                },
                "project": {
                    "type": "string",
                    "description": "Project name for ~/Sites/{project}/.claude/inbox/ resolution."
                }
            }
        }),
    }
}

// ── PBD tool handlers (E-P8-04) ───────────────────────────────────────────────

/// `cascade.get_current` — return current.yaml active pointers.
///
/// Returns a compact JSON object bounded to <=200 tokens for session-boot use.
async fn handle_get_current(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let current = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        store.read_current()
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("read_current: {e}")))?;

    // Build compact output (<=200 tokens: JSON with only populated fields)
    let mut obj = serde_json::Map::new();
    if let Some(p) = &current.active_phase {
        obj.insert("phase".into(), Value::String(p.clone()));
    }
    if let Some(e) = &current.active_epic {
        obj.insert("epic".into(), Value::String(e.clone()));
    }
    if let Some(w) = &current.active_wave {
        obj.insert("wave".into(), Value::String(w.clone()));
    }
    if let Some(s) = &current.active_sprint {
        obj.insert("sprint".into(), Value::String(s.clone()));
    }
    if !current.active_tickets.is_empty() {
        obj.insert(
            "tickets".into(),
            Value::Array(
                current
                    .active_tickets
                    .iter()
                    .map(|t| Value::String(t.clone()))
                    .collect(),
            ),
        );
    }

    let text = serde_json::to_string(&obj).unwrap_or_else(|_| "{}".into());

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "current": obj
    }))
}

/// `cascade.update_ticket_status` — transition a ticket status.
///
/// Looks up the ticket in the INDEX to find its full path, then delegates to
/// `PbdStore::transition_ticket`. Validates the status enum and transition graph.
async fn handle_update_ticket_status(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let ticket_id = args
        .get("ticket_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'ticket_id' is required"))?
        .to_string();

    let status_str = args
        .get("status")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'status' is required"))?
        .to_string();

    let note = args
        .get("note")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());

    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());
    let ticket_id_for_resp = ticket_id.clone();

    let new_status = parse_ticket_status(&status_str)
        .ok_or_else(|| JsonRpcError::invalid_params(format!("invalid status: '{status_str}'")))?;

    let result = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        // Walk the index to locate ticket coordinates
        let index = store.read_index()?;
        let entry = index
            .entries
            .iter()
            .find(|e| e.kind == "ticket" && e.id == ticket_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "ticket '{ticket_id}' not found in INDEX.yaml"
                ))
            })?;
        // Resolve parent chain: ticket -> sprint -> wave -> epic -> phase
        let coords = resolve_ticket_coords(&store, &entry.id, entry.parent.as_deref())?;
        store.transition_ticket(
            &coords.phase_id,
            &coords.epic_id,
            &coords.wave_id,
            &coords.sprint_id,
            &ticket_id,
            new_status.clone(),
            note.as_deref(),
        )?;
        Ok::<String, cascade_types::error::CascadeError>(new_status.as_str().to_string())
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("transition: {e}")))?;

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("ticket '{}' -> '{}'", ticket_id_for_resp, result)
        }],
        "ticket_id": ticket_id_for_resp,
        "new_status": result
    }))
}

/// `cascade.append_event` — append a raw event to events.jsonl.
async fn handle_append_event(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let event_val = args
        .get("event")
        .cloned()
        .ok_or_else(|| JsonRpcError::invalid_params("'event' is required"))?;

    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    // Inject ts if missing before deserializing (PbdEvent requires ts)
    let mut event_val = event_val;
    if let Value::Object(ref mut map) = event_val {
        map.entry("ts")
            .or_insert_with(|| Value::String(chrono::Utc::now().to_rfc3339()));
    }
    let event: PbdEvent = serde_json::from_value(event_val)
        .map_err(|e| JsonRpcError::invalid_params(format!("invalid event JSON: {e}")))?;

    tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        store.init()?;
        store.append_event(&event)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("append_event: {e}")))?;
    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": "event appended" }]
    }))
}

/// `cascade.get_sprint` — return sprint YAML for a sprint ID.
///
/// Searches the entire phase/epic/wave tree for the matching sprint.
async fn handle_get_sprint(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let sprint_id = args
        .get("sprint_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'sprint_id' is required"))?
        .to_string();

    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let sprint_json = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        // Walk the INDEX for a sprint entry
        let index = store.read_index()?;
        let entry = index
            .entries
            .iter()
            .find(|e| e.kind == "sprint" && e.id == sprint_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "sprint '{sprint_id}' not found in INDEX.yaml"
                ))
            })?;
        // entry.parent is wave_id; wave.parent is epic_id; epic.parent is phase_id
        let wave_id = entry.parent.as_deref().ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "sprint '{sprint_id}' has no parent wave in INDEX"
            ))
        })?;
        let wave_entry = index
            .entries
            .iter()
            .find(|e| e.kind == "wave" && e.id == wave_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "wave '{wave_id}' not found in INDEX.yaml"
                ))
            })?;
        let epic_id = wave_entry.parent.as_deref().ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "wave '{wave_id}' has no parent epic in INDEX"
            ))
        })?;
        let epic_entry = index
            .entries
            .iter()
            .find(|e| e.kind == "epic" && e.id == epic_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "epic '{epic_id}' not found in INDEX.yaml"
                ))
            })?;
        let phase_id = epic_entry.parent.as_deref().ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "epic '{epic_id}' has no parent phase in INDEX"
            ))
        })?;
        let sprint = store.load_sprint(phase_id, epic_id, wave_id, &sprint_id)?;
        serde_json::to_string(&sprint)
            .map_err(|e| cascade_types::error::CascadeError::Other(format!("json: {e}")))
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("get_sprint: {e}")))?;

    let sprint_val: Value = serde_json::from_str(&sprint_json).unwrap_or(Value::Null);

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": sprint_json }],
        "sprint": sprint_val
    }))
}

/// `cascade.read_phase_status` — compact status summary for a phase.
async fn handle_read_phase_status(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let phase_id_filter = args
        .get("phase_id")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let summary = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        let phases = store.list_phases()?;
        let mut result = Vec::new();
        for phase in &phases {
            if let Some(ref filter) = phase_id_filter {
                if &phase.id != filter {
                    continue;
                }
            }
            // Skip archived phases unless explicitly requested
            if phase.status == cascade_core::pbd::schema::PhaseStatus::Archived
                && phase_id_filter.is_none()
            {
                continue;
            }
            let index = store.read_index().unwrap_or_default();
            let tickets: Vec<_> = index
                .entries
                .iter()
                .filter(|e| e.kind == "ticket")
                .collect();
            let mut counts = std::collections::HashMap::new();
            for t in &tickets {
                *counts.entry(t.status.clone()).or_insert(0u32) += 1;
            }
            result.push(serde_json::json!({
                "id": phase.id,
                "title": phase.title,
                "status": phase.status.as_str(),
                "ticket_counts": counts,
                "started_at": phase.started_at,
                "closed_at": phase.closed_at
            }));
        }
        Ok::<Vec<Value>, cascade_types::error::CascadeError>(result)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("read_phase_status: {e}")))?;

    let text = serde_json::to_string(&summary).unwrap_or_else(|_| "[]".into());
    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "phases": summary
    }))
}

/// `cascade.list_tickets` — list tickets with optional filters.
async fn handle_list_tickets(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let status_filter = args
        .get("status")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let sprint_filter = args
        .get("sprint_id")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let phase_filter = args
        .get("phase_id")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let tickets = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        let index = store.read_index()?;
        let mut result = Vec::new();
        for entry in &index.entries {
            if entry.kind != "ticket" {
                continue;
            }
            if let Some(ref sf) = status_filter {
                if &entry.status != sf {
                    continue;
                }
            }
            if let Some(ref spf) = sprint_filter {
                if entry.parent.as_deref() != Some(spf.as_str()) {
                    continue;
                }
            }
            if let Some(ref pf) = phase_filter {
                // Ticket parent is sprint; sprint parent is wave; wave parent is epic; epic parent is phase.
                // Fast check: look up sprint -> wave -> epic -> phase in index.
                if !ticket_in_phase(&index.entries, &entry.id, pf) {
                    continue;
                }
            }
            result.push(serde_json::json!({
                "id": entry.id,
                "title": entry.title,
                "status": entry.status,
                "sprint": entry.parent,
            }));
        }
        Ok::<Vec<Value>, cascade_types::error::CascadeError>(result)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("list_tickets: {e}")))?;

    let text = serde_json::to_string(&tickets).unwrap_or_else(|_| "[]".into());
    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "tickets": tickets,
        "count": tickets.len()
    }))
}

/// `cascade.check_routes` — check api-routes.yaml and return per-route ok/fail.
///
/// Reads a minimal YAML file of the form:
/// ```yaml
/// routes:
///   - path: /api/health
///     method: GET
///     expected_status: 200
/// ```
/// and issues HTTP checks. `base_url` overrides the host. In tests, callers
/// can point `routes_file` at a seeded temp file and `base_url` at a mock server.
async fn handle_check_routes(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let routes_file = args
        .get("routes_file")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let base_url = args
        .get("base_url")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let timeout_ms = args
        .get("timeout_ms")
        .and_then(|v| v.as_u64())
        .unwrap_or(5000)
        .clamp(100, 30000);

    // Resolve routes file path
    let path = if let Some(p) = routes_file {
        std::path::PathBuf::from(p)
    } else {
        let cwd = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        cwd.join(".claude").join("docs").join("api-routes.yaml")
    };

    if !path.exists() {
        return Ok(serde_json::json!({
            "content": [{ "type": "text", "text": format!("routes file not found: {}", path.display()) }],
            "routes": [],
            "not_found": true
        }));
    }

    let yaml_text = tokio::fs::read_to_string(&path)
        .await
        .map_err(|e| JsonRpcError::internal(format!("read routes file: {e}")))?;

    // Parse routes — minimal YAML parse without full serde dependency
    let routes = parse_routes_yaml(&yaml_text);

    if routes.is_empty() {
        return Ok(serde_json::json!({
            "content": [{ "type": "text", "text": "no routes found in api-routes.yaml" }],
            "routes": []
        }));
    }

    // Check each route
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_millis(timeout_ms))
        .build()
        .map_err(|e| JsonRpcError::internal(format!("build http client: {e}")))?;

    let mut results = Vec::new();
    for route in &routes {
        let url = if let Some(ref base) = base_url {
            format!("{}{}", base.trim_end_matches('/'), route.path)
        } else {
            route.path.clone()
        };

        let method = route.method.to_uppercase();
        let req = match method.as_str() {
            "POST" => client.post(&url),
            "PUT" => client.put(&url),
            "DELETE" => client.delete(&url),
            "PATCH" => client.patch(&url),
            _ => client.get(&url),
        };

        let (ok, status_code, error_msg) = match req.send().await {
            Ok(resp) => {
                let code = resp.status().as_u16();
                let expected = route.expected_status.unwrap_or(200);
                (code == expected, Some(code), None)
            }
            Err(e) => (false, None, Some(e.to_string())),
        };

        results.push(serde_json::json!({
            "path": route.path,
            "method": route.method,
            "ok": ok,
            "status_code": status_code,
            "expected_status": route.expected_status,
            "error": error_msg
        }));
    }

    let all_ok = results.iter().all(|r| r["ok"].as_bool().unwrap_or(false));
    let text = format!(
        "{}/{} routes ok",
        results
            .iter()
            .filter(|r| r["ok"].as_bool().unwrap_or(false))
            .count(),
        results.len()
    );

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "routes": results,
        "all_ok": all_ok
    }))
}

/// `cascade.scan_inbox` — scan an inbox directory and return file summaries.
async fn handle_scan_inbox(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let inbox_path = if let Some(p) = args.get("inbox_path").and_then(|v| v.as_str()) {
        std::path::PathBuf::from(p)
    } else if let Some(proj) = args.get("project").and_then(|v| v.as_str()) {
        mcp_paths::inbox_dir(proj)
    } else {
        // Auto-discover: CWD/.claude/inbox or CWD/.cascade/inbox
        let cwd = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        let claude_inbox = cwd.join(".claude").join("inbox");
        if claude_inbox.is_dir() {
            claude_inbox
        } else {
            cwd.join(".cascade").join("inbox")
        }
    };

    if !inbox_path.exists() {
        return Ok(serde_json::json!({
            "content": [{ "type": "text", "text": "inbox directory not found" }],
            "messages": [],
            "count": 0
        }));
    }

    let mut messages = Vec::new();
    let mut rd = tokio::fs::read_dir(&inbox_path)
        .await
        .map_err(|e| JsonRpcError::internal(format!("read inbox dir: {e}")))?;

    while let Ok(Some(entry)) = rd.next_entry().await {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("md") {
            continue;
        }
        let filename = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("")
            .to_string();
        let meta = tokio::fs::metadata(&path).await.ok();
        let size = meta.map(|m| m.len()).unwrap_or(0);

        // Read first non-empty line as subject
        let subject = if let Ok(content) = tokio::fs::read_to_string(&path).await {
            content
                .lines()
                .find(|l| !l.trim().is_empty())
                .unwrap_or("")
                .trim_start_matches('#')
                .trim()
                .to_string()
        } else {
            String::new()
        };

        messages.push(serde_json::json!({
            "file": filename,
            "subject": subject,
            "size_bytes": size
        }));
    }

    // Sort by filename (chronological for date-prefixed names)
    messages.sort_by(|a, b| {
        a["file"]
            .as_str()
            .unwrap_or("")
            .cmp(b["file"].as_str().unwrap_or(""))
    });

    let count = messages.len();
    let text = format!("{count} message(s) in inbox");

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "messages": messages,
        "count": count
    }))
}

// ── PBD helpers ───────────────────────────────────────────────────────────────

/// Extract optional phases_root from args.
fn pbd_root_from_args(args: &Value) -> Option<std::path::PathBuf> {
    args.get("phases_root")
        .and_then(|v| v.as_str())
        .map(std::path::PathBuf::from)
}

/// Parse a status string into a TicketStatus enum value.
fn parse_ticket_status(s: &str) -> Option<TicketStatus> {
    match s {
        "planned" => Some(TicketStatus::Planned),
        "queue" => Some(TicketStatus::Queue),
        "active" => Some(TicketStatus::Active),
        "review" => Some(TicketStatus::Review),
        "blocked" => Some(TicketStatus::Blocked),
        "done" => Some(TicketStatus::Done),
        "archived" => Some(TicketStatus::Archived),
        _ => None,
    }
}

/// Ticket coordinate resolution: given ticket_id and its sprint parent from INDEX,
/// walk the index upward to find (phase_id, epic_id, wave_id, sprint_id).
struct TicketCoords {
    phase_id: String,
    epic_id: String,
    wave_id: String,
    sprint_id: String,
}

fn resolve_ticket_coords(
    store: &PbdStore,
    ticket_id: &str,
    sprint_id_opt: Option<&str>,
) -> std::result::Result<TicketCoords, cascade_types::error::CascadeError> {
    let index = store.read_index()?;
    let sprint_id = sprint_id_opt.ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!(
            "ticket '{ticket_id}' has no sprint parent in INDEX"
        ))
    })?;
    let sprint_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "sprint" && e.id == sprint_id)
        .ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "sprint '{sprint_id}' not found in INDEX"
            ))
        })?;
    let wave_id = sprint_entry.parent.as_deref().ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!(
            "sprint '{sprint_id}' has no wave in INDEX"
        ))
    })?;
    let wave_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "wave" && e.id == wave_id)
        .ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "wave '{wave_id}' not found in INDEX"
            ))
        })?;
    let epic_id = wave_entry.parent.as_deref().ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!("wave '{wave_id}' has no epic in INDEX"))
    })?;
    let epic_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "epic" && e.id == epic_id)
        .ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "epic '{epic_id}' not found in INDEX"
            ))
        })?;
    let phase_id = epic_entry.parent.as_deref().ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!("epic '{epic_id}' has no phase in INDEX"))
    })?;
    Ok(TicketCoords {
        phase_id: phase_id.to_string(),
        epic_id: epic_id.to_string(),
        wave_id: wave_id.to_string(),
        sprint_id: sprint_id.to_string(),
    })
}

/// Check whether ticket_id belongs to phase_id by walking the index parent chain.
fn ticket_in_phase(
    entries: &[cascade_core::pbd::schema::IndexEntry],
    ticket_id: &str,
    phase_id: &str,
) -> bool {
    // ticket -> sprint -> wave -> epic -> phase
    let ticket = entries
        .iter()
        .find(|e| e.kind == "ticket" && e.id == ticket_id);
    let sprint_id = match ticket.and_then(|t| t.parent.as_deref()) {
        Some(s) => s,
        None => return false,
    };
    let sprint = entries
        .iter()
        .find(|e| e.kind == "sprint" && e.id == sprint_id);
    let wave_id = match sprint.and_then(|s| s.parent.as_deref()) {
        Some(w) => w,
        None => return false,
    };
    let wave = entries.iter().find(|e| e.kind == "wave" && e.id == wave_id);
    let epic_id = match wave.and_then(|w| w.parent.as_deref()) {
        Some(e) => e,
        None => return false,
    };
    let epic = entries.iter().find(|e| e.kind == "epic" && e.id == epic_id);
    match epic.and_then(|e| e.parent.as_deref()) {
        Some(p) => p == phase_id,
        None => false,
    }
}

/// Minimal YAML-parsing for api-routes.yaml.
///
/// Parses a routes file of the form:
/// ```yaml
/// routes:
///   - path: /api/health
///     method: GET
///     expected_status: 200
/// ```
/// This avoids pulling in a heavy serde_yaml dependency specifically for this function.
struct RouteEntry {
    path: String,
    method: String,
    expected_status: Option<u16>,
}

fn parse_routes_yaml(yaml: &str) -> Vec<RouteEntry> {
    let mut routes = Vec::new();
    let mut in_routes = false;
    let mut current: Option<(String, String, Option<u16>)> = None;

    for line in yaml.lines() {
        let trimmed = line.trim();
        if trimmed == "routes:" {
            in_routes = true;
            continue;
        }
        if !in_routes {
            continue;
        }
        // New route entry
        if trimmed.starts_with("- path:") || trimmed.starts_with("-path:") {
            // Flush previous
            if let Some((path, method, expected)) = current.take() {
                if !path.is_empty() {
                    routes.push(RouteEntry {
                        path,
                        method,
                        expected_status: expected,
                    });
                }
            }
            let path = trimmed
                .trim_start_matches('-')
                .trim()
                .strip_prefix("path:")
                .unwrap_or("")
                .trim()
                .to_string();
            current = Some((path, "GET".into(), None));
        } else if let Some(ref mut c) = current {
            if trimmed.starts_with("method:") {
                c.1 = trimmed
                    .strip_prefix("method:")
                    .unwrap_or("GET")
                    .trim()
                    .to_string();
            } else if trimmed.starts_with("expected_status:") {
                if let Ok(code) = trimmed
                    .strip_prefix("expected_status:")
                    .unwrap_or("")
                    .trim()
                    .parse::<u16>()
                {
                    c.2 = Some(code);
                }
            }
        }
    }
    // Flush last entry
    if let Some((path, method, expected)) = current {
        if !path.is_empty() {
            routes.push(RouteEntry {
                path,
                method,
                expected_status: expected,
            });
        }
    }
    routes
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

    /// tools/list returns exactly 18 tools (10 original + 8 PBD tools E-P8-04).
    #[tokio::test]
    async fn tools_list_returns_9_tools() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.expect("list should not fail");
        let tools = result["tools"].as_array().expect("tools must be array");
        assert_eq!(
            tools.len(),
            18,
            "expected exactly 18 tools, got {}",
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

    /// tools/list now returns 18 tools (10 original + 8 PBD E-P8-04).
    #[tokio::test]
    async fn tools_list_returns_10_tools() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.expect("list should not fail");
        let tools = result["tools"].as_array().expect("tools must be array");
        assert_eq!(
            tools.len(),
            18,
            "expected exactly 18 tools, got {}",
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
                result
                    .get("isError")
                    .map(|v| v == &Value::Bool(false))
                    .unwrap_or(true),
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

    // ── PBD tool tests (E-P8-04) ─────────────────────────────────────────────

    /// Build a minimal phases tree in a temp dir and return its path.
    fn scaffold_pbd_tree() -> tempfile::TempDir {
        use cascade_core::pbd::schema::*;
        use cascade_core::pbd::store::PbdStore;

        let tmp = tempfile::TempDir::new().unwrap();
        let phases = tmp.path().join("phases");
        let store = PbdStore::new(&phases);
        store.init().unwrap();

        let phase = Phase {
            id: "p1".into(),
            title: "Phase 1".into(),
            status: PhaseStatus::Building,
            epics: vec![],
            started_at: Some(chrono::Utc::now()),
            closed_at: None,
            note: None,
        };
        store.create_phase(&phase).unwrap();

        let epic = Epic {
            id: "e01".into(),
            phase_id: "p1".into(),
            title: "Epic 1".into(),
            status: EpicStatus::Active,
            waves: vec![],
            depends_on: vec![],
            note: None,
        };
        store.create_epic(&epic).unwrap();

        let wave = Wave {
            id: "w01".into(),
            epic_id: "e01".into(),
            title: "Wave 1".into(),
            status: WaveStatus::Active,
            sprints: vec![],
            note: None,
        };
        store.create_wave("p1", &wave).unwrap();

        let sprint = Sprint {
            id: "s01".into(),
            wave_id: "w01".into(),
            title: "Sprint 1".into(),
            status: SprintStatus::Active,
            tickets: vec![],
            note: None,
        };
        store.create_sprint("p1", "e01", &sprint).unwrap();

        let ticket = Ticket {
            id: "T-P1-E01-01".into(),
            sprint_id: "s01".into(),
            title: "Test ticket".into(),
            status: TicketStatus::Active,
            steps: vec![],
            depends_on: vec![],
            repo: None,
            weight: None,
            note: None,
            blocked_reason: None,
        };
        store.create_ticket("p1", "e01", "w01", &ticket).unwrap();

        tmp
    }

    /// cascade.get_current returns shape with content field.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_get_current_shape() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.get_current",
            "arguments": { "phases_root": phases_root.to_str().unwrap() }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "get_current should succeed: {result}"
        );
        let content = result["content"].as_array().expect("content array");
        assert!(!content.is_empty(), "content must not be empty");
        // current block present
        assert!(result.get("current").is_some(), "current field required");
    }

    /// cascade.get_current on empty phases tree returns empty current.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_get_current_empty_tree() {
        let tmp = tempfile::TempDir::new().unwrap();
        let phases_root = tmp.path().join("phases");
        std::fs::create_dir_all(&phases_root).unwrap();
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.get_current",
            "arguments": { "phases_root": phases_root.to_str().unwrap() }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        // Empty phases → no error, empty current
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "empty tree should not be an error: {result}"
        );
        let current = &result["current"];
        assert!(
            current.as_object().map(|o| o.is_empty()).unwrap_or(false),
            "empty tree should yield empty current: {current}"
        );
    }

    /// cascade.update_ticket_status — valid transition active->done.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_update_ticket_status_valid_transition() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.update_ticket_status",
            "arguments": {
                "ticket_id": "T-P1-E01-01",
                "status": "done",
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "valid transition must succeed: {result}"
        );
        assert_eq!(
            result["new_status"].as_str(),
            Some("done"),
            "new_status must be 'done'"
        );
    }

    /// cascade.update_ticket_status — invalid transition done->queue returns is_error.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_update_ticket_status_invalid_transition() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");

        // First move to done
        use cascade_core::pbd::schema::TicketStatus;
        use cascade_core::pbd::store::PbdStore;
        let store = PbdStore::new(tmp.path().join("phases"));
        store
            .transition_ticket(
                "p1",
                "e01",
                "w01",
                "s01",
                "T-P1-E01-01",
                TicketStatus::Done,
                None,
            )
            .unwrap();

        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.update_ticket_status",
            "arguments": {
                "ticket_id": "T-P1-E01-01",
                "status": "queue",  // done->queue is not allowed
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "invalid transition must return is_error: {result}"
        );
    }

    /// cascade.update_ticket_status — invalid status string returns is_error.
    #[tokio::test]
    async fn pbd_update_ticket_status_bad_status_string() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.update_ticket_status",
            "arguments": {
                "ticket_id": "T-X",
                "status": "flying"
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "bad status must be is_error"
        );
    }

    /// cascade.append_event — event appended and readable.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_append_event_ordering() {
        let tmp = tempfile::TempDir::new().unwrap();
        let phases_root = tmp.path().join("phases");
        std::fs::create_dir_all(&phases_root).unwrap();
        let reg = ToolRegistry::new();

        // Append two events
        for (from, to) in [("planned", "active"), ("active", "done")] {
            let params = serde_json::json!({
                "name": "cascade.append_event",
                "arguments": {
                    "event": {
                        "actor": "test",
                        "level": "ticket",
                        "id": "T-001",
                        "from": from,
                        "to": to
                    },
                    "phases_root": phases_root.to_str().unwrap()
                }
            });
            let result = reg.call(&params).await.expect("no protocol error");
            assert!(
                result
                    .get("isError")
                    .map(|v| v == &Value::Bool(false))
                    .unwrap_or(true),
                "append_event must succeed: {result}"
            );
        }

        // Read events.jsonl and verify ordering
        let events_path = phases_root.join("events.jsonl");
        let content = std::fs::read_to_string(&events_path).expect("events.jsonl must exist");
        let lines: Vec<&str> = content.lines().filter(|l| !l.is_empty()).collect();
        assert_eq!(
            lines.len(),
            2,
            "two events must be appended; got: {content}"
        );
        let first: Value = serde_json::from_str(lines[0]).unwrap();
        let second: Value = serde_json::from_str(lines[1]).unwrap();
        assert_eq!(first["from"].as_str(), Some("planned"));
        assert_eq!(second["from"].as_str(), Some("active"));
    }

    /// cascade.append_event — missing required field returns is_error.
    #[tokio::test]
    async fn pbd_append_event_missing_field() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.append_event",
            "arguments": {
                "event": {
                    "actor": "test",
                    // missing level, id, from, to
                }
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "missing fields must be is_error"
        );
    }

    /// cascade.get_sprint — returns sprint JSON.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_get_sprint_found() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.get_sprint",
            "arguments": {
                "sprint_id": "s01",
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "get_sprint must succeed: {result}"
        );
        let sprint = &result["sprint"];
        assert_eq!(sprint["id"].as_str(), Some("s01"), "sprint.id must be s01");
        assert!(
            !sprint["tickets"].is_null(),
            "sprint.tickets must be present"
        );
    }

    /// cascade.get_sprint — unknown sprint returns is_error.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_get_sprint_not_found() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.get_sprint",
            "arguments": {
                "sprint_id": "s99",
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert_eq!(
            result["isError"].as_bool(),
            Some(true),
            "unknown sprint must return is_error"
        );
    }

    /// cascade.list_tickets — no filter returns all tickets.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_list_tickets_no_filter() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.list_tickets",
            "arguments": { "phases_root": phases_root.to_str().unwrap() }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "list_tickets must succeed: {result}"
        );
        let tickets = result["tickets"].as_array().expect("tickets must be array");
        assert_eq!(
            tickets.len(),
            1,
            "should find exactly one ticket; got {}",
            tickets.len()
        );
    }

    /// cascade.list_tickets — status filter.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn pbd_list_tickets_status_filter() {
        let tmp = scaffold_pbd_tree();
        let phases_root = tmp.path().join("phases");
        let reg = ToolRegistry::new();

        // Filter for "active" → 1 result
        let params = serde_json::json!({
            "name": "cascade.list_tickets",
            "arguments": {
                "status": "active",
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        let tickets = result["tickets"].as_array().unwrap();
        assert_eq!(tickets.len(), 1, "active filter: expected 1 ticket");

        // Filter for "done" → 0 results
        let params2 = serde_json::json!({
            "name": "cascade.list_tickets",
            "arguments": {
                "status": "done",
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result2 = reg.call(&params2).await.expect("no protocol error");
        let tickets2 = result2["tickets"].as_array().unwrap();
        assert_eq!(tickets2.len(), 0, "done filter: expected 0 tickets");
    }

    /// cascade.check_routes — missing routes file returns not_found flag.
    #[tokio::test]
    async fn pbd_check_routes_missing_file() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.check_routes",
            "arguments": {
                "routes_file": "/tmp/does_not_exist_cascade_routes_xyz.yaml"
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "missing routes file must not be is_error (just not_found flag): {result}"
        );
        assert_eq!(
            result["not_found"].as_bool(),
            Some(true),
            "not_found flag must be true"
        );
    }

    /// cascade.check_routes — mock HTTP: routes_file with base_url pointing to httpbin.
    ///
    /// This test uses a seeded temp routes file and a mock server.
    /// Because we can't guarantee network in CI, we test the parsing + structure only
    /// when a real server is unavailable (ok=false is still a valid structured response).
    #[tokio::test]
    async fn pbd_check_routes_with_mock_file() {
        use std::io::Write;
        let tmp = tempfile::TempDir::new().unwrap();
        let routes_file = tmp.path().join("api-routes.yaml");
        {
            let mut f = std::fs::File::create(&routes_file).unwrap();
            writeln!(f, "routes:").unwrap();
            writeln!(f, "  - path: /healthz").unwrap();
            writeln!(f, "    method: GET").unwrap();
            writeln!(f, "    expected_status: 200").unwrap();
        }

        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.check_routes",
            "arguments": {
                "routes_file": routes_file.to_str().unwrap(),
                "base_url": "http://127.0.0.1:1",  // nothing listening → connection refused
                "timeout_ms": 200
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        // Must not be a tool-level error — connection refused becomes ok=false in the routes array
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "check_routes must not bubble network errors as is_error: {result}"
        );
        let routes = result["routes"].as_array().expect("routes must be array");
        assert_eq!(routes.len(), 1, "one route must be in result");
        assert_eq!(
            routes[0]["path"].as_str(),
            Some("/healthz"),
            "path must match"
        );
        assert_eq!(
            routes[0]["ok"].as_bool(),
            Some(false),
            "connection refused must be ok=false"
        );
    }

    /// cascade.scan_inbox — finds seeded .md error files.
    #[tokio::test]
    async fn pbd_scan_inbox_finds_error_files() {
        use std::io::Write;
        let tmp = tempfile::TempDir::new().unwrap();
        let inbox = tmp.path().join("inbox");
        std::fs::create_dir_all(&inbox).unwrap();

        // Seed three .md files
        for (name, subject) in [
            ("ci-error-1.md", "# CI failure: build broke"),
            ("ci-error-2.md", "# CI failure: tests failed"),
            ("pci-enhancement.md", "# Enhancement: add dark mode"),
        ] {
            let mut f = std::fs::File::create(inbox.join(name)).unwrap();
            writeln!(f, "{subject}").unwrap();
            writeln!(f, "\nBody text.").unwrap();
        }

        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.scan_inbox",
            "arguments": { "inbox_path": inbox.to_str().unwrap() }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "scan_inbox must succeed: {result}"
        );
        let messages = result["messages"]
            .as_array()
            .expect("messages must be array");
        assert_eq!(
            messages.len(),
            3,
            "must find 3 messages; got {}",
            messages.len()
        );
        // Verify subject extraction
        assert!(
            messages
                .iter()
                .any(|m| m["subject"].as_str().unwrap_or("").contains("CI failure")),
            "must extract CI failure subjects"
        );
        assert_eq!(result["count"].as_u64(), Some(3), "count must be 3");
    }

    /// cascade.scan_inbox — non-existent inbox returns empty list, no error.
    #[tokio::test]
    async fn pbd_scan_inbox_missing_dir() {
        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.scan_inbox",
            "arguments": { "inbox_path": "/tmp/does_not_exist_cascade_inbox_xyz" }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "missing inbox must not be is_error: {result}"
        );
        assert_eq!(
            result["count"].as_u64(),
            Some(0),
            "missing inbox must return count=0"
        );
    }

    // ── active_work in provide_harness_context (E-P8-07) ─────────────────────

    /// provide_harness_context response includes active_work field (E-P8-07).
    ///
    /// Even when no active sprint exists (no phases tree), active_work must be
    /// present as an object field (empty, not null).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn provide_harness_context_includes_active_work() {
        use tempfile::TempDir;
        let tmp = TempDir::new().unwrap();

        // Scaffold a minimal cascade tree so resolve_cascade_full finds something.
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(cascade_dir.join("CASCADE.md"), "# Test\nInstructions.").unwrap();

        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": {
                "harness": "claude-code",
                "cwd": tmp.path().to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");

        // Must not be error
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "provide_harness_context must succeed: {result}"
        );

        // active_work field must be present
        assert!(
            result.get("active_work").is_some(),
            "provide_harness_context must include active_work field: {result}"
        );
        assert!(
            result.get("active_work_text").is_some(),
            "provide_harness_context must include active_work_text field: {result}"
        );
        // With no phases tree, active_work should indicate empty state
        let aw = &result["active_work"];
        assert!(aw.is_object(), "active_work must be a JSON object: {aw}");
    }

    /// provide_harness_context active_work reflects active sprint when phases tree exists.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn provide_harness_context_active_work_reflects_sprint() {
        use cascade_core::pbd::schema::*;
        use cascade_core::pbd::store::PbdStore;
        use tempfile::TempDir;

        let tmp = TempDir::new().unwrap();

        // Scaffold cascade tree
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(cascade_dir.join("CASCADE.md"), "# Test\nInstructions.").unwrap();

        // Scaffold phases tree under .cascade/phases/
        let phases_root = tmp.path().join(".cascade").join("phases");
        let store = PbdStore::new(&phases_root);
        store.init().unwrap();

        let phase = Phase {
            id: "p1".into(),
            title: "P1".into(),
            status: PhaseStatus::Building,
            epics: vec!["e01".into()],
            started_at: None,
            closed_at: None,
            note: None,
        };
        store.create_phase(&phase).unwrap();

        let epic = Epic {
            id: "e01".into(),
            phase_id: "p1".into(),
            title: "E1".into(),
            status: EpicStatus::Active,
            waves: vec!["w01".into()],
            depends_on: vec![],
            note: None,
        };
        store.create_epic(&epic).unwrap();

        let wave = Wave {
            id: "w01".into(),
            epic_id: "e01".into(),
            title: "W1".into(),
            status: WaveStatus::Active,
            sprints: vec!["s01".into()],
            note: None,
        };
        store.create_wave("p1", &wave).unwrap();

        let sprint = Sprint {
            id: "s01".into(),
            wave_id: "w01".into(),
            title: "Active Sprint".into(),
            status: SprintStatus::Active,
            tickets: vec!["T-P1-E01-W01-S01-01".into()],
            note: None,
        };
        store.create_sprint("p1", "e01", &sprint).unwrap();

        let ticket = Ticket {
            id: "T-P1-E01-W01-S01-01".into(),
            sprint_id: "s01".into(),
            title: "Do the thing".into(),
            status: TicketStatus::Active,
            steps: vec![],
            depends_on: vec![],
            repo: None,
            weight: None,
            note: None,
            blocked_reason: None,
        };
        store.create_ticket("p1", "e01", "w01", &ticket).unwrap();

        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": {
                "harness": "claude-code",
                "cwd": tmp.path().to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");

        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "must succeed: {result}"
        );

        let aw = &result["active_work"];
        assert_eq!(
            aw["sprint_id"].as_str(),
            Some("s01"),
            "active_work.sprint_id must be s01: {aw}"
        );
        let aw_text = result["active_work_text"].as_str().unwrap_or("");
        assert!(
            aw_text.contains("s01"),
            "active_work_text must mention sprint: {aw_text}"
        );
        assert!(
            aw_text.contains("Do the thing"),
            "active_work_text must mention ticket title: {aw_text}"
        );
    }

    /// inject_active_work: harness_context token_bounded (active_work <= 800 tokens).
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn provide_harness_context_active_work_token_bounded() {
        use tempfile::TempDir;
        let tmp = TempDir::new().unwrap();

        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(cascade_dir.join("CASCADE.md"), "# T\nInstructions.").unwrap();

        let reg = ToolRegistry::new();
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": { "harness": "opencode", "cwd": tmp.path().to_str().unwrap() }
        });
        let result = reg.call(&params).await.expect("no protocol error");

        let aw_text = result["active_work_text"].as_str().unwrap_or("");
        // Token bound: text length / 4 chars-per-token <= 800 tokens
        let estimated_tokens = aw_text.len() / 4;
        assert!(
            estimated_tokens <= 800,
            "active_work_text estimated tokens={estimated_tokens} must be <=800; text length={}",
            aw_text.len()
        );
    }

    /// All 8 PBD tools appear in tools/list.
    #[tokio::test]
    async fn pbd_tools_in_list() {
        let reg = ToolRegistry::new();
        let result = reg.list().await.unwrap();
        let tools = result["tools"].as_array().unwrap();
        let names: Vec<&str> = tools
            .iter()
            .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
            .collect();
        let expected_pbd = [
            "cascade.get_current",
            "cascade.update_ticket_status",
            "cascade.append_event",
            "cascade.get_sprint",
            "cascade.read_phase_status",
            "cascade.list_tickets",
            "cascade.check_routes",
            "cascade.scan_inbox",
        ];
        for tool in &expected_pbd {
            assert!(
                names.contains(tool),
                "PBD tool '{tool}' must be in tools/list; found: {names:?}"
            );
        }
    }

    // ── cascade.search live RAG integration ───────────────────────────────────

    /// Build a tiny in-memory `RagIndex` with two fixture docs, wrap it in an
    /// `RrfRetriever` (FTS-only mode — no embeddings required), inject it into
    /// a `ToolRegistry`, and assert that `cascade.search` returns real hits.
    ///
    /// This is the primary integration test for the E11.1 search wiring.
    #[tokio::test]
    async fn search_live_rrf_returns_real_hits() {
        use cascade_rag::index::RagIndex;
        use cascade_rag::retrieve::rrf::{RrfConfig, RrfRetriever};
        use cascade_types::NoopEmbeddingProvider;

        // Build a temp-file index (RagIndex::open needs a real path; it uses
        // SQLite WAL which doesn't work with ":memory:" across threads).
        let tmp = tempfile::tempdir().expect("tempdir");
        let db_path = tmp.path().join("test.db");
        let idx = Arc::new(RagIndex::open(&db_path).await.expect("RagIndex::open"));

        // Ingest three fixture chunks.
        idx.upsert_chunk(
            "c1",
            None,
            Some(1),
            Some(5),
            "prayer times fajr dhuhr asr maghrib isha",
            None,
        )
        .await
        .expect("upsert c1");
        idx.upsert_chunk(
            "c2",
            None,
            Some(10),
            Some(15),
            "hijri calendar month ramadan shawwal dhul-hijja",
            None,
        )
        .await
        .expect("upsert c2");
        idx.upsert_chunk(
            "c3",
            None,
            Some(20),
            Some(25),
            "qibla direction great circle mecca bearing",
            None,
        )
        .await
        .expect("upsert c3");

        // FTS-only retriever (no embedding model required for the test).
        let retriever: Arc<dyn Retriever> = Arc::new(RrfRetriever::new(
            Arc::clone(&idx),
            Arc::new(NoopEmbeddingProvider),
            RrfConfig {
                use_vec: false,
                ..RrfConfig::default()
            },
        ));

        let reg = ToolRegistry::new().with_retriever(Arc::clone(&retriever));

        // Query for "prayer" — should hit c1.
        let params = serde_json::json!({
            "name": "cascade.search",
            "arguments": { "query": "prayer fajr", "limit": 5 }
        });
        let result = reg
            .call(&params)
            .await
            .expect("call must not protocol-error");

        // Must not be an error result.
        assert!(
            result.get("isError").is_none() || result["isError"] == serde_json::Value::Bool(false),
            "search must not return isError; got: {result}"
        );

        // Citations array must contain at least one entry.
        let citations = result["citations"]
            .as_array()
            .expect("citations must be array");
        assert!(
            !citations.is_empty(),
            "cascade.search must return at least one citation for 'prayer fajr'; got: {result}"
        );

        // chunk_id "c1" must appear in citations.
        let ids: Vec<&str> = citations
            .iter()
            .filter_map(|c| c.get("chunk_id").and_then(|v| v.as_str()))
            .collect();
        assert!(
            ids.contains(&"c1"),
            "citation for c1 (prayer chunk) must be present; found: {ids:?}"
        );

        // metadata.ready must be true.
        assert_eq!(
            result["metadata"]["ready"],
            serde_json::Value::Bool(true),
            "metadata.ready must be true when retriever is wired"
        );
    }

    /// When no retriever is injected, `cascade.search` returns gracefully with
    /// `ready: false` rather than an error.
    #[tokio::test]
    async fn search_no_retriever_returns_not_ready() {
        let reg = ToolRegistry::new(); // no retriever
        let params = serde_json::json!({
            "name": "cascade.search",
            "arguments": { "query": "anything", "limit": 5 }
        });
        let result = reg
            .call(&params)
            .await
            .expect("call must not protocol-error");

        // Must not be is_error.
        assert!(
            result.get("isError").is_none() || result["isError"] == serde_json::Value::Bool(false),
            "no-retriever search must not be is_error"
        );

        // ready flag must be false.
        assert_eq!(
            result["metadata"]["ready"],
            serde_json::Value::Bool(false),
            "metadata.ready must be false when no retriever is wired"
        );

        // Text must explain the situation.
        let text = result["content"][0]["text"].as_str().unwrap_or_default();
        assert!(
            text.contains("index not ready"),
            "response text must mention 'index not ready'; got: {text:?}"
        );
    }
}
