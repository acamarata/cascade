//! Cascade tier and inbox tier enumerations.
//!
//! # Cascade tiers (instruction layers)
//!
//! The six cascade tiers represent the instruction-inheritance hierarchy from
//! broadest (GCI — Global Claude Instructions) to most specific (PAC —
//! Per-App Cascade). Higher tiers win on conflict; lower tiers add specificity
//! without overriding.
//!
//! ```text
//! GCI  (~/.cascade/)                all tools, all projects
//!  └─ PCI  (~/<project-group>/.cascade/)  all projects in a group
//!      └─ APC  (~/<sites>/.cascade/)      all coding projects
//!          └─ PPC  (<project>/.cascade/)  single project (multi-repo)
//!              └─ PRC  (<repo>/.cascade/) single repo
//!                  └─ PAC  (<app>/.cascade/)  single app within a repo
//! ```
//!
//! # Inbox tiers
//!
//! `InboxTier` mirrors the three project-scoped tiers that receive cross-agent
//! PCI (Project Claude Inbox) messages.

use serde::{Deserialize, Serialize};

/// A cascade instruction tier.
///
/// Variants are ordered from highest precedence (GCI) to lowest (PAC).
/// The `Ord` implementation reflects this: `Gci < Pci < Apc < Ppc < Prc < Pac`
/// where "less than" means "higher precedence."
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum CascadeTier {
    /// Global Cascade Instructions — `~/.cascade/`.
    /// Applies to all tools and all projects. Highest precedence.
    Gci,

    /// Project-group Cascade Instructions — e.g. `~/Sites/.cascade/`.
    /// Applies to a named group of projects (equivalent to old ASI layer).
    Pci,

    /// All-Projects Cascade — e.g. a team-wide shared directory.
    /// Sits between a project group and an individual project.
    Apc,

    /// Per-Project Cascade — `<project-root>/.cascade/`.
    /// Applies to all repos within a multi-repo project.
    Ppc,

    /// Per-Repo Cascade — `<repo-root>/.cascade/`.
    /// Applies to a single git repository.
    Prc,

    /// Per-App Cascade — `<repo>/<app>/.cascade/`.
    /// Applies to a single application within a multi-app repo.
    Pac,
}

impl CascadeTier {
    /// Returns the short acronym used in file names and CLI arguments.
    pub fn acronym(&self) -> &'static str {
        match self {
            Self::Gci => "gci",
            Self::Pci => "pci",
            Self::Apc => "apc",
            Self::Ppc => "ppc",
            Self::Prc => "prc",
            Self::Pac => "pac",
        }
    }

    /// Returns a human-readable description of the tier.
    pub fn description(&self) -> &'static str {
        match self {
            Self::Gci => "Global Cascade Instructions (~/.cascade/)",
            Self::Pci => "Project-group Cascade Instructions",
            Self::Apc => "All-Projects Cascade",
            Self::Ppc => "Per-Project Cascade",
            Self::Prc => "Per-Repo Cascade",
            Self::Pac => "Per-App Cascade",
        }
    }

    /// Returns all tiers in precedence order (highest first).
    pub fn all_in_precedence_order() -> &'static [CascadeTier] {
        &[
            Self::Gci,
            Self::Pci,
            Self::Apc,
            Self::Ppc,
            Self::Prc,
            Self::Pac,
        ]
    }

    /// Parse a tier from its acronym string (case-insensitive).
    ///
    /// Returns `None` if the string does not match any known acronym.
    pub fn from_acronym(s: &str) -> Option<Self> {
        match s.to_lowercase().as_str() {
            "gci" => Some(Self::Gci),
            "pci" => Some(Self::Pci),
            "apc" => Some(Self::Apc),
            "ppc" => Some(Self::Ppc),
            "prc" => Some(Self::Prc),
            "pac" => Some(Self::Pac),
            _ => None,
        }
    }
}

impl std::fmt::Display for CascadeTier {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.acronym())
    }
}

impl std::str::FromStr for CascadeTier {
    type Err = String;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Self::from_acronym(s).ok_or_else(|| format!("unknown cascade tier: {s}"))
    }
}

// ── Inbox tier ────────────────────────────────────────────────────────────────

/// The inbox tier for cross-agent PCI messages.
///
/// Inbox messages are routed to the `.cascade/inbox/` directory at the
/// corresponding tier level.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum InboxTier {
    /// Per-Project Inbox — `<project-root>/.cascade/inbox/`.
    Ppi,
    /// Per-Repo Inbox — `<repo-root>/.cascade/inbox/`.
    Pri,
    /// Per-App Inbox — `<app>/.cascade/inbox/`.
    Pai,
}

impl InboxTier {
    /// Returns the acronym used in routing.
    pub fn acronym(&self) -> &'static str {
        match self {
            Self::Ppi => "ppi",
            Self::Pri => "pri",
            Self::Pai => "pai",
        }
    }
}

impl std::fmt::Display for InboxTier {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.acronym())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tier_roundtrip_from_acronym() {
        for tier in CascadeTier::all_in_precedence_order() {
            let parsed: CascadeTier = tier.acronym().parse().unwrap();
            assert_eq!(&parsed, tier);
        }
    }

    #[test]
    fn tier_precedence_order() {
        let tiers = CascadeTier::all_in_precedence_order();
        for window in tiers.windows(2) {
            assert!(
                window[0] < window[1],
                "GCI must be < PCI < APC < PPC < PRC < PAC in Ord"
            );
        }
    }

    #[test]
    fn unknown_acronym_returns_none() {
        assert!(CascadeTier::from_acronym("unknown").is_none());
    }
}
