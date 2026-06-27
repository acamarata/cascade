//! # tool::handlers_memory
//!
//! Purpose: MCP tool handlers for the RAG-08 memory module.
//!   Implements `cascade.memory.remember`, `cascade.memory.recall`,
//!   `cascade.memory.forget`, and `cascade.memory.search`.
//!
//! Inputs:  JSON args from MCP tools/call, per-connection context
//! Outputs: MCP CallToolResult JSON (content array + metadata)
//! Constraints:
//!   - All handlers enforce the personal-namespace firewall at the tool boundary.
//!     External clients cannot access namespace=personal without opt_in=true.
//!   - rusqlite is sync; all DB work runs in `spawn_blocking`.
//!   - DB path: `~/.cascade/memory.db` (resolved via `crate::paths::memory_db_path`).
//!   - Migrations run automatically if the DB is new/stale.

use serde_json::Value;

use cascade_rag::memory::{
    delete_episode, insert_episode, recall as memory_recall,
    validate_with_firewall, NamespaceError,
};
use cascade_rag::db::run_migrations;

use crate::paths::memory_db_path;
use crate::server::JsonRpcError;

// ── Helper: open or create the memory DB ─────────────────────────────────────

fn open_memory_db() -> std::result::Result<rusqlite::Connection, String> {
    let path = memory_db_path();

    // Ensure ~/.cascade/ exists.
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create ~/.cascade/: {e}"))?;
    }

    let conn = rusqlite::Connection::open(&path)
        .map_err(|e| format!("failed to open memory DB at {}: {e}", path.display()))?;

    run_migrations(&conn)
        .map_err(|e| format!("memory DB migration failed: {e}"))?;

    Ok(conn)
}

// ── Personal firewall helper ──────────────────────────────────────────────────

fn extract_namespace_and_opt_in(args: &Value) -> std::result::Result<(String, bool), JsonRpcError> {
    let namespace = args
        .get("namespace")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'namespace' is required"))?
        .to_owned();
    let opt_in = args
        .get("opt_in")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    Ok((namespace, opt_in))
}

fn firewall_error_to_tool_result(err: NamespaceError) -> Value {
    serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("Memory namespace error: {err}")
        }],
        "isError": true,
        "metadata": { "error_kind": "namespace_error" }
    })
}

// ── Handler: cascade.memory.remember ─────────────────────────────────────────

/// Insert an episode into memory for the given namespace.
///
/// Personal namespace is blocked unless `opt_in: true` is passed.
pub(super) async fn handle_memory_remember(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let (namespace_raw, opt_in) = extract_namespace_and_opt_in(args)?;
    let content = args
        .get("content")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'content' is required"))?
        .to_owned();

    // Validate namespace + personal firewall at tool boundary.
    let ns = match validate_with_firewall(&namespace_raw, opt_in) {
        Ok(ns) => ns,
        Err(e) => return Ok(firewall_error_to_tool_result(e)),
    };

    let id = tokio::task::spawn_blocking(move || {
        let conn = open_memory_db()?;
        insert_episode(&conn, &ns, &content).map_err(|e| format!("insert_episode failed: {e}"))
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking panic: {e}")))?
    .map_err(|e| JsonRpcError::internal(e))?;

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("Remembered episode {id} in namespace '{namespace_raw}'")
        }],
        "metadata": { "id": id, "namespace": namespace_raw }
    }))
}

// ── Handler: cascade.memory.recall ───────────────────────────────────────────

/// Query memory for the given namespace and query string.
///
/// Personal namespace is blocked unless `opt_in: true`.
pub(super) async fn handle_memory_recall(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let (namespace_raw, opt_in) = extract_namespace_and_opt_in(args)?;
    let query = args
        .get("query")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'query' is required"))?
        .to_owned();
    let k = args
        .get("k")
        .and_then(|v| v.as_u64())
        .unwrap_or(10)
        .clamp(1, 50) as usize;

    let ns = match validate_with_firewall(&namespace_raw, opt_in) {
        Ok(ns) => ns,
        Err(e) => return Ok(firewall_error_to_tool_result(e)),
    };

    let hits = tokio::task::spawn_blocking(move || {
        let conn = open_memory_db()?;
        memory_recall(&conn, &ns, &query, k).map_err(|e| format!("recall failed: {e}"))
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking panic: {e}")))?
    .map_err(|e| JsonRpcError::internal(e))?;

    let results: Vec<Value> = hits
        .iter()
        .map(|h| {
            serde_json::json!({
                "id": h.id,
                "source": h.source,
                "content": h.content,
                "confidence": h.confidence,
                "timestamp": h.timestamp
            })
        })
        .collect();

    let text = if results.is_empty() {
        format!("No memory found in namespace '{}' for query '{}'", namespace_raw, args.get("query").and_then(|v| v.as_str()).unwrap_or(""))
    } else {
        results
            .iter()
            .enumerate()
            .map(|(i, r)| {
                format!(
                    "{}. [{}] {} (confidence: {})",
                    i + 1,
                    r["source"].as_str().unwrap_or("?"),
                    r["content"].as_str().unwrap_or("?"),
                    r["confidence"].as_f64().unwrap_or(0.0)
                )
            })
            .collect::<Vec<_>>()
            .join("\n")
    };

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "hits": results,
        "metadata": { "namespace": namespace_raw, "count": results.len(), "k": k }
    }))
}

// ── Handler: cascade.memory.forget ───────────────────────────────────────────

/// Archive or delete a memory item by id.
///
/// Personal namespace is blocked unless `opt_in: true`.
pub(super) async fn handle_memory_forget(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let (namespace_raw, opt_in) = extract_namespace_and_opt_in(args)?;
    let id = args
        .get("id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'id' is required"))?
        .to_owned();
    let kind = args
        .get("kind")
        .and_then(|v| v.as_str())
        .unwrap_or("episode")
        .to_owned();

    let ns = match validate_with_firewall(&namespace_raw, opt_in) {
        Ok(ns) => ns,
        Err(e) => return Ok(firewall_error_to_tool_result(e)),
    };

    let kind_clone = kind.clone();
    let affected = tokio::task::spawn_blocking(move || -> std::result::Result<usize, String> {
        let conn = open_memory_db()?;
        match kind_clone.as_str() {
            "fact" => {
                // For facts, id is used as content_hash (the stable unique key for facts).
                cascade_rag::memory::archive_fact(&conn, &ns, &id)
                    .map_err(|e| format!("archive_fact failed: {e}"))
            }
            _ => {
                // Default: episode — hard delete.
                delete_episode(&conn, &ns, &id)
                    .map_err(|e| format!("delete_episode failed: {e}"))
            }
        }
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking panic: {e}")))?
    .map_err(|e| JsonRpcError::internal(e))?;

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": if affected > 0 {
                format!("Forgot {kind} in namespace '{namespace_raw}'")
            } else {
                format!("No {kind} found with that id in namespace '{namespace_raw}'")
            }
        }],
        "metadata": { "namespace": namespace_raw, "affected": affected }
    }))
}

// ── Handler: cascade.memory.search ───────────────────────────────────────────

/// Semantic search within a namespace (alias of recall for discoverability).
///
/// Personal namespace is blocked unless `opt_in: true`.
pub(super) async fn handle_memory_search(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    // search is identical to recall — delegate.
    handle_memory_recall(args).await
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use serde_json::json;
    use tempfile::TempDir;

    use super::*;

    /// Override the DB path to a temp file for tests.
    /// We can't easily do this without refactoring, so tests use integration
    /// assertions through the handler return values with real temp DBs.
    /// The simplest approach: test via direct module calls (already covered
    /// in cascade-rag memory module tests). Here we just smoke-test the
    /// handler JSON parsing and firewall enforcement without DB I/O.

    #[tokio::test]
    async fn remember_blocks_personal_without_opt_in() {
        let args = json!({
            "namespace": "personal",
            "content": "secret thought"
        });
        // Without opt_in, should return a tool-level error (not a protocol error).
        let result = handle_memory_remember(&args).await.unwrap();
        let is_error = result["isError"].as_bool().unwrap_or(false);
        assert!(is_error, "personal namespace without opt_in must return isError=true");
    }

    #[tokio::test]
    async fn recall_blocks_personal_without_opt_in() {
        let args = json!({
            "namespace": "personal",
            "query": "anything"
        });
        let result = handle_memory_recall(&args).await.unwrap();
        let is_error = result["isError"].as_bool().unwrap_or(false);
        assert!(is_error, "personal namespace without opt_in must return isError=true");
    }

    #[tokio::test]
    async fn forget_blocks_personal_without_opt_in() {
        let args = json!({
            "namespace": "personal",
            "id": "some-uuid"
        });
        let result = handle_memory_forget(&args).await.unwrap();
        let is_error = result["isError"].as_bool().unwrap_or(false);
        assert!(is_error, "personal namespace without opt_in must return isError=true");
    }

    #[tokio::test]
    async fn remember_missing_content_returns_protocol_error() {
        let args = json!({ "namespace": "dev-test" });
        let result = handle_memory_remember(&args).await;
        assert!(result.is_err(), "missing 'content' must be a protocol error");
    }

    #[tokio::test]
    async fn recall_missing_query_returns_protocol_error() {
        let args = json!({ "namespace": "dev-test" });
        let result = handle_memory_recall(&args).await;
        assert!(result.is_err(), "missing 'query' must be a protocol error");
    }
}
