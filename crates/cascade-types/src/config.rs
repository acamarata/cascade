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

// ── HarnessConfig ─────────────────────────────────────────────────────────────

/// The `[harness]` table in `cascade.toml` — per-harness configuration block.
///
/// Consumed by `provide_harness_context` to tailor cascade context injection
/// for each AI harness (Claude Code, OpenCode, Codex, etc.).
///
/// # TOML shape
///
/// ```toml
/// [harness]
/// model_preference   = "claude-opus-4-5"
/// enabled_tools      = ["bash", "read", "write", "mcp_tool"]
/// max_context_tokens = 100000
///
/// [harness.mcp]
/// transport = "unix"
/// socket    = "~/.cascade/mcp.sock"
/// ```
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(default, rename_all = "snake_case")]
pub struct HarnessConfig {
    /// Preferred model identifier for this project (harness-agnostic string).
    /// When absent, the harness's own default is used.
    pub model_preference: Option<String>,

    /// Explicit list of tool identifiers that should be enabled for this project.
    /// An empty list means "use the harness default tool set".
    #[serde(default)]
    pub enabled_tools: Vec<String>,

    /// Maximum number of tokens to include in the injected cascade context.
    /// `None` means no project-level cap (harness default applies).
    pub max_context_tokens: Option<u32>,

    /// MCP connection settings for this harness instance.
    #[serde(default)]
    pub mcp: HarnessMcpConfig,
}

/// MCP-specific overrides within the `[harness]` block.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(default, rename_all = "snake_case")]
pub struct HarnessMcpConfig {
    /// Transport type: `"unix"` (default), `"http"`, or `"stdio"`.
    pub transport: Option<String>,

    /// Unix socket path override. Defaults to `~/.cascade/mcp.sock`.
    pub socket: Option<std::path::PathBuf>,

    /// HTTP base URL for the `"http"` transport.
    pub url: Option<String>,

    /// Request timeout in seconds for MCP calls from this harness.
    pub timeout_secs: Option<u64>,
}

// ── AiFolder ──────────────────────────────────────────────────────────────────

/// Which AI folder name Cascade uses to store its working files.
///
/// On a fresh setup, Cascade checks for an already-existing AI folder in this
/// order: `.claude` → `.codex` → `.opencode` → `.cascade`. If one is found it
/// is auto-adopted (non-destructive). If none exists, `.cascade` is created.
///
/// The choice is persisted to `config.toml` as `ai_folder`.
///
/// # Variants
/// - `Cascade` — `.cascade` (default)
/// - `Claude`  — `.claude`
/// - `Codex`   — `.codex`
/// - `Custom`  — any other folder name supplied by the user
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum AiFolder {
    /// Use `.cascade/` (default).
    Cascade,
    /// Use `.claude/` (Claude Code convention).
    Claude,
    /// Use `.codex/` (OpenAI Codex convention).
    Codex,
    /// Use `.opencode/` (OpenCode convention).
    Opencode,
    /// Arbitrary folder name.
    Custom(String),
}

impl Default for AiFolder {
    fn default() -> Self {
        AiFolder::Cascade
    }
}

impl AiFolder {
    /// The folder name this variant resolves to (without a leading dot, as
    /// that is part of the identifier stored on disk).
    ///
    /// Returns the dotted folder name (e.g. `.cascade`).
    pub fn folder_name(&self) -> &str {
        match self {
            AiFolder::Cascade => ".cascade",
            AiFolder::Claude => ".claude",
            AiFolder::Codex => ".codex",
            AiFolder::Opencode => ".opencode",
            AiFolder::Custom(name) => name.as_str(),
        }
    }

    /// Try to parse an `AiFolder` from a user-supplied string (e.g. from the
    /// CLI argument `cascade folder set .claude`).
    ///
    /// Leading dots are accepted and stripped before comparison so both
    /// `.claude` and `claude` are valid inputs.
    pub fn from_str_loose(s: &str) -> Self {
        let name = s.trim_start_matches('.');
        match name {
            "cascade" => AiFolder::Cascade,
            "claude" => AiFolder::Claude,
            "codex" => AiFolder::Codex,
            "opencode" => AiFolder::Opencode,
            other => AiFolder::Custom(format!(".{}", other)),
        }
    }
}

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

    /// Per-harness configuration (model prefs, enabled tools, MCP settings).
    /// Consumed by `provide_harness_context`; canonical type defined in cascade-types.
    pub harness: HarnessConfig,

    /// Policy rules for this tier.
    /// Merged with rules from other tiers into an effective `PolicySet` at runtime.
    pub policy: crate::policy::PolicyTableConfig,

    /// Which AI folder name Cascade uses (`.cascade`, `.claude`, `.codex`, or custom).
    ///
    /// Written by `cascade folder set <name>` and read by every path-resolution
    /// helper. Defaults to `cascade` (→ `.cascade`).
    pub ai_folder: AiFolder,

    /// GFP (Gemini Free Pool) multi-key provisioning policy.
    pub gfp: GfpConfig,

    /// Which cascade tiers are active. An empty vec means all tiers are active.
    pub active_tiers: Vec<CascadeTier>,

    /// Maximum size of the merged cascade text in bytes.
    /// Default: 512 KiB.
    pub max_cascade_size_bytes: Option<usize>,

    /// Experimental / beta features.
    ///
    /// All flags in this block default to `false`. Enabling them may violate
    /// third-party Terms of Service, may break without warning, and is not
    /// covered by the Cascade stability guarantee.
    pub experimental: ExperimentalConfig,

    /// Arbitrary extra keys for forward compatibility. Keys starting with `_`
    /// are reserved for internal use.
    #[serde(flatten)]
    pub extra: HashMap<String, toml::Value>,
}

// ── ExperimentalConfig ────────────────────────────────────────────────────────

/// `[experimental]` — opt-in beta features.
///
/// **ALL flags default to `false`.** Enabling any flag is an explicit opt-in;
/// Cascade will never activate experimental behaviour automatically.
///
/// # TOML example
///
/// ```toml
/// [experimental]
/// # WARNING: May violate the Anthropic Claude Code Terms of Service.
/// # Read .github/docs/cc-api-proxy-beta.md before enabling.
/// cc_api_proxy = false   # DEFAULT: off — change to true only after reading the risk doc
/// ```
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(default, rename_all = "snake_case")]
pub struct ExperimentalConfig {
    /// **EXPERIMENTAL — OFF BY DEFAULT.**
    ///
    /// When `true`, `cascade ccapi start` will launch an HTTP+SSE bridge that
    /// drives the interactive Claude Code CLI (`claude`) to expose a
    /// `/v1/messages`-compatible API endpoint.
    ///
    /// **Risk:** This wraps the Claude Code interactive terminal process via a
    /// PTY/pipe. Anthropic's Terms of Service permit the subscription tier only
    /// for interactive use. Using this bridge for automated/programmatic access
    /// may violate those terms and result in account suspension.
    ///
    /// **Maintenance risk:** Claude Code's terminal output format is not a stable
    /// API. Any CC release can break the bridge without notice.
    ///
    /// **Security note:** The bridge listens on a local port (default 7190) with
    /// no authentication by default. Bind it to 127.0.0.1 only.
    ///
    /// Read `.github/docs/cc-api-proxy-beta.md` for full details before enabling.
    ///
    /// Default: `false` (disabled).
    pub cc_api_proxy: bool,
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

// ── GfpConfig ─────────────────────────────────────────────────────────────────

/// `[gfp]` — Gemini Free Pool multi-key provisioning policy.
///
/// Controls how many GCP API keys `full_auto_multi` may create per account,
/// how long to wait between key creations, and whether automatic ceiling
/// expansion is permitted.
///
/// **Conservative by default.** These defaults protect real Google accounts
/// from ToS / abuse-detection bans. Do NOT raise `max_keys_per_account`
/// without understanding the implications.
///
/// # TOML example
///
/// ```toml
/// [gfp]
/// max_keys_per_account = 3
/// cooldown_secs        = 30
/// auto_max             = false  # SAFE DEFAULT — never raise without explicit opt-in
/// ```
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(default, rename_all = "snake_case")]
pub struct GfpConfig {
    /// Hard ceiling on the number of GCP API keys created per Google account.
    ///
    /// `full_auto_multi` will never create more than this many keys regardless
    /// of the `count` requested. Conservative default: 3.
    pub max_keys_per_account: u32,

    /// Seconds to wait between successive key creations for the same account.
    ///
    /// Reduces the risk of hitting Google's abuse-detection heuristics.
    /// Conservative default: 30 seconds.
    pub cooldown_secs: u64,

    /// When `false` (default), the `count` argument to `full_auto_multi` is
    /// always capped at `max_keys_per_account`. When `true`, `count` may
    /// exceed the ceiling — the caller must explicitly opt in.
    ///
    /// **SAFE DEFAULT: false.** Never set this to `true` without reading the
    /// Google Cloud Terms of Service on API key limits and project quotas.
    pub auto_max: bool,
}

impl Default for GfpConfig {
    fn default() -> Self {
        Self {
            max_keys_per_account: 3,
            cooldown_secs: 30,
            auto_max: false,
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
    pub const AI_FOLDER: &str = "ai_folder";
}
