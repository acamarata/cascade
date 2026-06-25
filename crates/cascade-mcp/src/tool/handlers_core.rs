//! Core tool handlers: read, search, inbox, master_lists, memory, context_slice,
//! and the provide_harness_context bootstrap.

use std::sync::Arc;

use serde_json::Value;
use tracing::debug;

use cascade_core::cascade_resolution::resolve_cascade_full;
use cascade_core::pbd::active_work::build_active_work;
use cascade_rag::context::ContextOptimizer;
use cascade_types::retriever::Retriever;

use crate::paths as mcp_paths;
use crate::server::JsonRpcError;

use super::helpers::build_retrieve_opts;
use super::helpers::chrono_local_date;

// ── Tool handlers ─────────────────────────────────────────────────────────────

/// `cascade.read` — read a tier instruction file.
pub(super) async fn handle_read(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_search(
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
pub(super) async fn handle_search_codebase(
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
pub(super) async fn handle_inbox_list(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_inbox_send(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_master_lists(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_memory_read(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_memory_write(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_context_slice(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_provide_harness_context(
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
