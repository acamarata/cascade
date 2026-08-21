//! Core tool handlers: read, search, inbox, master_lists, memory, context_slice,
//! and the provide_harness_context bootstrap.

use std::sync::Arc;

use serde_json::Value;
use tracing::{debug, warn};

use cascade_core::cascade_resolution::resolve_cascade_full;
use cascade_core::pbd::active_work::build_active_work;
use cascade_rag::context::ContextOptimizer;
use cascade_types::retriever::Retriever;
use cascade_types::RetrievalHit;

use crate::paths as mcp_paths;
use crate::server::JsonRpcError;

use super::context_assembler::{ContextAssembler, ContextRequest};
use super::helpers::build_retrieve_opts;

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
/// # Retriever selection
///
/// Prefers `code_retriever` — a retriever backed by a TREE-SITTER-CHUNKED
/// index, whose chunks are whole functions, impl blocks and classes rather
/// than fixed-size windows — and falls back to the general-purpose retriever
/// when no code index has been injected (T-P7-E15-02). The response reports
/// which one answered in `metadata.index`, so a caller can tell a genuine
/// code-index hit from a generic fallback instead of guessing.
///
/// A `lang` filter is still passed through only as a result annotation:
/// `RetrieveOpts` has no language field, so constraining the retrieval itself
/// remains future work rather than something this handler silently pretends
/// to do.
pub(super) async fn handle_search_codebase(
    args: &Value,
    code_retriever: Option<Arc<dyn Retriever>>,
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

    // Dedicated code index first; the general retriever is the fallback.
    let used_code_index = code_retriever.is_some();
    let ret = match code_retriever.or(retriever) {
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

    // Name the index that answered. A caller cannot otherwise distinguish a
    // real tree-sitter code hit from a generic-index fallback.
    let index = if used_code_index { "code" } else { "general" };
    let text = if hits.is_empty() {
        format!("cascade.search_codebase: no results for {query:?} (lang={lang:?}, index={index})")
    } else if used_code_index {
        format!(
            "cascade.search_codebase: {} result(s) for {query:?} (lang={lang:?}, \
             index=code, tree-sitter chunked)",
            hits.len()
        )
    } else {
        format!(
            "cascade.search_codebase: {} result(s) for {query:?} (lang={lang:?}, \
             index=general — no dedicated code index injected)",
            hits.len()
        )
    };

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "results": results,
        "metadata": { "ready": true, "lang": lang, "index": index }
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
///
/// Validation and file-writing logic are shared via
/// `cascade_core::inbox::send_message` so the Tauri IPC command
/// `cascade_inbox_send` uses identical logic.
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

    let result =
        cascade_core::inbox::send_message("cascade-mcp", target, subject, body, priority, msg_type)
            .await
            .map_err(|e| JsonRpcError::internal(format!("Failed to send inbox message: {e}")))?;

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": format!("Message sent: {}", result.filename) }],
        "file": result.filename
    }))
}

/// `cascade.master_lists` — read a master list file.
pub(super) async fn handle_master_lists(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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
pub(super) async fn handle_memory_write(args: &Value) -> std::result::Result<Value, JsonRpcError> {
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
/// double-serialization — T-P4-E04-22 architectural requirement). When a live
/// retriever is injected, real RAG hits are retrieved; otherwise a single
/// informational fallback chunk is returned. The optimizer pipeline (dedup,
/// window, shell compression) is fully operational.
///
/// ## Cross-session dedup (T-P7-E15-01)
///
/// When a live SQLite pool (`DbPool`) is injected via
/// [`ToolRegistry::with_db_pool`] AND the caller supplies a `session_id`,
/// chunks already delivered to that session within the last 30 minutes are
/// suppressed (via the `context_fingerprints` table) and the chunks actually
/// returned are recorded for future suppression. Without a pool or
/// `session_id`, every call returns independent results (the prior behaviour).
///
/// ## Arguments
/// - `query`         — natural-language query (required)
/// - `budget_tokens` — max tokens to include (256–32768, default 4096)
/// - `session_id`    — optional; enables cross-session dedup when a pool is present
/// - `include_shell` — if true, compress embedded shell snippets
/// - `project`       — optional project name filter (forwarded to future retriever)
///
/// ## Returns
/// `CallToolResult` with:
/// - `content[0].text` — markdown context (fenced blocks with citation headers)
/// - `metadata.tokens_used` — estimated token count of included chunks
/// - `metadata.chunks_returned` — number of chunks in the slice
/// - `metadata.dedup_applied` — `true` when cross-session dedup ran
/// - `metadata.dedup_suppressed` — chunks filtered by cross-session dedup
pub(super) async fn handle_context_slice(
    args: &Value,
    retriever: Option<Arc<dyn Retriever>>,
    db_pool: Option<cascade_db::pool::DbPool>,
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
    // Role and model for ContextAssembler profile lookup.
    let role = args
        .get("role")
        .and_then(|v| v.as_str())
        .unwrap_or("default")
        .to_string();
    let model = args
        .get("model")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();

    debug!(
        query,
        budget_tokens,
        session_id = ?session_id,
        include_shell,
        project = ?project,
        role = %role,
        model = %model,
        "cascade.context_slice"
    );

    // ── RAG retrieve (E-05 unblock) ───────────────────────────────────────────
    // When a live retriever is injected via ToolRegistry::with_retriever, we
    // execute a real retrieval pass.  When absent (index not yet built or daemon
    // not started), we fall back to a single informational chunk so the optimizer
    // pipeline still runs and the caller gets a non-empty, valid response.
    //
    // Architecture note (T-P4-E04-22): direct crate-level call, no daemon IPC.
    let retriever_ready = retriever.is_some();
    let raw_hits: Vec<RetrievalHit> = match retriever {
        Some(ret) => {
            // Build retrieve opts: limit to profile k_chunks (default 10).
            let opts = build_retrieve_opts(10, None);
            ret.retrieve(query, &opts)
                .await
                .map_err(|e| JsonRpcError::internal(format!("retrieval failed: {e}")))?
        }
        None => {
            // TODO(E-05-follow): inject retriever via ToolRegistry::with_retriever
            // so this branch is never taken in production.  Currently the
            // ToolRegistry does not forward the retriever slot to context_slice;
            // that wiring is the remaining step to fully close E-05.
            Vec::new()
        }
    };

    // Fallback informational chunk when the retriever is not wired in.  This is
    // NOT real retrieved context, so it is excluded from cross-session dedup
    // and fingerprint recording.
    let fallback_msg: Option<String> = if !retriever_ready {
        Some(format!(
            "cascade.context_slice: index not ready (query={query:?}). \
             Run `cascade index rebuild` then restart the MCP server."
        ))
    } else {
        None
    };

    // ── Cross-session dedup (T-P7-E15-01) ─────────────────────────────────────
    // When a live SQLite pool AND a session_id are supplied, filter out chunks
    // already delivered to this session within the last 30 minutes via the
    // `context_fingerprints` table.  This runs BEFORE assembly so the token
    // window operates on the deduped set.  rusqlite is sync, so the DB work
    // runs in `spawn_blocking` (same pattern as handlers_memory).
    //
    // Dedup is only applied to real retrieved chunks (not the fallback message)
    // and requires both a pool and a session_id; otherwise we pass through.
    let raw_count = raw_hits.len();
    let can_dedup = db_pool.is_some() && session_id.is_some() && retriever_ready;
    let deduped_hits: Vec<RetrievalHit> = match (db_pool.clone(), session_id) {
        (Some(pool), Some(sid)) if retriever_ready => {
            let sid = sid.to_string();
            let opt = ContextOptimizer::new(budget_tokens);
            tokio::task::spawn_blocking(
                move || -> std::result::Result<Vec<RetrievalHit>, String> {
                    let conn = pool.get().map_err(|e| format!("db pool checkout: {e}"))?;
                    Ok(opt.cross_session_dedup(raw_hits, &sid, &conn))
                },
            )
            .await
            .map_err(|e| JsonRpcError::internal(format!("dedup task join: {e}")))?
            .map_err(JsonRpcError::internal)?
        }
        _ => raw_hits,
    };
    let dedup_suppressed = raw_count.saturating_sub(deduped_hits.len());

    // Convert to text strings for the ContextAssembler.
    let raw_chunks: Vec<String> = if let Some(msg) = fallback_msg {
        vec![msg]
    } else {
        deduped_hits.iter().map(|h| h.text.clone()).collect()
    };

    // ── ContextAssembler (ctx-01) ─────────────────────────────────────────────
    // Load profiles from disk if available; fall back to built-in defaults.
    let assembler = ContextAssembler::new();
    let req = ContextRequest {
        query: query.to_string(),
        role: role.clone(),
        model: model.clone(),
        token_budget: budget_tokens,
    };
    let assembled = assembler.assemble(&req, raw_chunks);

    // ── Optional shell compression ────────────────────────────────────────────
    // When include_shell is set, run the ContextOptimizer's shell compressor over
    // the assembled chunks before formatting.  Reuse the same budget the assembler
    // already applied so we don't double-charge tokens.
    let final_chunks: Vec<String> = if include_shell && assembled.tokens_used > 0 {
        let optimizer = ContextOptimizer::new(assembled.tokens_used.max(64));
        assembled
            .chunks
            .iter()
            .map(|c| optimizer.compress_shell(c))
            .collect()
    } else {
        assembled.chunks.clone()
    };

    // ── Format output as markdown ─────────────────────────────────────────────
    let chunks_returned = final_chunks.len();
    let tokens_used = assembled.tokens_used;

    let mut md = String::new();
    if final_chunks.is_empty() {
        md.push_str(&format!(
            "<!-- cascade.context_slice: query={query:?} budget={budget_tokens} chunks=0 -->\n\
             *No context chunks available — ensure `cascaded start` is running and the index is built.*"
        ));
    } else {
        for (i, chunk) in final_chunks.iter().enumerate() {
            md.push_str(&format!(
                "<!-- chunk:{i} role:{role} -->\n```\n{chunk}\n```\n\n"
            ));
        }
    }

    // ── Record delivered fingerprints (T-P7-E15-01) ───────────────────────────
    // Persist fingerprints of the chunks actually returned so a subsequent
    // context_slice for the same session_id within 30 min suppresses them.
    // Called AFTER the response markdown is built (per `record_delivered`'s
    // contract) so a transport error dropping the reply does not mark
    // undelivered chunks as delivered.
    //
    // Only real retrieved chunks are recorded (not the fallback message), and
    // only when both a pool and session_id are present.  Recording failures are
    // non-fatal: the response is already valid, so we log and continue rather
    // than surfacing a protocol error.
    if retriever_ready && !assembled.chunks.is_empty() {
        if let (Some(pool), Some(sid)) = (db_pool.as_ref(), session_id) {
            let pool = pool.clone();
            let sid = sid.to_string();
            // `assembled.chunks` are the raw (pre-shell-compression) texts of the
            // hits that survived the token window.  Their fingerprints match the
            // fingerprints computed by `cross_session_dedup` on raw retrieved
            // text, so future suppression is consistent.
            let delivered: Vec<RetrievalHit> = assembled
                .chunks
                .iter()
                .enumerate()
                .map(|(i, text)| RetrievalHit {
                    chunk_id: format!("delivered-{i}"),
                    text: text.clone(),
                    file_path: None,
                    start_line: None,
                    end_line: None,
                    score: 1.0,
                    rank: i,
                    tier: None,
                })
                .collect();
            let opt = ContextOptimizer::new(budget_tokens);
            let join_res =
                tokio::task::spawn_blocking(move || -> std::result::Result<(), String> {
                    let conn = pool.get().map_err(|e| format!("db pool checkout: {e}"))?;
                    opt.record_delivered(&sid, &delivered, &conn)
                        .map_err(|e| format!("record_delivered: {e}"))
                })
                .await;
            match join_res {
                Ok(Ok(())) => {}
                Ok(Err(e)) => warn!("context_slice record_delivered failed: {e}"),
                Err(e) => warn!("context_slice record_delivered task join failed: {e}"),
            }
        }
    }

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": md }],
        "metadata": {
            "tokens_used": tokens_used,
            "chunks_returned": chunks_returned,
            "query": query,
            "budget_tokens": budget_tokens,
            "role": role,
            "tier": assembled.tier,
            "retriever_ready": retriever_ready,
            "dedup_applied": can_dedup,
            "dedup_suppressed": dedup_suppressed,
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
