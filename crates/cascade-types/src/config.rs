//! Unified configuration schema for the Cascade AI context framework.
//!
//! This module resolves CRIT-5 from the Pass 1 architecture review by defining
//! a single canonical schema for all Cascade configuration. Crates that need
//! to read or write config MUST import from here; no ad-hoc `serde` structs
//! in other crates.
//!
//! # Format-per-kind policy
//!
//! | Format | Use |
//! |--------|-----|
//! | **TOML** | Human-edited config: `config.toml`, project-level settings |
//! | **YAML** | Declarative specs: tier maps, security policy files, templates |
//! | **JSON** | Machine-written state: daemon state, plugin state, quota |
//!
//! # Global vs project split
//!
//! - `~/.cascade/config.toml` — user-global; persists across projects.
//! - `.cascade/config.toml` — project-scoped; overrides global for this project.
//!
//! The resolver merges them: project config wins on conflict.

use crate::cascade_tier::CascadeTier;
use crate::embedding_provider::ProviderKind;
use crate::query_strategy::StrategyKind;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

// ── CascadeConfig ─────────────────────────────────────────────────────────────

/// The top-level configuration struct.
///
/// Deserialised from `config.toml` at the applicable scope (global or project).
/// All fields are optional with sensible defaults; an empty `config.toml` is valid.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct CascadeConfig {
    /// Provider (embedding model) settings.
    pub provider: ProviderConfig,

    /// RAG pipeline settings.
    pub rag: RagConfig,

    /// MCP server settings.
    pub mcp: McpConfig,

    /// Plugin settings.
    pub plugins: PluginConfig,

    /// Daemon process settings.
    pub daemon: DaemonConfig,

    /// Which cascade tiers are active. An empty vec means all tiers are active.
    pub active_tiers: Vec<CascadeTier>,

    /// Maximum size of the merged cascade text in bytes.
    /// Default: 512 KiB.
    pub max_cascade_size_bytes: Option<usize>,

    /// Arbitrary extra keys for forward compatibility. Keys starting with `_`
    /// are reserved for internal use.
    #[serde(flatten)]
    pub extra: HashMap<String, toml::Value>,
}

// ── ProviderConfig ────────────────────────────────────────────────────────────

/// Embedding provider configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct ProviderConfig {
    /// The embedding model to use.
    pub kind: ProviderKind,

    /// Model variant override (e.g. `"text-embedding-3-large"`).
    /// When absent, the provider uses its default model.
    pub model: Option<String>,

    /// Maximum batch size for embedding API calls.
    pub batch_size: usize,

    /// Request timeout in seconds.
    pub timeout_secs: u64,

    /// Key ID in the `KeyStorage` backend that holds the API key.
    /// For local models, leave `None`.
    pub key_id: Option<String>,

    /// API base URL override. Useful for local proxies or self-hosted endpoints.
    pub base_url: Option<String>,
}

impl Default for ProviderConfig {
    fn default() -> Self {
        Self {
            kind: ProviderKind::BgeM3,
            model: None,
            batch_size: 32,
            timeout_secs: 30,
            key_id: None,
            base_url: None,
        }
    }
}

// ── RagConfig ─────────────────────────────────────────────────────────────────

/// RAG (Retrieval-Augmented Generation) pipeline configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct RagConfig {
    /// Enable or disable the RAG pipeline entirely.
    pub enabled: bool,

    /// The query strategy to apply.
    pub strategy: StrategyKind,

    /// Number of chunks to retrieve in the first pass.
    pub top_k: usize,

    /// Number of chunks to keep after reranking.
    /// Must be ≤ `top_k`. `None` means skip the rerank pass.
    pub rerank_top_k: Option<usize>,

    /// Minimum score threshold for the vector search pass.
    pub min_score: Option<f32>,

    /// Paths (glob patterns) to index. Relative to the project root.
    /// Default: `[".cascade/**/*.md"]`
    pub index_paths: Vec<String>,

    /// Paths to exclude from indexing (glob patterns).
    pub exclude_paths: Vec<String>,

    /// Target chunk size in tokens.
    pub chunk_size: usize,

    /// Chunk overlap in tokens.
    pub chunk_overlap: usize,

    /// Directory where the vector index is persisted.
    /// Default: `.cascade/index/`
    pub index_dir: Option<std::path::PathBuf>,

    /// Number of SQLite shards for the dense vector index.
    ///
    /// Each document is assigned to shard `fnv1a(doc_id) % shard_count`.
    /// KNN fan-outs query all shards in parallel and merge by score.
    ///
    /// Changing this value on an existing index requires a full re-index.
    /// Default: 4.
    pub shard_count: usize,
}

impl Default for RagConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            strategy: StrategyKind::HybridRrf,
            top_k: 10,
            rerank_top_k: Some(5),
            min_score: None,
            index_paths: vec![".cascade/**/*.md".into()],
            exclude_paths: vec![],
            chunk_size: 512,
            chunk_overlap: 64,
            index_dir: None,
            shard_count: 4,
        }
    }
}

// ── McpConfig ─────────────────────────────────────────────────────────────────

/// MCP (Model Context Protocol) server configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct McpConfig {
    /// Enable the built-in MCP server.
    pub enabled: bool,

    /// The Unix socket path for the MCP server.
    /// Defaults to `$HOME/.cascade/mcp.sock`.
    pub socket_path: Option<std::path::PathBuf>,

    /// Maximum number of concurrent MCP connections.
    pub max_connections: usize,

    /// Request timeout in seconds.
    pub request_timeout_secs: u64,
}

impl Default for McpConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            socket_path: None,
            max_connections: 10,
            request_timeout_secs: 60,
        }
    }
}

// ── PluginConfig ──────────────────────────────────────────────────────────────

/// Plugin subsystem configuration.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct PluginConfig {
    /// Enable the plugin subsystem.
    pub enabled: bool,

    /// Directory to search for plugin WASM files.
    /// Default: `~/.cascade/plugins/`
    pub plugin_dir: Option<std::path::PathBuf>,

    /// Per-plugin settings keyed by plugin name.
    pub plugins: HashMap<String, toml::Value>,
}

// WHY: Default is derived above — no manual impl needed.

// ── DaemonConfig ─────────────────────────────────────────────────────────────

/// Cascade daemon (`cascaded`) configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct DaemonConfig {
    /// Enable the daemon process.
    pub enabled: bool,

    /// Log level for the daemon (`trace`, `debug`, `info`, `warn`, `error`).
    pub log_level: String,

    /// Debounce interval for file-system events in milliseconds.
    pub debounce_ms: u64,

    /// Maximum number of file-watcher events queued before dropping.
    pub event_queue_size: usize,

    /// Idle timeout in seconds before the daemon shuts itself down.
    /// `None` means run indefinitely.
    pub idle_timeout_secs: Option<u64>,
}

impl Default for DaemonConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            log_level: "warn".into(),
            debounce_ms: 50,
            event_queue_size: 256,
            idle_timeout_secs: None,
        }
    }
}

// ── Config key constants ──────────────────────────────────────────────────────

/// Dot-separated config key constants for use with `cascade config get/set`.
pub mod keys {
    pub const PROVIDER_KIND: &str = "provider.kind";
    pub const PROVIDER_MODEL: &str = "provider.model";
    pub const PROVIDER_BATCH_SIZE: &str = "provider.batch_size";
    pub const PROVIDER_KEY_ID: &str = "provider.key_id";
    pub const RAG_ENABLED: &str = "rag.enabled";
    pub const RAG_STRATEGY: &str = "rag.strategy";
    pub const RAG_TOP_K: &str = "rag.top_k";
    pub const RAG_CHUNK_SIZE: &str = "rag.chunk_size";
    pub const MCP_ENABLED: &str = "mcp.enabled";
    pub const DAEMON_LOG_LEVEL: &str = "daemon.log_level";
    pub const DAEMON_DEBOUNCE_MS: &str = "daemon.debounce_ms";
}
