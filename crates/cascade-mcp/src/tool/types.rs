//! Core types shared across the tool module.

use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokio::sync::RwLock;

use cascade_types::retriever::Retriever;

/// Shared interior-mutable slot holding an optional live [`Retriever`].
///
/// The slot is `None` until a background task opens the index and injects a
/// real retriever.  Search handlers read-lock, clone the `Option` out, then
/// drop the guard *before* any `.await` — the guard is never held across an
/// await point.
pub type RetrieverSlot = Arc<RwLock<Option<Arc<dyn Retriever>>>>;

/// Shared interior-mutable slot holding an optional live SQLite connection
/// pool ([`cascade_db::pool::DbPool`]).
///
/// `None` until a pool is injected via [`ToolRegistry::with_db_pool`] or a
/// background task writes into the slot returned by
/// [`ToolRegistry::db_pool_slot`].  `cascade.context_slice` snapshots the
/// slot (read-lock → clone the `Option` out → drop the guard) and, when a
/// pool is present together with a `session_id`, runs cross-session chunk
/// dedup against the `context_fingerprints` table (T-P7-E15-01).
///
/// `DbPool` is cheaply cloneable (an `Arc` internally), so cloning the
/// `Option` out of the slot is a shallow clone.
pub type DbPoolSlot = Arc<RwLock<Option<cascade_db::pool::DbPool>>>;

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
