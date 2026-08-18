//! ToolRegistry — handles `tools/list` and `tools/call` for the MCP server.

use std::sync::Arc;

use serde_json::Value;
use tracing::debug;

use cascade_types::error::Result;
use cascade_types::retriever::Retriever;

use crate::auth::McpAuth;
use crate::server::JsonRpcError;

use super::handlers_core::{
    handle_context_slice, handle_inbox_list, handle_inbox_send, handle_master_lists,
    handle_memory_read, handle_memory_write, handle_provide_harness_context, handle_read,
    handle_search, handle_search_codebase,
};
use super::handlers_memory::{
    handle_memory_forget, handle_memory_recall, handle_memory_remember, handle_memory_search,
};
use super::handlers_pbd::{
    handle_append_event, handle_check_routes, handle_get_current, handle_get_sprint,
    handle_list_tickets, handle_read_phase_status, handle_scan_inbox, handle_update_ticket_status,
};
use super::handlers_security::{handle_secret_scan, handle_security_audit};
use super::helpers::{call_tool_error, tool_result};
use super::schemas::{
    cascade_append_event_tool, cascade_check_routes_tool, cascade_context_slice_tool,
    cascade_get_current_tool, cascade_get_sprint_tool, cascade_inbox_list_tool,
    cascade_inbox_send_tool, cascade_list_tickets_tool, cascade_master_lists_tool,
    cascade_memory_forget_tool, cascade_memory_read_tool, cascade_memory_recall_tool,
    cascade_memory_remember_tool, cascade_memory_search_tool, cascade_memory_write_tool,
    cascade_provide_harness_context_tool, cascade_read_phase_status_tool, cascade_read_tool,
    cascade_scan_inbox_tool, cascade_search_codebase_tool, cascade_search_tool,
    cascade_security_audit_tool, cascade_security_secret_scan_tool,
    cascade_update_ticket_status_tool,
};
use super::types::{ConnectionContext, DbPoolSlot, RetrieverSlot};

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
    /// Shared slot for the live SQLite connection pool used by
    /// `cascade.context_slice` cross-session dedup (T-P7-E15-01).
    /// `None` = dedup disabled (every session gets independent results).
    db_pool: DbPoolSlot,
}

impl ToolRegistry {
    /// Create a registry without an auth backend, retriever, or DB pool.
    ///
    /// `cascade.memory.write` calls are always rejected; `cascade.search`
    /// returns an "index not ready" message until a retriever is injected;
    /// `cascade.context_slice` skips cross-session dedup until a pool is
    /// injected via [`ToolRegistry::with_db_pool`].
    pub fn new() -> Self {
        Self {
            auth: None,
            retriever: Arc::new(tokio::sync::RwLock::new(None)),
            db_pool: Arc::new(tokio::sync::RwLock::new(None)),
        }
    }

    /// Create a registry with an [`McpAuth`] backend for the memory.write gate.
    pub fn with_auth(auth: Arc<McpAuth>) -> Self {
        Self {
            auth: Some(auth),
            retriever: Arc::new(tokio::sync::RwLock::new(None)),
            db_pool: Arc::new(tokio::sync::RwLock::new(None)),
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

    /// Synchronously fill the SQLite pool slot so that `cascade.context_slice`
    /// performs cross-session chunk dedup against the `context_fingerprints`
    /// table (T-P7-E15-01).
    ///
    /// Intended for construction-time injection (tests, daemon).  For
    /// post-construction background injection use [`ToolRegistry::db_pool_slot`].
    ///
    /// The pool must point at a database that has run the Cascade migrations
    /// (migration 0008 creates `context_fingerprints`).  When the slot is
    /// `None`, `cascade.context_slice` skips dedup and every session receives
    /// independent results — the pre-T-P7-E15-01 behaviour.
    pub fn with_db_pool(self, pool: cascade_db::pool::DbPool) -> Self {
        // `try_write` on a freshly created, uncontested lock always succeeds.
        if let Ok(mut slot) = self.db_pool.try_write() {
            *slot = Some(pool);
        }
        self
    }

    /// Return a clone of the shared [`DbPoolSlot`] so that a background task
    /// can inject a pool after the server is already running.
    ///
    /// The background task should:
    /// 1. Build the pool (`cascade_db::pool::build_pool`) and run migrations.
    /// 2. Acquire a write-lock on the slot.
    /// 3. Store `Some(pool)`.
    /// 4. Drop the write-lock.
    ///
    /// Subsequent `cascade.context_slice` calls (with a `session_id`) will
    /// then dedup across sessions.
    pub fn db_pool_slot(&self) -> DbPoolSlot {
        Arc::clone(&self.db_pool)
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
            // RAG-08 memory tools
            cascade_memory_remember_tool(),
            cascade_memory_recall_tool(),
            cascade_memory_forget_tool(),
            cascade_memory_search_tool(),
            // Security tools
            cascade_security_secret_scan_tool(),
            cascade_security_audit_tool(),
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

        // Snapshot the DB pool slot the same way (T-P7-E15-01).  DbPool is
        // cheaply cloneable (Arc internally), so this is a shallow clone.
        let db_pool_snapshot: Option<cascade_db::pool::DbPool> = {
            let guard = self.db_pool.read().await;
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
            "cascade.context_slice" => {
                tool_result(handle_context_slice(&args, retriever_snapshot, db_pool_snapshot).await)
            }
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
            // RAG-08 memory tools — enforce personal-namespace firewall at tool boundary
            "cascade.memory.remember" => tool_result(handle_memory_remember(&args).await),
            "cascade.memory.recall" => tool_result(handle_memory_recall(&args).await),
            "cascade.memory.forget" => tool_result(handle_memory_forget(&args).await),
            "cascade.memory.search" => tool_result(handle_memory_search(&args).await),
            // Security tools
            "cascade.security.secret_scan" => tool_result(handle_secret_scan(&args).await),
            "cascade.security.audit" => tool_result(handle_security_audit(&args).await),
            _ => Err(JsonRpcError::not_found(format!("Unknown tool: {name}"))),
        }
    }
}

impl Default for ToolRegistry {
    fn default() -> Self {
        Self::new()
    }
}
