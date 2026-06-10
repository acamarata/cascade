//! Cascade tier nomenclature constants and resolved-context types.
//!
//! Provides a unified nomenclature for the six cascade instruction tiers,
//! including display names, short names (acronyms), inbox naming rules,
//! default filesystem paths, and the [`ResolvedContext`] type produced by
//! the cascade-resolve engine.

use crate::hook::HookConfigEntry;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

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
/// # TOML shape
///
/// ```toml
/// instructions = "Always use snake_case identifiers."
/// rules = ["Never commit secrets", "Run tests before push"]
/// memory_paths = ["/home/user/.cascade/memory/decisions.md"]
/// task_paths   = ["~/.cascade/tasks/active.md"]
/// ```
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct TierConfig {
    /// Free-form instruction text for this tier.
    ///
    /// When absent, the resolver falls back to reading `CASCADE.md` from the
    /// same directory as the plain-text instructions value.
    pub instructions: Option<String>,

    /// Ordered list of rule snippets contributed by this tier.
    ///
    /// Rules from all tiers are **accumulated** (not replaced): GCI rules appear
    /// first in the merged output, PAC rules appear last.
    #[serde(default)]
    pub rules: Vec<String>,

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
/// | Field           | Merge rule                                              |
/// |-----------------|---------------------------------------------------------|
/// | `instructions`  | Highest-tier non-empty value wins (GCI beats PAC)      |
/// | `rules`         | Accumulated; GCI rules prepended (appear first)         |
/// | `memory_paths`  | Accumulated; GCI paths prepended                        |
/// | `task_paths`    | Accumulated; GCI paths prepended                        |
/// | `tier_sources`  | Map from [`TierName`] → resolved `.cascade/` dir path  |
/// | `resolved_at`   | ISO-8601 UTC timestamp of when resolution ran           |
///
/// This type is `Serialize + Deserialize` for forward-compatible IPC exposure
/// in E-03.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ResolvedContext {
    /// The winning instruction text (highest-tier non-empty value wins).
    pub instructions: String,

    /// All rules from all tiers, accumulated highest-tier-first.
    pub rules: Vec<String>,

    /// All memory file paths from all tiers, accumulated highest-tier-first.
    pub memory_paths: Vec<PathBuf>,

    /// All task file paths from all tiers, accumulated highest-tier-first.
    pub task_paths: Vec<PathBuf>,

    /// Map from each found tier to the `.cascade/` directory path that was
    /// resolved for it. A tier absent from this map was not found and was
    /// silently skipped. Ordered by `TierName`'s natural `BTreeMap` key order
    /// (GCI first since `GCI < PCI < ... < PAC` by `Ord`).
    pub tier_sources: BTreeMap<TierName, PathBuf>,

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
        assert!(ctx.tier_sources.is_empty());
    }

    #[test]
    fn tier_config_defaults_all_empty() {
        let cfg: TierConfig = toml::from_str("").expect("empty toml parses to default");
        assert!(cfg.instructions.is_none());
        assert!(cfg.rules.is_empty());
        assert!(cfg.memory_paths.is_empty());
        assert!(cfg.task_paths.is_empty());
    }
}
