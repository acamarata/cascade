//! Accounts registry types for `~/.cascade/accounts.json`.
//!
//! Purpose: defines the canonical data model for the multi-account fleet
//! registry. Covers account families, access methods, roles, task classes,
//! and the full [`AccountsRegistry`] struct written to disk.
//!
//! Inputs:  populated by `cascade-core::accounts_store::default_registry()`
//!          on first `cascade accounts detect`; updated by daemon ticks.
//! Outputs: serialised to `~/.cascade/accounts.json`; read by CLI and daemon.
//! Constraints:
//!   - Zero runtime I/O in this module (types only).
//!   - All enums use `serde(rename_all = "kebab-case")` for stable JSON keys.
//!   - `PartialEq` enables test assertions and change-detection diffs.

use serde::{Deserialize, Serialize};

// ── Schema version ────────────────────────────────────────────────────────────

/// Monotonically-increasing schema version for `~/.cascade/accounts.json`.
///
/// Bump this constant when a breaking field change is required.
pub const ACCOUNTS_SCHEMA_VERSION: u32 = 1;

// ── Enums ─────────────────────────────────────────────────────────────────────

/// Account family identifier.
///
/// Purpose: groups accounts by their upstream provider/subscription type.
/// Used in routing decisions and CLI display grouping.
/// Constraints: serialised as kebab-case strings in JSON.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Hash)]
#[serde(rename_all = "kebab-case")]
pub enum AccountFamily {
    /// Anthropic Claude subscription (Claude Max, Pro, etc.).
    Claude,
    /// OpenAI subscription (Pro, etc.).
    Openai,
    /// Google AI subscription (AI Ultra, etc.).
    Google,
    /// OpenCode run-based subscription.
    Opencode,
    /// z.ai GLM Coding Plan, dispatched through Claude Code with GLM endpoint env.
    #[serde(alias = "glm")]
    Zai,
    /// Gemini Free Pool — key-based, no CLI.
    Gfp,
}

/// Access method — how the account is invoked.
///
/// Purpose: routing layer uses this to select the correct execution path
/// for a given account (native CLI, PTY wrapper, API key pool, etc.).
/// Constraints: serialised as kebab-case strings in JSON.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Hash)]
#[serde(rename_all = "kebab-case")]
pub enum AccessMethod {
    /// `claude` CLI (primary Claude Code account).
    NativeCc,
    /// `smithers claude -p` PTY wrapper (extra CC accounts).
    SmithersClaudeP,
    /// `codex` CLI execution.
    CodexCli,
    /// `agy -p` CLI (Google AI).
    AgyCli,
    /// `opencode run` execution.
    OpencodeRun,
    /// GFP round-robin key pool (API-key based, no CLI).
    GfpKeypool,
}

/// Account role in routing decisions.
///
/// Purpose: controls drain order and reservation status for the scheduler.
/// Primary accounts are consumed last; pooled accounts are drained first.
/// Constraints: serialised as kebab-case strings in JSON.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Hash)]
#[serde(rename_all = "kebab-case")]
pub enum AccountRole {
    /// Reserved primary — used only when pooled accounts are exhausted.
    PrimaryT0,
    /// Pooled — drain before primary.
    Pooled,
    /// GFP free tier — cheapest, highest drain priority.
    Free,
    /// Generic fallback when no preferred account matches.
    Fallback,
}

/// Task class for routing hints in the model matrix.
///
/// Purpose: allows the routing layer to prefer specific account+model
/// combinations for different workload types (interactive vs bulk vs sensitive).
/// Constraints: serialised as kebab-case strings in JSON.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Hash)]
#[serde(rename_all = "kebab-case")]
pub enum TaskClass {
    /// Real-time interactive chat sessions.
    InteractiveChat,
    /// High-throughput batch agent work.
    BulkExecution,
    /// Pre/post-prompt hooks, taxonomy classification, lightweight tasks.
    Grunt,
    /// Taxonomy and classification jobs.
    Taxonomy,
    /// Adversarial code review passes.
    AdversarialCr,
    /// Final gate / synthesis (Opus-tier).
    FinalGate,
    /// PII / VA / health data — hard firewall, restricted routing.
    Sensitive,
    /// Non-interactive background work.
    Background,
    /// Post-prompt processing hooks.
    PostPrompt,
}

// ── Structs ───────────────────────────────────────────────────────────────────

/// A single account in the Cascade fleet registry.
///
/// Purpose: canonical record for one subscription account, capturing all
/// metadata needed for routing, drain-order decisions, and quota linking.
/// Inputs:  populated by `accounts_store::default_registry()` and updated
///          by `cascade accounts detect` and daemon ticks.
/// Outputs: serialised as one element of `AccountsRegistry::accounts`.
/// Constraints:
///   - `exhaustion_priority` lower = drained first; 255 = reserved last.
///   - `key_count` is 0 for non-GFP accounts.
///   - `quota_account_id` links to `QuotaStore::AccountEntry::account_id`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Account {
    /// Stable identifier for this account (e.g. `"claude-acc1"`).
    pub id: String,
    /// Provider family.
    pub family: AccountFamily,
    /// Human-readable subscription tier (e.g. `"anthropic-max"`).
    pub subscription: String,
    /// Ordered list of access methods available for this account.
    pub access_methods: Vec<AccessMethod>,
    /// Routing role (primary, pooled, free, fallback).
    pub role: AccountRole,
    /// Drain order: lower values are consumed first; 255 = reserved last.
    pub exhaustion_priority: u8,
    /// Model IDs available via this account.
    pub models: Vec<String>,
    /// Whether the required CLI binary was found in PATH at last detection.
    pub cli_available: bool,
    /// Number of API keys in pool (GFP only; 0 for all other families).
    pub key_count: u32,
    /// Links to `QuotaStore::AccountEntry::account_id` for usage join.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub quota_account_id: Option<String>,
    /// Free-form notes displayed in `cascade accounts list`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

/// One route in the model routing matrix.
///
/// Purpose: pairs an account ID with the access method used to reach it,
/// forming a (model → route) edge in the routing graph.
/// Constraints: `account_id` must match an `Account::id` in the same registry.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ModelRoute {
    /// Account identifier (matches `Account::id`).
    pub account_id: String,
    /// Access method for reaching this account.
    pub method: AccessMethod,
}

/// Model routing matrix entry.
///
/// Purpose: maps one model ID to its ordered list of available routes and
/// documents which task classes it serves best.
/// Inputs:  built by `accounts_store::default_registry()`.
/// Outputs: used by the routing layer to select account+method for a request.
/// Constraints: `available_via` is ordered best-first; first viable route wins.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ModelMatrixEntry {
    /// Canonical model ID (e.g. `"claude-sonnet-4-6"`).
    pub model_id: String,
    /// Available routes ordered best-first.
    pub available_via: Vec<ModelRoute>,
    /// Task classes this model is best suited for.
    pub best_for: Vec<TaskClass>,
    /// Tier label (e.g. `"T0"`, `"T1"`, `"T2"`, `"T3-cheap"`).
    pub tier: String,
    /// Provider family that supplies this model.
    pub family: AccountFamily,
    /// Optional notes for CLI display.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

/// The full accounts registry persisted to `~/.cascade/accounts.json`.
///
/// Purpose: single source of truth for all fleet accounts, their access
/// methods, and the model routing matrix. Read by CLI and daemon; written
/// by `cascade accounts detect` and refreshed by daemon ticks.
/// Inputs:  produced by `accounts_store::default_registry()` or read from disk.
/// Outputs: serialised to `~/.cascade/accounts.json`.
/// Constraints:
///   - `schema_version` must equal [`ACCOUNTS_SCHEMA_VERSION`] when reading.
///   - `updated_at` is an ISO-8601 RFC 3339 timestamp.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AccountsRegistry {
    /// Schema version — must equal [`ACCOUNTS_SCHEMA_VERSION`].
    pub schema_version: u32,
    /// ISO-8601 timestamp of the last write.
    pub updated_at: String,
    /// All registered accounts.
    pub accounts: Vec<Account>,
    /// Model → route routing matrix.
    pub model_matrix: Vec<ModelMatrixEntry>,
}
