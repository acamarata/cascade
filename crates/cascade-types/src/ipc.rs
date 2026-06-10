// FROZEN — schema version 1. Add new methods by appending only; never remove or rename.
// This file is the canonical JSON-RPC 2.0 protocol contract for all Cascade IPC clients
// (cascade-cli, OS widgets, MCP server in E7, and future external integrations).
// Any schema change requires a versioning ticket before any dependent crate is updated.

//! # cascade IPC protocol — JSON-RPC 2.0 types
//!
//! Defines the frozen wire-format types used by every Cascade IPC client.
//!
//! ## Architecture
//!
//! ```text
//! cascade-cli / widgets / MCP  ──[JSON-RPC 2.0]──► cascaded daemon
//!                                                        │
//!                                               dispatch to handlers
//! ```
//!
//! ## Protocol summary
//!
//! | Field | Type | Notes |
//! |-------|------|-------|
//! | `jsonrpc` | `"2.0"` | Always `"2.0"` per spec |
//! | `id` | `RequestId` | Number, string, or null per JSON-RPC 2.0 |
//! | `method` | `String` | e.g. `"ping"`, `"cascade.status"` |
//! | `params` | `Option<P>` | Method-specific params struct |
//! | `result` | `Option<R>` | Present on success |
//! | `error` | `Option<RpcError>` | Present on failure; mutually exclusive with `result` |
//!
//! ## Error codes
//!
//! | Constant | Value | Meaning |
//! |----------|-------|---------|
//! | `METHOD_NOT_FOUND` | -32601 | No handler for the requested method |
//! | `INVALID_PARAMS` | -32602 | Params failed validation |
//! | `INTERNAL_ERROR` | -32603 | Unhandled daemon-side error |
//! | `DAEMON_NOT_RUNNING` | -32001 | Client tried to connect but daemon is not up |
//! | `AUTH_FAILED` | -32002 | Auth token missing or invalid |
//! | `RESOURCE_NOT_FOUND` | -32003 | Requested resource does not exist |

use serde::{Deserialize, Serialize};

// ── Protocol version ──────────────────────────────────────────────────────────

/// Bump this when a non-backward-compatible schema change is unavoidable.
/// Clients can reject connections from daemons with a different `protocol_version`.
pub const PROTOCOL_VERSION: u8 = 1;

// ── Error code constants ──────────────────────────────────────────────────────

/// JSON-RPC 2.0 standard: no handler registered for the requested method.
pub const METHOD_NOT_FOUND: i32 = -32601;

/// JSON-RPC 2.0 standard: params failed validation.
pub const INVALID_PARAMS: i32 = -32602;

/// JSON-RPC 2.0 standard: unhandled daemon-side error.
pub const INTERNAL_ERROR: i32 = -32603;

/// Cascade extension: client tried to reach the daemon but it is not running.
pub const DAEMON_NOT_RUNNING: i32 = -32001;

/// Cascade extension: auth token missing or invalid (populated by T-P2-E03-04).
pub const AUTH_FAILED: i32 = -32002;

/// Cascade extension: the requested resource does not exist.
pub const RESOURCE_NOT_FOUND: i32 = -32003;

// ── Core JSON-RPC 2.0 envelope types ─────────────────────────────────────────

/// The `jsonrpc` version discriminator.
///
/// Always serialises to the string `"2.0"`. Deserialisation rejects any other value.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum JsonRpcVersion {
    /// JSON-RPC protocol version 2.0.
    #[serde(rename = "2.0")]
    V2_0,
}

/// A JSON-RPC 2.0 request `id`.
///
/// Per spec, the id may be a number, a string, or null (for notifications).
/// Framing-level code rejects absent `id` fields — all Cascade requests are
/// call-style (non-notification) so the field must be present.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RequestId {
    /// Numeric id (most common; counters start at 1).
    Number(i64),
    /// String id (useful for correlation with user-visible labels).
    String(String),
    /// Null id (notifications; the daemon sends no response).
    Null,
}

/// A JSON-RPC 2.0 request envelope.
///
/// `P` is the method-specific params struct. Use `serde_json::Value` when the
/// params type is unknown at parse time.
///
/// # Example
///
/// ```rust
/// use cascade_types::ipc::{JsonRpcVersion, Request, RequestId, PingParams, PROTOCOL_VERSION};
/// use serde_json;
///
/// let req = Request {
///     jsonrpc: JsonRpcVersion::V2_0,
///     id: RequestId::Number(1),
///     method: "ping".to_string(),
///     params: Some(PingParams { echo: Some("hello".to_string()) }),
///     protocol_version: PROTOCOL_VERSION,
/// };
/// let json = serde_json::to_string(&req).unwrap();
/// let back: Request<PingParams> = serde_json::from_str(&json).unwrap();
/// assert_eq!(back.id, RequestId::Number(1));
/// ```
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request<P> {
    /// Always `"2.0"`.
    pub jsonrpc: JsonRpcVersion,
    /// Correlation id. Must be present for all Cascade call-style requests.
    pub id: RequestId,
    /// Method name, e.g. `"ping"` or `"cascade.status"`.
    pub method: String,
    /// Method-specific parameters; `None` for methods that take no params.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<P>,
    /// Schema version. Clients set this to [`PROTOCOL_VERSION`].
    /// Daemons may reject mismatches.
    pub protocol_version: u8,
}

/// A JSON-RPC 2.0 response envelope.
///
/// `R` is the method-specific result struct. Per spec, exactly one of `result`
/// and `error` is populated; never both.
///
/// # Example
///
/// ```rust
/// use cascade_types::ipc::{JsonRpcVersion, Response, RequestId, PingResult};
/// use serde_json;
///
/// let resp = Response {
///     jsonrpc: JsonRpcVersion::V2_0,
///     id: RequestId::Number(1),
///     result: Some(PingResult { pong: "hello".to_string() }),
///     error: None,
/// };
/// let json = serde_json::to_string(&resp).unwrap();
/// let back: Response<PingResult> = serde_json::from_str(&json).unwrap();
/// assert_eq!(back.result.unwrap().pong, "hello");
/// ```
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Response<R> {
    /// Always `"2.0"`.
    pub jsonrpc: JsonRpcVersion,
    /// Echoed from the corresponding request.
    pub id: RequestId,
    /// Present on success; absent on error.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<R>,
    /// Present on error; absent on success.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<RpcError>,
}

/// A JSON-RPC 2.0 error object.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RpcError {
    /// Numeric error code. Use the `*` constants defined in this module.
    pub code: i32,
    /// Human-readable error message. Never expose internal stack traces here.
    pub message: String,
    /// Optional structured detail; schema is method-specific.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<serde_json::Value>,
}

// ── Method params / result structs (alphabetical) ────────────────────────────

// --- config_get ---

/// Params for the `config_get` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConfigGetParams {
    /// Dot-separated config key, e.g. `"daemon.socket_path"`.
    pub key: String,
}

/// Result for the `config_get` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConfigGetResult {
    /// The key that was queried.
    pub key: String,
    /// Current value; type depends on the key.
    pub value: serde_json::Value,
}

// --- config_set ---

/// Params for the `config_set` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConfigSetParams {
    /// Dot-separated config key to update.
    pub key: String,
    /// New value to persist.
    pub value: serde_json::Value,
}

/// Result for the `config_set` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConfigSetResult {
    /// The key that was updated.
    pub key: String,
    /// Previous value, if any existed.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub previous: Option<serde_json::Value>,
}

// --- daemon_stop ---

/// Params for the `daemon_stop` method (no fields; included for type uniformity).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DaemonStopParams {}

/// Result for the `daemon_stop` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DaemonStopResult {
    /// Always `"stopping"`. Clients should not expect further responses.
    pub status: String,
}

// --- health ---

/// Params for the `health` method (no fields).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HealthParams {}

/// A single health check entry.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HealthCheck {
    /// Short name for the check, e.g. `"sqlite"` or `"rag_index"`.
    pub name: String,
    /// Whether the check passed.
    pub ok: bool,
    /// Human-readable detail — error message or extra context.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// Result for the `health` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HealthResult {
    /// `true` when all checks pass.
    pub ok: bool,
    /// Individual check results.
    pub checks: Vec<HealthCheck>,
}

// --- hotword_lookup ---

/// Params for the `hotword_lookup` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HotwordLookupParams {
    /// The hotword string to look up in the quota table.
    pub word: String,
}

/// Result for the `hotword_lookup` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HotwordLookupResult {
    /// The matching block string, or `None` if not found.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub block: Option<String>,
}

// --- inbox_summary ---

/// Params for the `inbox_summary` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InboxSummaryParams {
    /// Max number of inbox entries to return. `None` returns all available.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub limit: Option<usize>,
}

/// A single inbox item in the summary list.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InboxItem {
    /// Unique message id (filename slug or UUID).
    pub id: String,
    /// Message subject line.
    pub subject: String,
    /// Sender identity string.
    pub from: String,
    /// Priority label: `"critical"`, `"high"`, `"medium"`, or `"low"`.
    pub priority: String,
    /// ISO 8601 creation timestamp.
    pub created: String,
}

/// Result for the `inbox_summary` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InboxSummaryResult {
    /// Inbox items in reverse chronological order.
    pub items: Vec<InboxItem>,
}

// --- memory_read ---

/// Params for the `memory_read` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MemoryReadParams {
    /// Project root path or project slug used to locate the `.claude/memory/` dir.
    pub project: String,
    /// Memory file name relative to `.claude/memory/`, e.g. `"decisions.md"`.
    pub file: String,
}

/// Result for the `memory_read` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MemoryReadResult {
    /// Full UTF-8 text content of the memory file.
    pub content: String,
    /// Absolute path to the file that was read.
    pub path: String,
}

// --- memory_write ---

/// Params for the `memory_write` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MemoryWriteParams {
    /// Project root path or project slug.
    pub project: String,
    /// Memory file name relative to `.claude/memory/`, e.g. `"lessons.md"`.
    pub file: String,
    /// Full UTF-8 content to write (overwrites existing content).
    pub content: String,
}

/// Result for the `memory_write` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MemoryWriteResult {
    /// Absolute path of the file that was written.
    pub path: String,
    /// Number of bytes written.
    pub bytes: usize,
}

// --- ping ---

/// Params for the `ping` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PingParams {
    /// Optional payload echoed back verbatim in the response.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub echo: Option<String>,
}

/// Result for the `ping` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PingResult {
    /// Echo payload from the request, or an empty string if none was sent.
    pub pong: String,
}

// --- provider_quota ---

/// Params for the `provider_quota` method (no fields).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProviderQuotaParams {}

/// A single AI provider quota entry.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProviderEntry {
    /// Provider name, e.g. `"anthropic"`, `"openai"`, `"gemini"`.
    pub name: String,
    /// Percentage of the quota consumed this billing period (0.0–100.0).
    pub pct_used: f32,
    /// ISO 8601 reset timestamp, if known.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub resets_at: Option<String>,
}

/// Result for the `provider_quota` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProviderQuotaResult {
    /// Quota entries for all configured providers.
    pub providers: Vec<ProviderEntry>,
}

// --- resolve ---

/// Params for the `resolve` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ResolveParams {
    /// Target tier slug, e.g. `"gci"`, `"ppi"`, `"pri"`. `None` returns the full cascade.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tier: Option<String>,
    /// Output format: `"markdown"` (default) or `"json"`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub format: Option<String>,
}

/// Result for the `resolve` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ResolveResult {
    /// Resolved context content.
    pub content: String,
    /// Format of the returned content: `"markdown"` or `"json"`.
    pub format: String,
    /// Tier that was resolved, e.g. `"gci"` or `"full"`.
    pub tier: String,
}

// --- search ---

/// Params for the `search` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SearchParams {
    /// Natural language query string.
    pub query: String,
    /// Maximum number of hits to return. Defaults to 10 server-side if absent.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub limit: Option<usize>,
}

/// A single search result hit.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SearchHit {
    /// Unique chunk or document id.
    pub id: String,
    /// Relevance score (higher = more relevant).
    pub score: f32,
    /// Short excerpt from the matching document.
    pub excerpt: String,
    /// Source document path or URL.
    pub source: String,
}

/// Result for the `search` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SearchResult {
    /// Ranked search hits, best match first.
    pub hits: Vec<SearchHit>,
}

// --- status ---

/// Params for the `status` method (no fields).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StatusParams {}

/// Result for the `status` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StatusResult {
    /// OS process id of the running daemon.
    pub pid: u32,
    /// Seconds since the daemon started.
    pub uptime_secs: u64,
    /// Number of requests currently queued awaiting dispatch.
    pub queue_depth: u32,
    /// Whether the RAG index was last refreshed within its TTL.
    pub rag_index_fresh: bool,
    /// Daemon version string, e.g. `"0.1.0"`.
    pub version: String,
    /// TCP IPC port if enabled (feature: tcp-ipc). Added schema v1.1 — safe additive.
    #[serde(default)]
    pub tcp_port: Option<u16>,
}

// --- update_check (T-P4-E04-14/16) ---

/// Params for the `update_check` method (no fields — daemon uses its own current version).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpdateCheckParams {}

/// Result for the `update_check` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpdateCheckResult {
    /// Whether a newer version is available.
    pub update_available: bool,
    /// Current installed version string, e.g. `"0.1.2"`.
    pub current_version: String,
    /// Latest available version, if newer than current.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub latest_version: Option<String>,
}

// --- update_apply (T-P4-E04-14/16) ---

/// Params for the `update_apply` method.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpdateApplyParams {}

/// Result for the `update_apply` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpdateApplyResult {
    /// Whether the update was applied successfully.
    pub ok: bool,
    /// Version that was installed.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub installed_version: Option<String>,
    /// Snapshot id created before applying (used for rollback).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snapshot_id: Option<String>,
    /// Error message if `ok` is false.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

// --- update_auto (T-P4-E04-16) ---

/// Params for the `update_auto` method — toggle auto-update in config.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpdateAutoParams {
    /// Set to `true` to enable auto-update, `false` to disable.
    pub enable: bool,
}

/// Result for the `update_auto` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpdateAutoResult {
    /// New value of `auto_update` after this call.
    pub auto_update: bool,
}

// --- rollback_list (T-P4-E04-15) ---

/// Params for the `rollback_list` method (no fields).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RollbackListParams {}

/// A single rollback snapshot entry for display.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SnapshotEntry {
    /// Snapshot id, e.g. `"snapshot-1717200000"`.
    pub id: String,
    /// ISO 8601 creation timestamp.
    pub created: String,
    /// Cascade version recorded at snapshot time.
    pub cascade_version: String,
    /// Number of files in the snapshot.
    pub file_count: usize,
    /// Total bytes across all snapshot files.
    pub total_bytes: u64,
}

/// Result for the `rollback_list` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RollbackListResult {
    /// All available snapshots, oldest first.
    pub snapshots: Vec<SnapshotEntry>,
}

// --- rollback_apply (T-P4-E04-15) ---

/// Params for the `rollback_apply` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RollbackApplyParams {
    /// Snapshot id to restore (e.g., `"snapshot-1717200000"`).
    pub snapshot_id: String,
}

/// Result for the `rollback_apply` method.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RollbackApplyResult {
    /// Whether the rollback completed successfully.
    pub ok: bool,
    /// Cascade version restored.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub restored_version: Option<String>,
    /// Error message if `ok` is false.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

// ── Schema-validating deserialization (T-P2-E07-11) ────────────────────────────

/// Deserialize a length-prefixed JSON IPC `Request<P>` body, mapping serde
/// schema violations onto structured [`crate::error::IpcError`] variants.
///
/// Purpose: every IPC message type carries `#[serde(deny_unknown_fields)]`, so an
///   unexpected key or a missing required field fails deserialization. This wrapper
///   translates the opaque serde error text into an actionable [`crate::error::IpcError`]
///   the daemon can return to the client as a structured JSON-RPC error.
/// Inputs: raw JSON bytes of a single request frame.
/// Outputs: `Ok(Request<P>)`, or `IpcError::UnknownField` / `IpcError::MissingField`
///   on schema violation, or a generic `IpcError::MalformedFrame` otherwise.
/// Constraints: pure; does no I/O. The `unknown field` / `missing field` substrings
///   are part of serde's stable human-readable error format.
pub fn deserialize_request<P>(bytes: &[u8]) -> Result<Request<P>, crate::error::IpcError>
where
    P: serde::de::DeserializeOwned,
{
    serde_json::from_slice::<Request<P>>(bytes).map_err(|e| {
        let msg = e.to_string();
        if msg.contains("unknown field") {
            crate::error::IpcError::UnknownField(msg)
        } else if msg.contains("missing field") {
            crate::error::IpcError::MissingField(msg)
        } else {
            crate::error::IpcError::MalformedFrame(msg)
        }
    })
}

// ── Field-value bounds validation (T-P2-E07-12) ─────────────────────────────────

/// Maximum accepted length of a search query string, in characters.
pub const MAX_QUERY_LEN: usize = 2048;

/// Maximum accepted length of a memory-write content payload, in bytes.
pub const MAX_CONTENT_LEN: usize = 524_288; // 512 KiB

/// The canonical cascade tier names, mirroring [`crate::cascade_tier::CascadeTier`]'s
/// `#[serde(rename_all = "lowercase")]` serialization. A `ResolveParams.tier` value
/// outside this set is rejected.
pub const VALID_TIERS: &[&str] = &["gci", "pci", "apc", "ppc", "prc", "pac"];

/// Reject a relative path that contains a `..` traversal component or a null byte.
///
/// cascade-types cannot depend on cascade-core (that would form a dependency cycle:
/// cascade-core already depends on cascade-types), so the traversal guard is
/// inlined here rather than calling `cascade_core::security::validate_cascade_path`.
/// The check is intentionally identical in spirit: reject `..` and `\0`.
fn path_field_is_safe(value: &str) -> bool {
    if value.contains('\0') {
        return false;
    }
    !std::path::Path::new(value)
        .components()
        .any(|c| c == std::path::Component::ParentDir)
}

/// Validate a [`SearchParams`]: the query must not exceed [`MAX_QUERY_LEN`].
pub fn validate_search_params(p: &SearchParams) -> Result<(), crate::error::IpcError> {
    if p.query.chars().count() > MAX_QUERY_LEN {
        return Err(crate::error::IpcError::InvalidFieldValue {
            field: "query".to_string(),
            reason: format!("exceeds {MAX_QUERY_LEN} character cap"),
        });
    }
    Ok(())
}

/// Validate a [`MemoryWriteParams`]: `content` must not exceed [`MAX_CONTENT_LEN`]
/// bytes, and `project` must not contain a path traversal.
pub fn validate_memory_write_params(p: &MemoryWriteParams) -> Result<(), crate::error::IpcError> {
    if p.content.len() > MAX_CONTENT_LEN {
        return Err(crate::error::IpcError::InvalidFieldValue {
            field: "content".to_string(),
            reason: format!("exceeds {MAX_CONTENT_LEN} byte cap"),
        });
    }
    if !path_field_is_safe(&p.project) {
        return Err(crate::error::IpcError::InvalidFieldValue {
            field: "project_path".to_string(),
            reason: "path traversal".to_string(),
        });
    }
    Ok(())
}

/// Validate a [`ResolveParams`]: when `tier` is present it must be one of
/// [`VALID_TIERS`].
pub fn validate_resolve_params(p: &ResolveParams) -> Result<(), crate::error::IpcError> {
    if let Some(tier) = &p.tier {
        if !VALID_TIERS.contains(&tier.as_str()) {
            return Err(crate::error::IpcError::InvalidFieldValue {
                field: "tier".to_string(),
                reason: format!("`{tier}` is not one of {VALID_TIERS:?}"),
            });
        }
    }
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json;

    /// Helper: serialize to JSON then deserialize back, assert equality.
    fn roundtrip<T: Serialize + for<'de> Deserialize<'de> + PartialEq + std::fmt::Debug>(
        value: &T,
    ) {
        let json = serde_json::to_string(value).expect("serialize");
        let back: T = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(value, &back, "roundtrip mismatch for {json}");
    }

    // ── Core envelope types ──────────────────────────────────────────────────

    #[test]
    fn test_request_id_roundtrip() {
        roundtrip(&RequestId::Number(42));
        roundtrip(&RequestId::String("abc".to_string()));
        roundtrip(&RequestId::Null);
    }

    #[test]
    fn test_json_rpc_version_roundtrip() {
        roundtrip(&JsonRpcVersion::V2_0);
    }

    #[test]
    fn test_rpc_error_roundtrip() {
        roundtrip(&RpcError {
            code: INTERNAL_ERROR,
            message: "oops".to_string(),
            data: Some(serde_json::json!({"key": "val"})),
        });
        roundtrip(&RpcError {
            code: METHOD_NOT_FOUND,
            message: "no such method".to_string(),
            data: None,
        });
    }

    #[test]
    fn test_request_envelope_roundtrip() {
        let req = Request {
            jsonrpc: JsonRpcVersion::V2_0,
            id: RequestId::Number(1),
            method: "ping".to_string(),
            params: Some(PingParams {
                echo: Some("hello".to_string()),
            }),
            protocol_version: PROTOCOL_VERSION,
        };
        roundtrip(&req);
    }

    #[test]
    fn test_response_envelope_roundtrip() {
        let resp: Response<PingResult> = Response {
            jsonrpc: JsonRpcVersion::V2_0,
            id: RequestId::Number(1),
            result: Some(PingResult {
                pong: "hello".to_string(),
            }),
            error: None,
        };
        roundtrip(&resp);
    }

    #[test]
    fn test_response_error_roundtrip() {
        let resp: Response<PingResult> = Response {
            jsonrpc: JsonRpcVersion::V2_0,
            id: RequestId::Number(2),
            result: None,
            error: Some(RpcError {
                code: INTERNAL_ERROR,
                message: "daemon failure".to_string(),
                data: None,
            }),
        };
        roundtrip(&resp);
    }

    // ── Method-specific params / result pairs ────────────────────────────────

    #[test]
    fn test_config_get_roundtrip() {
        roundtrip(&ConfigGetParams {
            key: "daemon.socket_path".to_string(),
        });
        roundtrip(&ConfigGetResult {
            key: "daemon.socket_path".to_string(),
            value: serde_json::json!("/tmp/cascade.sock"),
        });
    }

    #[test]
    fn test_config_set_roundtrip() {
        roundtrip(&ConfigSetParams {
            key: "rag.top_k".to_string(),
            value: serde_json::json!(20),
        });
        roundtrip(&ConfigSetResult {
            key: "rag.top_k".to_string(),
            previous: Some(serde_json::json!(10)),
        });
        roundtrip(&ConfigSetResult {
            key: "rag.top_k".to_string(),
            previous: None,
        });
    }

    #[test]
    fn test_daemon_stop_roundtrip() {
        roundtrip(&DaemonStopParams {});
        roundtrip(&DaemonStopResult {
            status: "stopping".to_string(),
        });
    }

    #[test]
    fn test_health_roundtrip() {
        roundtrip(&HealthParams {});
        roundtrip(&HealthCheck {
            name: "sqlite".to_string(),
            ok: true,
            detail: None,
        });
        roundtrip(&HealthCheck {
            name: "rag_index".to_string(),
            ok: false,
            detail: Some("index file missing".to_string()),
        });
        roundtrip(&HealthResult {
            ok: false,
            checks: vec![
                HealthCheck {
                    name: "sqlite".to_string(),
                    ok: true,
                    detail: None,
                },
                HealthCheck {
                    name: "rag_index".to_string(),
                    ok: false,
                    detail: Some("stale".to_string()),
                },
            ],
        });
    }

    #[test]
    fn test_hotword_lookup_roundtrip() {
        roundtrip(&HotwordLookupParams {
            word: "claude".to_string(),
        });
        roundtrip(&HotwordLookupResult {
            block: Some("claude_code".to_string()),
        });
        roundtrip(&HotwordLookupResult { block: None });
    }

    #[test]
    fn test_inbox_summary_roundtrip() {
        roundtrip(&InboxSummaryParams { limit: Some(5) });
        roundtrip(&InboxSummaryParams { limit: None });
        roundtrip(&InboxItem {
            id: "msg-2026-01-01-test".to_string(),
            subject: "Test message".to_string(),
            from: "cascade".to_string(),
            priority: "high".to_string(),
            created: "2026-01-01T00:00:00Z".to_string(),
        });
        roundtrip(&InboxSummaryResult {
            items: vec![InboxItem {
                id: "msg-001".to_string(),
                subject: "Hello".to_string(),
                from: "system".to_string(),
                priority: "low".to_string(),
                created: "2026-06-01T12:00:00Z".to_string(),
            }],
        });
    }

    #[test]
    fn test_memory_read_roundtrip() {
        roundtrip(&MemoryReadParams {
            project: "/Volumes/X9/Sites/acamarata/cascade".to_string(),
            file: "decisions.md".to_string(),
        });
        roundtrip(&MemoryReadResult {
            content: "# Decisions\n\n- Use JSON-RPC 2.0\n".to_string(),
            path: "/Volumes/X9/Sites/acamarata/cascade/.claude/memory/decisions.md".to_string(),
        });
    }

    #[test]
    fn test_memory_write_roundtrip() {
        roundtrip(&MemoryWriteParams {
            project: "/Volumes/X9/Sites/acamarata/cascade".to_string(),
            file: "lessons.md".to_string(),
            content: "## Lessons\n\n- Always roundtrip-test serde types\n".to_string(),
        });
        roundtrip(&MemoryWriteResult {
            path: "/Volumes/X9/Sites/acamarata/cascade/.claude/memory/lessons.md".to_string(),
            bytes: 48,
        });
    }

    #[test]
    fn test_ping_roundtrip() {
        roundtrip(&PingParams {
            echo: Some("world".to_string()),
        });
        roundtrip(&PingParams { echo: None });
        roundtrip(&PingResult {
            pong: "world".to_string(),
        });
    }

    #[test]
    fn test_provider_quota_roundtrip() {
        roundtrip(&ProviderQuotaParams {});
        roundtrip(&ProviderEntry {
            name: "anthropic".to_string(),
            pct_used: 42.5,
            resets_at: Some("2026-06-07T00:00:00Z".to_string()),
        });
        roundtrip(&ProviderEntry {
            name: "openai".to_string(),
            pct_used: 0.0,
            resets_at: None,
        });
        roundtrip(&ProviderQuotaResult {
            providers: vec![ProviderEntry {
                name: "gemini".to_string(),
                pct_used: 15.3,
                resets_at: None,
            }],
        });
    }

    #[test]
    fn test_resolve_roundtrip() {
        roundtrip(&ResolveParams {
            tier: Some("gci".to_string()),
            format: Some("markdown".to_string()),
        });
        roundtrip(&ResolveParams {
            tier: None,
            format: None,
        });
        roundtrip(&ResolveResult {
            content: "# GCI\n\n...".to_string(),
            format: "markdown".to_string(),
            tier: "gci".to_string(),
        });
    }

    #[test]
    fn test_search_roundtrip() {
        roundtrip(&SearchParams {
            query: "how to configure rag".to_string(),
            limit: Some(10),
        });
        roundtrip(&SearchParams {
            query: "daemon socket path".to_string(),
            limit: None,
        });
        roundtrip(&SearchHit {
            id: "chunk-001".to_string(),
            score: 0.92,
            excerpt: "The daemon socket is at ~/.cascade/daemon.sock".to_string(),
            source: ".claude/docs/architecture.md".to_string(),
        });
        roundtrip(&SearchResult {
            hits: vec![SearchHit {
                id: "chunk-002".to_string(),
                score: 0.85,
                excerpt: "BGE-M3 embeddings are used for semantic search".to_string(),
                source: ".claude/docs/rag.md".to_string(),
            }],
        });
    }

    #[test]
    fn test_status_roundtrip() {
        roundtrip(&StatusParams {});
        roundtrip(&StatusResult {
            pid: 12345,
            uptime_secs: 3600,
            queue_depth: 0,
            rag_index_fresh: true,
            version: "0.1.0".to_string(),
            tcp_port: None,
        });
    }

    // ── JSON-RPC 2.0 spec compliance checks ─────────────────────────────────

    #[test]
    fn test_jsonrpc_version_serialises_as_string() {
        let json = serde_json::to_string(&JsonRpcVersion::V2_0).unwrap();
        assert_eq!(json, r#""2.0""#, "jsonrpc field must serialise as \"2.0\"");
    }

    #[test]
    fn test_request_id_null_serialises_correctly() {
        let json = serde_json::to_string(&RequestId::Null).unwrap();
        assert_eq!(json, "null");
    }

    #[test]
    fn test_protocol_version_constant() {
        assert_eq!(PROTOCOL_VERSION, 1);
    }

    #[test]
    fn test_error_codes() {
        assert_eq!(METHOD_NOT_FOUND, -32601);
        assert_eq!(INVALID_PARAMS, -32602);
        assert_eq!(INTERNAL_ERROR, -32603);
        assert_eq!(DAEMON_NOT_RUNNING, -32001);
        assert_eq!(AUTH_FAILED, -32002);
        assert_eq!(RESOURCE_NOT_FOUND, -32003);
    }

    // ── Schema validation (T-P2-E07-11) ────────────────────────────────────

    #[test]
    fn deserialize_request_rejects_unknown_field() {
        // PingParams has #[serde(deny_unknown_fields)]; an extra key must fail.
        let body = br#"{"jsonrpc":"2.0","id":1,"method":"ping","protocol_version":1,"params":{"echo":"hi","bogus":true}}"#;
        let err = deserialize_request::<PingParams>(body).unwrap_err();
        assert!(
            matches!(err, crate::error::IpcError::UnknownField(_)),
            "expected UnknownField, got {err:?}"
        );
    }

    #[test]
    fn deserialize_request_rejects_missing_field() {
        // Request<P> requires `method`; omit it to force a missing-field error.
        let body = br#"{"jsonrpc":"2.0","id":1,"protocol_version":1,"params":{"echo":"hi"}}"#;
        let err = deserialize_request::<PingParams>(body).unwrap_err();
        assert!(
            matches!(err, crate::error::IpcError::MissingField(_)),
            "expected MissingField, got {err:?}"
        );
    }

    #[test]
    fn deserialize_request_accepts_valid() {
        let body = br#"{"jsonrpc":"2.0","id":1,"method":"ping","protocol_version":1,"params":{"echo":"hi"}}"#;
        let req = deserialize_request::<PingParams>(body).expect("valid request must deserialize");
        assert_eq!(req.method, "ping");
    }

    // ── Field-value bounds (T-P2-E07-12) ───────────────────────────────────

    #[test]
    fn validate_resolve_params_rejects_unknown_tier() {
        let p = ResolveParams {
            tier: Some("evil".to_string()),
            format: None,
        };
        match validate_resolve_params(&p) {
            Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
                assert_eq!(field, "tier");
            }
            other => panic!("expected InvalidFieldValue{{tier}}, got {other:?}"),
        }
        // A valid tier passes.
        let ok = ResolveParams {
            tier: Some("gci".to_string()),
            format: None,
        };
        assert!(validate_resolve_params(&ok).is_ok());
    }

    #[test]
    fn validate_search_params_rejects_oversized_query() {
        let p = SearchParams {
            query: "x".repeat(MAX_QUERY_LEN + 1),
            limit: None,
        };
        match validate_search_params(&p) {
            Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
                assert_eq!(field, "query");
            }
            other => panic!("expected InvalidFieldValue{{query}}, got {other:?}"),
        }
        // Exactly at the cap is allowed.
        let ok = SearchParams {
            query: "x".repeat(MAX_QUERY_LEN),
            limit: None,
        };
        assert!(validate_search_params(&ok).is_ok());
    }

    #[test]
    fn validate_memory_write_params_rejects_traversal_and_oversize() {
        let traversal = MemoryWriteParams {
            project: "../../etc".to_string(),
            file: "lessons.md".to_string(),
            content: "ok".to_string(),
        };
        match validate_memory_write_params(&traversal) {
            Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
                assert_eq!(field, "project_path");
            }
            other => panic!("expected InvalidFieldValue{{project_path}}, got {other:?}"),
        }
        let oversize = MemoryWriteParams {
            project: "myproj".to_string(),
            file: "lessons.md".to_string(),
            content: "x".repeat(MAX_CONTENT_LEN + 1),
        };
        match validate_memory_write_params(&oversize) {
            Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
                assert_eq!(field, "content");
            }
            other => panic!("expected InvalidFieldValue{{content}}, got {other:?}"),
        }
        // A clean payload passes.
        let ok = MemoryWriteParams {
            project: "myproj".to_string(),
            file: "lessons.md".to_string(),
            content: "hello".to_string(),
        };
        assert!(validate_memory_write_params(&ok).is_ok());
    }
}
