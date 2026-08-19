// FROZEN — schema version 1. See parent module for protocol contract notes.

//! Method-specific params and result structs, in alphabetical order by method name.

use serde::{Deserialize, Serialize};

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
    /// Working directory to resolve from. The daemon resolves cascade tiers
    /// relative to this path. Must be an existing directory.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cwd: Option<std::path::PathBuf>,
    /// Target tier slug, e.g. `"gci"`, `"pci"`, `"prc"`. `None` returns the full cascade.
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
    /// Whether the RAG indexer is currently paused (e.g. because its source
    /// volume was unmounted). `false` means indexing is active.
    /// Added schema v1.2 — safe additive; old daemons omit the field → `false`.
    #[serde(default)]
    pub index_paused: bool,
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
