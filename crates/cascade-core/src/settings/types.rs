//! # cascade_core::settings::types
//!
//! Core settings structs for the Cascade configuration system.
//!
//! ## Purpose
//!
//! Defines [`CascadeSettings`], the top-level struct persisted to
//! `~/.cascade/settings.json`. All E-07 configuration surfaces are
//! represented as typed nested structs with `#[serde(default)]` so that
//! loading an older file never panics.
//!
//! ## Inputs
//!
//! Constructed from `serde_json::from_reader` over `settings.json`, or from
//! `Default::default()` when no file exists yet.
//!
//! ## Outputs
//!
//! Serialised back to `settings.json` via `serde_json::to_writer_pretty`.
//!
//! ## Constraints
//!
//! - Every field carries `#[serde(default)]` so loading an old file that
//!   predates a new field never panics.
//! - API key fields are `Option<String>` (never required) and default to
//!   `None`. Vault encryption is deferred to P4.
//! - Unknown fields are preserved via `#[serde(flatten)]` on an extra
//!   `serde_json::Map` so future-schema fields round-trip without loss.
//!
//! ## SPORT
//!
//! MASTER-RUST-CRATES.md — cascade-core: settings module (T-P3-E07-14)

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

fn default_schema_version() -> String {
    "2".to_string()
}

// ── Library ───────────────────────────────────────────────────────────────────

/// Default harness targets for new library items.
///
/// Controls which AI harnesses receive newly indexed documents.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct LibrarySettings {
    /// List of harness identifiers that receive new items by default.
    /// Empty means all harnesses receive them.
    #[serde(default)]
    pub default_harness_targets: Vec<String>,

    /// Preserve unknown fields for forward-compatibility.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Context ───────────────────────────────────────────────────────────────────

/// Context resolution overrides.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct ContextSettings {
    /// Override the default `~/Sites/` root used for context resolution.
    /// Null = use system default.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sites_root: Option<String>,

    /// Override the default personal-workspace root used by Personal-scope
    /// chat and the personal vault. Null = system default (`~/Downloads`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub personal_root: Option<String>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Memory ────────────────────────────────────────────────────────────────────

/// Chat-history memory consent settings.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct MemorySettings {
    /// Explicit user consent to persist Personal-scope chat history to the
    /// daemon's long-term memory store (`personal` namespace). Default: false.
    /// When false, Personal-scope chat is localStorage-only — the daemon is
    /// never contacted, matching the same firewall the `opt_in` flag guards
    /// server-side (see cascade-rag memory::namespace).
    #[serde(default)]
    pub personal_chat_sync: bool,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Project Map ───────────────────────────────────────────────────────────────

/// Project-map resolution overrides.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct ProjectMapSettings {
    /// Override the phase root path (`{project}/.claude/phases/`).
    /// Null = use system default.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub phase_root: Option<String>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Providers ─────────────────────────────────────────────────────────────────

/// Per-provider API keys and routing defaults.
///
/// All key fields are `Option<String>` — absent = not configured.
/// Vault encryption is deferred to P4.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct ProvidersSettings {
    /// Anthropic API key (plain string; P4 will encrypt).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub anthropic: Option<String>,

    /// OpenAI API key.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub openai: Option<String>,

    /// Google API keys (multiple accounts supported).
    #[serde(default)]
    pub google: Vec<String>,

    /// OpenAI Codex API key.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex: Option<String>,

    /// Default routing target harness slug, e.g. `"anthropic"`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub default_routing: Option<String>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Gemini Pool ───────────────────────────────────────────────────────────────

/// Gemini proxy pool configuration.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct GeminiPoolSettings {
    /// API keys in the rotation pool.
    #[serde(default)]
    pub keys: Vec<String>,

    /// TCP port the Gemini proxy daemon listens on. Default: 3761.
    #[serde(default = "default_gemini_proxy_port")]
    pub proxy_port: u16,

    /// Whether the Gemini proxy pool is enabled.
    #[serde(default)]
    pub enabled: bool,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

fn default_gemini_proxy_port() -> u16 {
    3761
}

impl Default for GeminiPoolSettings {
    fn default() -> Self {
        Self {
            keys: Vec::new(),
            proxy_port: default_gemini_proxy_port(),
            enabled: false,
            extra: HashMap::new(),
        }
    }
}

// ── Harness Bridges ───────────────────────────────────────────────────────────

/// Paths to external AI harness configuration files.
///
/// All paths are optional; absent = use the harness's own defaults.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct HarnessBridgesSettings {
    /// Path to the Claude Code (`cc`) config file.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cc_config_path: Option<String>,

    /// Path to the OpenCode (`oc`) config file.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub oc_config_path: Option<String>,

    /// Path to the OpenAI Codex config file.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_config_path: Option<String>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Hooks ─────────────────────────────────────────────────────────────────────

/// A single hook definition.
///
/// Hook execution is handled by the P2/E-02 engine; this struct only stores
/// the configuration.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct HookDefinition {
    /// Unique slug for this hook, e.g. `"on-session-start"`.
    pub id: String,

    /// The lifecycle event this hook fires on.
    pub event: String,

    /// Shell command or script path to execute.
    pub command: String,

    /// Whether this hook is currently active. Default: true.
    #[serde(default = "default_true")]
    pub enabled: bool,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

fn default_true() -> bool {
    true
}

// ── Scheduled Tasks ───────────────────────────────────────────────────────────

/// A single scheduled task entry.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ScheduledTaskEntry {
    /// Unique task identifier.
    pub id: String,

    /// Human-readable label.
    pub label: String,

    /// Cron expression (e.g. `"0 */6 * * *"` = every 6 h).
    pub cron: String,

    /// Command or agent prompt to execute on schedule.
    pub command: String,

    /// Whether this task is active. Default: true.
    #[serde(default = "default_true")]
    pub enabled: bool,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Plugins ───────────────────────────────────────────────────────────────────

/// Plugin management settings.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct PluginsSettings {
    /// Slugs of plugins that are currently enabled.
    #[serde(default)]
    pub enabled: Vec<String>,

    /// Per-plugin configuration keyed by plugin slug.
    /// Values are arbitrary JSON objects.
    #[serde(default)]
    pub config: HashMap<String, serde_json::Value>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Widgets ───────────────────────────────────────────────────────────────────

/// Display geometry for a single widget.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct WidgetGeometry {
    /// Screen X position in logical pixels.
    #[serde(default)]
    pub x: i32,

    /// Screen Y position in logical pixels.
    #[serde(default)]
    pub y: i32,

    /// Widget width in logical pixels.
    #[serde(default = "default_widget_width")]
    pub width: u32,

    /// Widget height in logical pixels.
    #[serde(default = "default_widget_height")]
    pub height: u32,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

fn default_widget_width() -> u32 {
    320
}
fn default_widget_height() -> u32 {
    200
}

impl Default for WidgetGeometry {
    fn default() -> Self {
        Self {
            x: 0,
            y: 0,
            width: default_widget_width(),
            height: default_widget_height(),
            extra: HashMap::new(),
        }
    }
}

/// Widget display settings keyed by widget slug.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct WidgetsSettings {
    /// Per-widget geometry keyed by widget slug.
    #[serde(default)]
    pub positions: HashMap<String, WidgetGeometry>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── MCP Servers ───────────────────────────────────────────────────────────────

/// A single MCP server definition.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct McpServerEntry {
    /// Human-readable server name.
    pub name: String,

    /// Executable command (full path or in `$PATH`).
    pub command: String,

    /// Arguments passed to the command.
    #[serde(default)]
    pub args: Vec<String>,

    /// Environment variables injected into the server process.
    #[serde(default)]
    pub env: HashMap<String, String>,

    /// Whether this server is active. Default: true.
    #[serde(default = "default_true")]
    pub enabled: bool,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Vault Display ─────────────────────────────────────────────────────────────

/// Controls how vault credentials are shown in the UI.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct VaultDisplaySettings {
    /// If true, show masked previews (e.g. `sk-••••••AB`).
    /// If false, values are fully hidden. Default: false.
    #[serde(default)]
    pub show_masked: bool,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Telemetry ─────────────────────────────────────────────────────────────────

/// Anonymous usage telemetry preferences.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
#[derive(Default)]
pub struct TelemetrySettings {
    /// Opt in to anonymous telemetry. Default: false.
    #[serde(default)]
    pub enabled: bool,

    /// Telemetry endpoint URL override. Null = use default.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub endpoint: Option<String>,

    /// Preserve unknown fields.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// ── Top-level CascadeSettings ─────────────────────────────────────────────────

/// Top-level application settings.
///
/// Persisted to `~/.cascade/settings.json`. Every field uses
/// `#[serde(default)]` so that upgrading the binary to a version that adds
/// new fields never fails to parse an existing file.
///
/// Schema version history:
/// - `"1"`: baseline (providers + gemini_pool as opaque blobs, T-P3-E07-00).
/// - `"2"`: full typed schema (all E-07 sections, T-P3-E07-14).
///
/// Unknown top-level keys are preserved in `extra` (flatten) so that a v2
/// binary loading a hypothetical v3 file does not silently drop new fields.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CascadeSettings {
    /// Schema version — incremented on incompatible changes.
    /// Absent in old files is treated as `"1"` by `#[serde(default)]`.
    #[serde(default = "default_schema_version")]
    pub schema_version: String,

    /// Library defaults.
    #[serde(default)]
    pub library: LibrarySettings,

    /// Context resolution overrides.
    #[serde(default)]
    pub context: ContextSettings,

    /// Chat-history memory consent settings.
    #[serde(default)]
    pub memory: MemorySettings,

    /// Project-map resolution overrides.
    #[serde(default)]
    pub project_map: ProjectMapSettings,

    /// Per-provider API keys and routing defaults.
    #[serde(default)]
    pub providers: ProvidersSettings,

    /// Gemini proxy pool configuration.
    #[serde(default)]
    pub gemini_pool: GeminiPoolSettings,

    /// Harness bridge configuration file paths.
    #[serde(default)]
    pub harness_bridges: HarnessBridgesSettings,

    /// Lifecycle hook definitions.
    #[serde(default)]
    pub hooks: Vec<HookDefinition>,

    /// Scheduled task definitions.
    #[serde(default)]
    pub scheduled_tasks: Vec<ScheduledTaskEntry>,

    /// Plugin management settings.
    #[serde(default)]
    pub plugins: PluginsSettings,

    /// Widget display geometry per widget slug.
    #[serde(default)]
    pub widgets: WidgetsSettings,

    /// MCP server definitions.
    #[serde(default)]
    pub mcp_servers: Vec<McpServerEntry>,

    /// Vault display preferences.
    #[serde(default)]
    pub vault_display: VaultDisplaySettings,

    /// Anonymous telemetry preferences.
    #[serde(default)]
    pub telemetry: TelemetrySettings,

    /// Unknown top-level fields preserved for forward-compatibility.
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

impl Default for CascadeSettings {
    fn default() -> Self {
        Self {
            schema_version: default_schema_version(),
            library: LibrarySettings::default(),
            context: ContextSettings::default(),
            memory: MemorySettings::default(),
            project_map: ProjectMapSettings::default(),
            providers: ProvidersSettings::default(),
            gemini_pool: GeminiPoolSettings::default(),
            harness_bridges: HarnessBridgesSettings::default(),
            hooks: Vec::new(),
            scheduled_tasks: Vec::new(),
            plugins: PluginsSettings::default(),
            widgets: WidgetsSettings::default(),
            mcp_servers: Vec::new(),
            vault_display: VaultDisplaySettings::default(),
            telemetry: TelemetrySettings::default(),
            extra: HashMap::new(),
        }
    }
}
