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
use crate::error::{CascadeError, Result};
use crate::query_strategy::StrategyKind;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::PathBuf;

// ── Config schema version ─────────────────────────────────────────────────────

/// Monotonically-increasing schema version for runtime YAML/TOML state files
/// read from `.cascade/*.yaml` and `config.toml`.
///
/// Bump this constant whenever a breaking field change is required.
/// Files with `schema_version` absent deserialise to 0 via `#[serde(default)]`
/// and are always considered compatible (additive-only migration model).
pub const CONFIG_SCHEMA_VERSION: u32 = 1;

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
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum AiFolder {
    /// Use `.cascade/` (default).
    #[default]
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
///
/// # Schema versioning
///
/// `schema_version` defaults to `0` when absent (oldest compatible format).
/// Call [`CascadeConfig::validate_schema_version`] after deserialisation to
/// reject configs written by a future binary that this version cannot safely read.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct CascadeConfig {
    /// Schema version of this config file.
    ///
    /// Absent in files written before versioning was introduced; `serde(default)`
    /// maps those to `0`, which passes validation.  Bump [`CONFIG_SCHEMA_VERSION`]
    /// when adding breaking field changes.
    pub schema_version: u32,

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

    /// Override for the PCI (Personal Cascade Instructions) root directory.
    ///
    /// When `None`, defaults to `~/Downloads`. Set this in `config.toml` to
    /// point Cascade at a different personal workspace (e.g. an external drive):
    ///
    /// ```toml
    /// personal_dir = "/Volumes/Data/personal"
    /// ```
    ///
    /// The effective value is always read via [`CascadeConfig::effective_personal_dir`].
    #[serde(default)]
    pub personal_dir: Option<PathBuf>,

    /// Override for the APC (All-Projects Cascade) root directories.
    ///
    /// When empty (the default), Cascade uses `[~/Sites]`. Supply one or more
    /// paths to index multiple project roots or to point at a non-standard
    /// location:
    ///
    /// ```toml
    /// projects_dirs = ["/Volumes/X9/Sites", "/Users/me/work"]
    /// ```
    ///
    /// The `CASCADE_APC_PATH` environment variable still wins over this field
    /// (only the first element of `projects_dirs` is compared when the env var
    /// is absent). Use [`CascadeConfig::effective_projects_dirs`] to get the
    /// resolved list.
    #[serde(default)]
    pub projects_dirs: Vec<PathBuf>,

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
            enabled: true,
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
            enabled: true,
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

// ── CascadeConfig impl ────────────────────────────────────────────────────────

impl CascadeConfig {
    /// Reject a config file whose `schema_version` exceeds `current_max`.
    ///
    /// A file written by a *future* binary may use fields this binary does not
    /// understand; loading it silently could corrupt state. Return a typed error
    /// so callers can surface a clear "please upgrade Cascade" message.
    ///
    /// Files with `schema_version = 0` (absent in older files) always pass.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::SchemaMismatch`] when
    /// `self.schema_version > current_max`.
    pub fn validate_schema_version(&self, current_max: u32) -> Result<()> {
        if self.schema_version > current_max {
            return Err(CascadeError::SchemaMismatch {
                expected: current_max,
                found: self.schema_version,
            });
        }
        Ok(())
    }

    /// Returns the effective personal workspace root (PCI tier).
    ///
    /// Precedence: `personal_dir` config field → `~/Downloads`.
    ///
    /// WHY: keeps the PCI default (`~/Downloads`) DRY across the codebase while
    /// letting users override it in `config.toml` for non-standard setups.
    pub fn effective_personal_dir(&self) -> PathBuf {
        self.personal_dir.clone().unwrap_or_else(|| {
            crate::tiers::home_dir().unwrap_or_default().join("Downloads")
        })
    }

    /// Returns the effective list of all-projects roots (APC tier).
    ///
    /// Precedence: `projects_dirs` config field (when non-empty) → `[~/Sites]`.
    ///
    /// WHY: keeps the APC default (`~/Sites`) DRY while supporting users with
    /// projects on external drives or multiple project roots.
    pub fn effective_projects_dirs(&self) -> Vec<PathBuf> {
        if self.projects_dirs.is_empty() {
            vec![crate::tiers::home_dir().unwrap_or_default().join("Sites")]
        } else {
            self.projects_dirs.clone()
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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn schema_version_absent_passes() {
        // A file with no schema_version key deserialises to 0 and must pass.
        let toml = r#"
[provider]
kind = "bge-m3"
"#;
        let cfg: CascadeConfig = toml::from_str(toml).expect("parse");
        assert_eq!(cfg.schema_version, 0);
        cfg.validate_schema_version(CONFIG_SCHEMA_VERSION)
            .expect("version 0 must be accepted");
    }

    #[test]
    fn schema_version_current_passes() {
        let mut cfg = CascadeConfig::default();
        cfg.schema_version = CONFIG_SCHEMA_VERSION;
        cfg.validate_schema_version(CONFIG_SCHEMA_VERSION)
            .expect("current version must be accepted");
    }

    #[test]
    fn schema_version_future_is_rejected() {
        let mut cfg = CascadeConfig::default();
        cfg.schema_version = CONFIG_SCHEMA_VERSION + 1;
        let err = cfg
            .validate_schema_version(CONFIG_SCHEMA_VERSION)
            .expect_err("future version must be rejected");
        assert!(
            matches!(
                err,
                crate::error::CascadeError::SchemaMismatch {
                    expected,
                    found
                } if expected == CONFIG_SCHEMA_VERSION && found == CONFIG_SCHEMA_VERSION + 1
            ),
            "unexpected error variant: {err}"
        );
    }

    // ── effective_personal_dir ────────────────────────────────────────────────

    /// When personal_dir is None, effective_personal_dir() returns ~/Downloads.
    #[test]
    fn test_effective_personal_dir_default() {
        let cfg = CascadeConfig::default();
        assert!(cfg.personal_dir.is_none());
        let result = cfg.effective_personal_dir();
        // Must end with "Downloads" — exact home depends on environment.
        assert!(
            result.ends_with("Downloads"),
            "default personal_dir must end with 'Downloads', got: {result:?}"
        );
    }

    /// When personal_dir is Some(path), effective_personal_dir() returns that path.
    #[test]
    fn test_effective_personal_dir_custom() {
        let mut cfg = CascadeConfig::default();
        let custom = PathBuf::from("/custom/personal");
        cfg.personal_dir = Some(custom.clone());
        assert_eq!(cfg.effective_personal_dir(), custom);
    }

    // ── effective_projects_dirs ───────────────────────────────────────────────

    /// When projects_dirs is empty, effective_projects_dirs() returns [~/Sites].
    #[test]
    fn test_effective_projects_dirs_default() {
        let cfg = CascadeConfig::default();
        assert!(cfg.projects_dirs.is_empty());
        let result = cfg.effective_projects_dirs();
        assert_eq!(result.len(), 1, "default must yield exactly one entry");
        assert!(
            result[0].ends_with("Sites"),
            "default projects_dirs[0] must end with 'Sites', got: {:?}",
            result[0]
        );
    }

    /// When projects_dirs is non-empty, effective_projects_dirs() returns those paths.
    #[test]
    fn test_effective_projects_dirs_custom() {
        let mut cfg = CascadeConfig::default();
        let paths = vec![
            PathBuf::from("/Volumes/X9/Sites"),
            PathBuf::from("/Users/me/work"),
        ];
        cfg.projects_dirs = paths.clone();
        assert_eq!(cfg.effective_projects_dirs(), paths);
    }
}
