//! Cascade tier nomenclature constants and resolved-context types.
//!
//! Provides a unified nomenclature for the six cascade instruction tiers,
//! including display names, short names (acronyms), inbox naming rules,
//! default filesystem paths, and the [`ResolvedContext`] type produced by
//! the cascade-resolve engine.
//!
//! # Always-loaded vs on-demand rules
//!
//! Rules contributed by a tier can be marked `on_demand = true` in `config.toml`.
//! On-demand rules are **not** inlined into harness files; they appear as
//! `->` pointer comments so that context-budget stays bounded. The resolver
//! separates them into [`ResolvedContext::on_demand_rules`].
//!
//! # Canonical variables (`vars`)
//!
//! Every tier may declare a `[vars]` table in `config.toml` that maps
//! namespaced keys to string values:
//!
//! ```toml
//! [vars]
//! "cascade.model.t1" = "claude-opus-4-5"
//! "infra.prod_ip"    = "1.2.3.4"
//! ```
//!
//! Variables from all tiers are merged with **lower (more-specific) tier
//! overrides higher (less-specific) tier on key collision** — the opposite
//! of the `instructions` precedence, but matches the `model_registry` pattern
//! where the most-specific scope knows best.
//!
//! After merging, the resolver performs token substitution: every occurrence
//! of `${ns.key}` in merged instruction text (and on-demand rule bodies) is
//! replaced with the corresponding value from the merged vars map.  Unknown
//! tokens are left verbatim; `$${ns.key}` is an escape for a literal
//! `${ns.key}`.  Substitution runs AFTER `@import` expansion.
//!
//! # Back-compatibility
//!
//! The `rules` field in `config.toml` supports two shapes that both deserialise
//! into [`Rule`]:
//!
//! ```toml
//! # bare string (old shape — always-loaded):
//! rules = ["Never commit secrets", "Run tests before push"]
//!
//! # table shape (new):
//! [[rules]]
//! text = "Load auth rules before implementing login"
//! on_demand = true
//! load_when = "auth"
//!
//! [[rules]]
//! text = "Always use snake_case"
//! # on_demand defaults to false → always-loaded
//! ```
//!
//! Old configs with plain strings continue to deserialise without any change.
//! Existing configs without a `[vars]` table parse without any change.

use crate::hook::HookConfigEntry;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

// ── Vars ──────────────────────────────────────────────────────────────────────

/// A map of namespaced canonical variable keys to their string values.
///
/// Keys follow the `namespace.sub_key` convention (e.g. `cascade.model.t1`,
/// `infra.prod_ip`).  Any string is technically valid as a key, but the
/// dotted-namespace style is the convention so that `${ns.key}` tokens in
/// instruction text are readable and searchable.
///
/// This is a type alias for [`BTreeMap`] so serialisation preserves key order.
pub type VarsMap = BTreeMap<String, String>;

// ── Rule ─────────────────────────────────────────────────────────────────────

/// A single rule snippet contributed by a cascade tier.
///
/// Deserialises from either a bare `String` (back-compat) or a TOML table:
///
/// ```toml
/// # Old shape — always-loaded rule:
/// rules = ["Never commit secrets"]
///
/// # New shape — on-demand rule:
/// [[rules]]
/// text = "Load auth docs before implementing login"
/// on_demand = true
/// load_when = "auth"
/// ```
///
/// When deserialised from a bare string, `on_demand` defaults to `false`
/// and `load_when` defaults to `None`, preserving full back-compatibility.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum Rule {
    /// A bare string (old config format). Always loaded into context.
    Plain(String),
    /// A structured rule with explicit metadata.
    Structured(RuleEntry),
}

impl Rule {
    /// The rule text.
    pub fn text(&self) -> &str {
        match self {
            Self::Plain(s) => s,
            Self::Structured(e) => &e.text,
        }
    }

    /// Returns `true` if this rule should be deferred to on-demand loading.
    pub fn on_demand(&self) -> bool {
        match self {
            Self::Plain(_) => false,
            Self::Structured(e) => e.on_demand,
        }
    }

    /// The condition string that triggers loading (e.g. `"auth"`, `"database"`).
    ///
    /// `None` for always-loaded rules and for on-demand rules without a
    /// specific trigger (they are still deferred, just without a named trigger).
    pub fn load_when(&self) -> Option<&str> {
        match self {
            Self::Plain(_) => None,
            Self::Structured(e) => e.load_when.as_deref(),
        }
    }
}

/// The structured form of a [`Rule`] entry in `config.toml`.
///
/// Used when `[[rules]]` array-of-tables syntax is preferred over bare strings.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct RuleEntry {
    /// The rule text body.
    pub text: String,

    /// When `true`, this rule is NOT inlined into harness files; it is emitted
    /// as a `-> name (load_when: …)` pointer comment instead.
    ///
    /// Default: `false` (always loaded).
    #[serde(default)]
    pub on_demand: bool,

    /// A condition keyword that tells the harness when to load this rule.
    ///
    /// Examples: `"auth"`, `"database"`, `"deployment"`.
    /// When absent, the rule is still on-demand but without a named trigger.
    #[serde(default)]
    pub load_when: Option<String>,
}

/// A resolved on-demand rule entry carrying pointer metadata for harness rendering.
///
/// Stored in [`ResolvedContext::on_demand_rules`]. Harness renderers emit these
/// as `-> <text> (load_when: <condition>)` comment lines rather than inlining
/// the full rule body.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct OnDemandRule {
    /// The rule text (used as the pointer label).
    pub text: String,

    /// The trigger condition, if any.
    pub load_when: Option<String>,

    /// Which tier contributed this on-demand rule.
    pub source_tier: String,
}

// ── TierName ──────────────────────────────────────────────────────────────────

/// The six cascade instruction tiers with nomenclature support.
///
/// This enum mirrors `CascadeTier` but uses the canonical Cascade-native names
/// (GCI / PCI / APC / PPC / PRC / PAC as uppercase acronyms) and adds
/// `default_path()` for resolution. Ordered from highest precedence (GCI) to
/// lowest (PAC) — the `Ord` implementation reflects this: `GCI < PCI < ... < PAC`
/// where "less than" means "higher precedence."
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum TierName {
    /// Global Cascade Instructions — `~/.cascade/`.
    /// Applies to all tools and all projects. Highest precedence.
    GCI,

    /// Personal Cascade Instructions — e.g. `~/Downloads/.cascade/`.
    /// Applies to personal/individual scope.
    PCI,

    /// All-Projects Cascade — e.g. `~/Sites/.cascade/`.
    /// Applies to all coding projects in a named group.
    APC,

    /// Per-Project Cascade — `<project-root>/.cascade/`.
    /// Applies to all repos within a multi-repo project.
    PPC,

    /// Per-Repo Cascade — `<repo-root>/.cascade/`.
    /// Applies to a single git repository.
    PRC,

    /// Per-App Cascade — `<repo>/<app>/.cascade/`.
    /// Applies to a single application within a multi-app repo.
    PAC,
}

impl TierName {
    /// Returns the short acronym (3 letters).
    pub fn short_name(&self) -> &'static str {
        match self {
            Self::GCI => "GCI",
            Self::PCI => "PCI",
            Self::APC => "APC",
            Self::PPC => "PPC",
            Self::PRC => "PRC",
            Self::PAC => "PAC",
        }
    }

    /// Returns a human-readable display name for the tier.
    pub fn display_name(&self) -> &'static str {
        match self {
            Self::GCI => "Global Cascade Instructions",
            Self::PCI => "Personal Cascade Instructions",
            Self::APC => "All-Projects Cascade",
            Self::PPC => "Per-Project Cascade",
            Self::PRC => "Per-Repo Cascade",
            Self::PAC => "Per-App Cascade",
        }
    }

    /// Returns the inbox name for this tier, if applicable.
    ///
    /// Only `PPC` (Per-Project Cascade) has an inbox, named `PPCi`.
    /// All other tiers return `None`.
    pub fn inbox_name(&self) -> Option<&'static str> {
        match self {
            Self::PPC => Some("PPCi"),
            _ => None,
        }
    }

    /// Returns all tiers in precedence order (highest to lowest).
    pub fn all_in_precedence_order() -> &'static [TierName] {
        &[
            Self::GCI,
            Self::PCI,
            Self::APC,
            Self::PPC,
            Self::PRC,
            Self::PAC,
        ]
    }

    /// Returns the default `.cascade/` directory path for this tier.
    ///
    /// - **GCI** → `~/.cascade`  (expands `~` via `$HOME`)
    /// - **PCI** → `~/Downloads/.cascade`
    /// - **APC** → `~/Sites/.cascade` (overridable via `CASCADE_APC_PATH` env var)
    /// - **PPC / PRC / PAC** → `{project_root}/.cascade`
    ///
    /// Returns `None` only when `$HOME` / `$USERPROFILE` cannot be determined
    /// (unusual sandbox environments).
    ///
    /// Callers that have a [`CascadeConfig`] available should prefer
    /// [`default_path_with_config`] so that `personal_dir` and `projects_dirs`
    /// overrides from `config.toml` are honoured.
    ///
    /// [`CascadeConfig`]: crate::config::CascadeConfig
    /// [`default_path_with_config`]: TierName::default_path_with_config
    pub fn default_path(&self, project_root: &Path) -> Option<PathBuf> {
        let home = home_dir()?;
        match self {
            Self::GCI => Some(home.join(".cascade")),
            Self::PCI => Some(home.join("Downloads").join(".cascade")),
            Self::APC => {
                // Honour env override; default to ~/Sites/.cascade (macOS convention).
                let apc = std::env::var("CASCADE_APC_PATH")
                    .ok()
                    .map(PathBuf::from)
                    .unwrap_or_else(|| home.join("Sites").join(".cascade"));
                Some(apc)
            }
            Self::PPC | Self::PRC | Self::PAC => Some(project_root.join(".cascade")),
        }
    }

    /// Config-aware variant of [`default_path`].
    ///
    /// Identical to [`default_path`] except that:
    ///
    /// - **PCI** root comes from [`CascadeConfig::effective_personal_dir`] instead
    ///   of the hard-coded `~/Downloads`.
    /// - **APC** root comes from the first entry of
    ///   [`CascadeConfig::effective_projects_dirs`] instead of the hard-coded
    ///   `~/Sites`. `CASCADE_APC_PATH` env var still takes highest priority.
    ///
    /// Pass a reference to the merged [`CascadeConfig`] loaded from
    /// `~/.cascade/config.toml` (and optionally project-level overrides).
    ///
    /// [`default_path`]: TierName::default_path
    /// [`CascadeConfig`]: crate::config::CascadeConfig
    pub fn default_path_with_config(
        &self,
        project_root: &Path,
        config: &crate::config::CascadeConfig,
    ) -> Option<PathBuf> {
        match self {
            Self::GCI => {
                let home = home_dir()?;
                Some(home.join(".cascade"))
            }
            Self::PCI => {
                // WHY: personal_dir overrides ~/Downloads for non-standard setups.
                Some(config.effective_personal_dir().join(".cascade"))
            }
            Self::APC => {
                // Env var wins over config (keeps existing override contract).
                if let Ok(env_path) = std::env::var("CASCADE_APC_PATH") {
                    return Some(PathBuf::from(env_path));
                }
                // First entry from config (defaults to ~/Sites when unset).
                let root = config
                    .effective_projects_dirs()
                    .into_iter()
                    .next()
                    .unwrap_or_else(|| home_dir().unwrap_or_default().join("Sites"));
                Some(root.join(".cascade"))
            }
            Self::PPC | Self::PRC | Self::PAC => Some(project_root.join(".cascade")),
        }
    }
}

impl std::fmt::Display for TierName {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.short_name())
    }
}

// ── ResolvedContext ───────────────────────────────────────────────────────────

/// A per-tier TOML configuration fragment.
///
/// Deserialised from `{tier_dir}/config.toml`. All fields are optional; a
/// missing field leaves the corresponding merged value unchanged.
///
/// # TOML shape — back-compatible
///
/// Old format (bare strings) still works:
/// ```toml
/// instructions = "Always use snake_case identifiers."
/// rules = ["Never commit secrets", "Run tests before push"]
/// memory_paths = ["/home/user/.cascade/memory/decisions.md"]
/// task_paths   = ["~/.cascade/tasks/active.md"]
/// ```
///
/// New format with on-demand rules:
/// ```toml
/// instructions = "Always use snake_case identifiers."
/// rules = ["Never commit secrets"]
///
/// [[rules]]
/// text = "Load auth policy before implementing login flows"
/// on_demand = true
/// load_when = "auth"
/// ```
///
/// Both formats can be mixed in the same `config.toml`.
/// Any entry without `on_demand = true` behaves identically to the old format.
///
/// The optional `[models]` table maps tiers to model IDs
/// (see [`crate::model_registry::ModelOverrides`]).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct TierConfig {
    /// Free-form instruction text for this tier.
    ///
    /// When absent, the resolver falls back to reading `CASCADE.md` from the
    /// same directory as the plain-text instructions value.
    pub instructions: Option<String>,

    /// Ordered list of rule entries contributed by this tier.
    ///
    /// Each entry is either a plain `String` (always-loaded, back-compat) or a
    /// [`Rule`] table with optional `on_demand` and `load_when` fields.
    ///
    /// Rules from all tiers are **accumulated** (not replaced): GCI rules appear
    /// first in the merged output, PAC rules appear last.
    #[serde(default)]
    pub rules: Vec<Rule>,

    /// Filesystem paths to memory files (decisions.md, lessons.md, etc.)
    /// contributed by this tier.  Accumulated across tiers.
    #[serde(default)]
    pub memory_paths: Vec<PathBuf>,

    /// Filesystem paths to task files (active.md, queue.md, etc.)
    /// contributed by this tier. Accumulated across tiers.
    #[serde(default)]
    pub task_paths: Vec<PathBuf>,

    /// Hook definitions contributed by this tier.
    ///
    /// Maps to the `[[hooks]]` array in `config.toml`. Accumulated across all
    /// tiers; GCI hooks are loaded first so they appear first in `list_all()`.
    ///
    /// ```toml
    /// [[hooks]]
    /// name = "log-regen"
    /// event = "RegenComplete"
    /// action.type = "LogMessage"
    /// action.level = "info"
    /// action.message = "Cascade regenerated"
    /// enabled = true
    /// ```
    #[serde(default)]
    pub hooks: Vec<HookConfigEntry>,

    /// Optional per-tier model-ID overrides.
    ///
    /// Maps to the `[models]` table in `config.toml`. When present, the
    /// resolver merges these into the workspace [`ModelRegistry`].
    ///
    /// [`ModelRegistry`]: crate::model_registry::ModelRegistry
    #[serde(default)]
    pub models: Option<crate::model_registry::ModelOverrides>,

    /// Canonical variable definitions for this tier.
    ///
    /// Maps to the `[vars]` table in `config.toml`:
    ///
    /// ```toml
    /// [vars]
    /// "cascade.model.t1" = "claude-opus-4-5"
    /// "infra.prod_ip"    = "1.2.3.4"
    /// ```
    ///
    /// Absent tiers (no `[vars]` table) contribute an empty map.  Merging
    /// across tiers follows a lower-tier-overrides-higher rule: the most
    /// specific scope (PAC) wins on key collision.
    ///
    /// Serde `default` ensures existing configs without a `[vars]` table
    /// continue to parse without error.
    #[serde(default)]
    pub vars: VarsMap,
}

/// The fully resolved cascade context produced by
/// [`cascade_core::cascade_resolve::resolve_cascade`].
///
/// Aggregates configuration from all discovered tiers with
/// higher-tier-wins-on-conflict semantics for scalar fields and
/// accumulate-all semantics for vector fields.
///
/// # Merge semantics
///
/// | Field              | Merge rule                                                        |
/// |--------------------|-------------------------------------------------------------------|
/// | `instructions`     | Highest-tier non-empty value wins (GCI beats PAC)                 |
/// | `rules`            | Always-loaded rules; GCI rules prepended (appear first)           |
/// | `on_demand_rules`  | Deferred rules; GCI first; NOT inlined into harness files         |
/// | `memory_paths`     | Accumulated; GCI paths prepended                                  |
/// | `task_paths`       | Accumulated; GCI paths prepended                                  |
/// | `tier_sources`     | Map from [`TierName`] → resolved `.cascade/` dir path             |
/// | `model_registry`   | Built-in defaults, overridden by lowest tier that sets them       |
/// | `vars`             | Lower tier (PAC) overrides higher tier (GCI) on key collision     |
/// | `resolved_at`      | ISO-8601 UTC timestamp of when resolution ran                     |
///
/// This type is `Serialize + Deserialize` for forward-compatible IPC exposure
/// in E-03.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ResolvedContext {
    /// The winning instruction text (highest-tier non-empty value wins).
    pub instructions: String,

    /// Always-loaded rules from all tiers, accumulated highest-tier-first.
    ///
    /// These are inlined verbatim into harness instruction files. To keep
    /// context budgets bounded, prefer moving large reference rules to
    /// [`on_demand_rules`] with an appropriate `load_when` trigger.
    pub rules: Vec<String>,

    /// On-demand rules that must NOT be inlined into harness files.
    ///
    /// Harness renderers emit these as pointer comment lines:
    /// `-> <text> (load_when: <condition>)`. The full text is carried for
    /// tooling that wants to display or validate on-demand rules.
    pub on_demand_rules: Vec<OnDemandRule>,

    /// All memory file paths from all tiers, accumulated highest-tier-first.
    pub memory_paths: Vec<PathBuf>,

    /// All task file paths from all tiers, accumulated highest-tier-first.
    pub task_paths: Vec<PathBuf>,

    /// Map from each found tier to the `.cascade/` directory path that was
    /// resolved for it. A tier absent from this map was not found and was
    /// silently skipped. Ordered by `TierName`'s natural `BTreeMap` key order
    /// (GCI first since `GCI < PCI < ... < PAC` by `Ord`).
    pub tier_sources: BTreeMap<TierName, PathBuf>,

    /// Model registry built from built-in defaults, then successively overridden
    /// by each tier's `[models]` table (lowest tier wins on conflict, i.e. PAC
    /// beats GCI for model selection, since the most-specific scope knows best).
    pub model_registry: crate::model_registry::ModelRegistry,

    /// Merged canonical variable map.
    ///
    /// Built from all tiers' `[vars]` tables.  Lower-tier values override
    /// higher-tier values on key collision (same precedence as `model_registry`:
    /// PAC knows best).
    ///
    /// Token substitution (`${ns.key}`) in merged instruction text uses this
    /// map.  `cascade doctor` also uses it to detect hardcoded literal values
    /// that should be interpolations instead.
    #[serde(default)]
    pub vars: VarsMap,

    /// ISO-8601 UTC timestamp of when resolution ran, e.g. `"2026-06-02T12:34:56Z"`.
    pub resolved_at: String,
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Returns the user's home directory via `$HOME` (Unix) or `USERPROFILE` (Windows).
///
/// Returns `None` only in heavily sandboxed environments where neither variable
/// is set.
pub(crate) fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .ok()
        .map(PathBuf::from)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_tier_name_variants() {
        assert_eq!(TierName::GCI.short_name(), "GCI");
        assert_eq!(TierName::PCI.short_name(), "PCI");
        assert_eq!(TierName::APC.short_name(), "APC");
        assert_eq!(TierName::PPC.short_name(), "PPC");
        assert_eq!(TierName::PRC.short_name(), "PRC");
        assert_eq!(TierName::PAC.short_name(), "PAC");
    }

    #[test]
    fn test_inbox_name_ppc() {
        assert_eq!(TierName::PPC.inbox_name(), Some("PPCi"));
    }

    #[test]
    fn test_inbox_name_others() {
        assert_eq!(TierName::GCI.inbox_name(), None);
        assert_eq!(TierName::PCI.inbox_name(), None);
        assert_eq!(TierName::APC.inbox_name(), None);
        assert_eq!(TierName::PRC.inbox_name(), None);
        assert_eq!(TierName::PAC.inbox_name(), None);
    }

    #[test]
    fn test_display_names() {
        assert_eq!(TierName::GCI.display_name(), "Global Cascade Instructions");
        assert_eq!(
            TierName::PCI.display_name(),
            "Personal Cascade Instructions"
        );
        assert_eq!(TierName::APC.display_name(), "All-Projects Cascade");
        assert_eq!(TierName::PPC.display_name(), "Per-Project Cascade");
        assert_eq!(TierName::PRC.display_name(), "Per-Repo Cascade");
        assert_eq!(TierName::PAC.display_name(), "Per-App Cascade");
    }

    #[test]
    fn test_serialization_round_trip() {
        for tier in TierName::all_in_precedence_order() {
            let json = serde_json::to_string(tier).expect("serialize");
            let parsed: TierName = serde_json::from_str(&json).expect("deserialize");
            assert_eq!(&parsed, tier);
        }
    }

    #[test]
    fn test_all_in_precedence_order() {
        let tiers = TierName::all_in_precedence_order();
        assert_eq!(tiers.len(), 6);
        for window in tiers.windows(2) {
            assert!(
                window[0] < window[1],
                "Precedence order must be strictly ascending"
            );
        }
    }

    #[test]
    fn test_default_path_gci_ends_with_dot_cascade() {
        let root = PathBuf::from("/tmp/fake-project");
        if let Some(path) = TierName::GCI.default_path(&root) {
            assert!(
                path.ends_with(".cascade"),
                "GCI default path must end with .cascade, got: {path:?}"
            );
        }
        // If HOME is not set, the test is vacuously satisfied — no panic expected.
    }

    #[test]
    fn test_default_path_project_scoped() {
        let root = PathBuf::from("/tmp/my-project");
        for tier in &[TierName::PPC, TierName::PRC, TierName::PAC] {
            let path = tier.default_path(&root).expect("should return a path");
            assert_eq!(
                path,
                PathBuf::from("/tmp/my-project/.cascade"),
                "{tier} default path should be project_root/.cascade"
            );
        }
    }

    #[test]
    fn resolved_context_default_is_empty() {
        let ctx = ResolvedContext::default();
        assert!(ctx.instructions.is_empty());
        assert!(ctx.rules.is_empty());
        assert!(ctx.on_demand_rules.is_empty());
        assert!(ctx.tier_sources.is_empty());
    }

    #[test]
    fn tier_config_defaults_all_empty() {
        let cfg: TierConfig = toml::from_str("").expect("empty toml parses to default");
        assert!(cfg.instructions.is_none());
        assert!(cfg.rules.is_empty());
        assert!(cfg.memory_paths.is_empty());
        assert!(cfg.task_paths.is_empty());
        assert!(cfg.models.is_none());
        assert!(cfg.vars.is_empty(), "vars must default to empty map");
    }

    // ── vars: back-compat — no [vars] table ──────────────────────────────────

    /// Existing config.toml without a [vars] section must parse without error.
    ///
    /// WHY: back-compatibility is a hard requirement — every field is serde
    /// `default`, so an absent `[vars]` must yield an empty map.
    #[test]
    fn vars_absent_is_empty_map() {
        let toml_str = r#"instructions = "Old config, no vars.""#;
        let cfg: TierConfig = toml::from_str(toml_str).expect("old config must parse");
        assert!(cfg.vars.is_empty(), "absent [vars] must yield empty map");
    }

    // ── vars: basic key=value parsing ────────────────────────────────────────

    /// A `[vars]` table with two entries parses into the expected map.
    #[test]
    fn vars_parse_key_value() {
        let toml_str = r#"
[vars]
"cascade.model.t1" = "claude-opus-4-5"
"infra.prod_ip"    = "1.2.3.4"
"#;
        let cfg: TierConfig = toml::from_str(toml_str).expect("[vars] must parse");
        assert_eq!(cfg.vars.get("cascade.model.t1"), Some(&"claude-opus-4-5".to_string()));
        assert_eq!(cfg.vars.get("infra.prod_ip"), Some(&"1.2.3.4".to_string()));
    }

    // ── vars: round-trip serialisation ───────────────────────────────────────

    /// TierConfig with vars round-trips through serde_json without data loss.
    #[test]
    fn vars_round_trip_json() {
        let mut cfg = TierConfig::default();
        cfg.vars.insert("ns.key".to_string(), "value".to_string());
        let json = serde_json::to_string(&cfg).expect("serialise");
        let back: TierConfig = serde_json::from_str(&json).expect("deserialise");
        assert_eq!(back.vars.get("ns.key"), Some(&"value".to_string()));
    }

    // ── Rule back-compat: bare string still deserialises ─────────────────────

    /// Old config.toml with plain string rules must still deserialise.
    ///
    /// WHY: back-compatibility is a hard requirement — existing configs must
    /// not break after the Rule struct is introduced.
    #[test]
    fn rule_bare_string_back_compat() {
        let toml_str = r#"rules = ["Never commit secrets", "Run tests before push"]"#;
        let cfg: TierConfig = toml::from_str(toml_str).expect("old rule format must parse");
        assert_eq!(cfg.rules.len(), 2);
        assert_eq!(cfg.rules[0].text(), "Never commit secrets");
        assert!(!cfg.rules[0].on_demand());
        assert_eq!(cfg.rules[1].text(), "Run tests before push");
        assert!(!cfg.rules[1].on_demand());
    }

    // ── Rule structured form: on_demand = true ────────────────────────────────

    /// Structured rule with on_demand = true deserialises correctly.
    #[test]
    fn rule_structured_on_demand() {
        let toml_str = r#"
[[rules]]
text = "Load auth docs before implementing login"
on_demand = true
load_when = "auth"

[[rules]]
text = "Always use snake_case"
"#;
        let cfg: TierConfig = toml::from_str(toml_str).expect("structured rule must parse");
        assert_eq!(cfg.rules.len(), 2);

        let r0 = &cfg.rules[0];
        assert_eq!(r0.text(), "Load auth docs before implementing login");
        assert!(r0.on_demand());
        assert_eq!(r0.load_when(), Some("auth"));

        let r1 = &cfg.rules[1];
        assert_eq!(r1.text(), "Always use snake_case");
        assert!(!r1.on_demand());
        assert!(r1.load_when().is_none());
    }

    // ── Rule mixed format (plain + structured) ────────────────────────────────

    /// A config can mix plain string rules with structured rules.
    #[test]
    fn rule_mixed_plain_and_structured() {
        // TOML does not allow mixing inline array items with [[array]] tables in
        // the same key. Instead, use all [[rules]] tables for the mixed case.
        let toml_str = r#"
[[rules]]
text = "plain rule"

[[rules]]
text = "deferred rule"
on_demand = true
load_when = "deploy"
"#;
        let cfg: TierConfig = toml::from_str(toml_str).expect("mixed rules must parse");
        assert_eq!(cfg.rules.len(), 2);
        assert!(!cfg.rules[0].on_demand());
        assert!(cfg.rules[1].on_demand());
        assert_eq!(cfg.rules[1].load_when(), Some("deploy"));
    }

    // ── default_path_with_config ──────────────────────────────────────────────

    /// Serializes tests that mutate/depend on the process-global CASCADE_APC_PATH
    /// env var, so they don't race under the parallel test runner.
    static APC_ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

    /// CASCADE_APC_PATH env var wins over config.projects_dirs in
    /// default_path_with_config for the APC tier.
    #[test]
    fn test_apc_env_override_wins() {
        let _env_guard = APC_ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        // Temporarily set the env var; restore it after the test.
        let env_key = "CASCADE_APC_PATH";
        let custom_env = "/tmp/env-apc-override";
        // Guard: save previous value and restore on test exit.
        let prev = std::env::var(env_key).ok();
        std::env::set_var(env_key, custom_env);

        let mut config = crate::config::CascadeConfig::default();
        // Even if config has a different projects_dirs, env var must win.
        config.projects_dirs = vec![PathBuf::from("/should/not/be/used")];

        let root = PathBuf::from("/tmp/fake-project");
        let result = TierName::APC
            .default_path_with_config(&root, &config)
            .expect("APC must return a path");

        // Restore env state before any assert (panic safety).
        match prev {
            Some(v) => std::env::set_var(env_key, v),
            None => std::env::remove_var(env_key),
        }

        assert_eq!(
            result,
            PathBuf::from(custom_env),
            "CASCADE_APC_PATH env var must win over config.projects_dirs"
        );
    }

    /// When CASCADE_APC_PATH is unset and config.projects_dirs is set,
    /// default_path_with_config uses config.projects_dirs[0].
    #[test]
    fn test_apc_config_projects_dirs_used_when_no_env() {
        let _env_guard = APC_ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let env_key = "CASCADE_APC_PATH";
        // Ensure the env var is NOT set for this test.
        let prev = std::env::var(env_key).ok();
        std::env::remove_var(env_key);

        let mut config = crate::config::CascadeConfig::default();
        config.projects_dirs = vec![PathBuf::from("/custom/projects")];

        let root = PathBuf::from("/tmp/fake-project");
        let result = TierName::APC
            .default_path_with_config(&root, &config)
            .expect("APC must return a path");

        // Restore env state before any assert.
        match prev {
            Some(v) => std::env::set_var(env_key, v),
            None => std::env::remove_var(env_key),
        }

        assert_eq!(
            result,
            PathBuf::from("/custom/projects/.cascade"),
            "config.projects_dirs[0] must be used when CASCADE_APC_PATH is unset"
        );
    }

    /// PCI tier uses config.effective_personal_dir() in default_path_with_config.
    #[test]
    fn test_pci_with_config_custom_personal_dir() {
        let mut config = crate::config::CascadeConfig::default();
        config.personal_dir = Some(PathBuf::from("/custom/personal"));

        let root = PathBuf::from("/tmp/fake-project");
        let result = TierName::PCI
            .default_path_with_config(&root, &config)
            .expect("PCI must return a path");

        assert_eq!(
            result,
            PathBuf::from("/custom/personal/.cascade"),
            "PCI must use config.personal_dir when set"
        );
    }
}
