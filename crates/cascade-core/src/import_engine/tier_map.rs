//! # tier_map — deterministic source-file → Cascade tier mapping
//!
//! ## Purpose
//! Map each [`DiscoveredFile`] from a source `.claude/`/`.opencode/`/`.codex/`
//! tree onto a Cascade tier and target path within `.cascade/`. The mapping is
//! fully table-driven and deterministic — given the same input path it always
//! produces the same output.
//!
//! ## Inputs
//! `&DiscoveredFile` + optional target `dest_root: &Path`.
//!
//! ## Outputs
//! `TierMapping` — describes the target Cascade tier, the suggested file path
//! within `.cascade/`, and the reason for the mapping.
//!
//! ## Constraints
//! - Must never assign a file to a higher-scope tier than its source implies
//!   (isolation guard).
//! - Unknown/script files are mapped to `.cascade/library/` and tagged as
//!   `MappedAsLibrary` so the coverage ledger can track them.
//!
//! ## Errors
//! Infallible — every file gets a mapping (possibly `Tier::Unknown`).
//!
//! ## Notes
//! The `load` field records whether this file is always-loaded or on-demand
//! at the mapped tier, so `cascade generate-instructions` can emit the right
//! pointer style.

use std::path::{Path, PathBuf};

use cascade_types::cascade_tier::CascadeTier;
use serde::{Deserialize, Serialize};

use super::discover::{DiscoveredFile, SourceKind};

// ── Public types ──────────────────────────────────────────────────────────────

/// How a mapped file should be loaded at runtime.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum LoadMode {
    /// File content is always injected into the active context.
    AlwaysLoaded,
    /// File is referenced via pointer; loaded only when the rule triggers.
    OnDemand,
    /// File is a library asset (script, template) — not loaded as instructions.
    Library,
}

/// The result of mapping a single [`DiscoveredFile`] to a Cascade destination.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TierMapping {
    /// The Cascade tier this file belongs to.
    pub tier: CascadeTier,
    /// The suggested path within the `.cascade/` directory at that tier.
    ///
    /// This is relative to the tier root (e.g. `rules/destructive-deny-list.md`).
    pub cascade_relative_path: PathBuf,
    /// Whether this file is always-loaded or on-demand.
    pub load_mode: LoadMode,
    /// Human-readable explanation of the mapping decision.
    pub reason: String,
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Map a discovered source file to a Cascade tier and destination path.
///
/// This is the primary public function. Pass the discovered file and the
/// tool name (`"claude"`, `"opencode"`, `"codex"`) so path heuristics can
/// adapt to tool-specific conventions.
///
/// # Arguments
/// * `file` — the discovered source file.
/// * `tool` — the harness tool name (for path interpretation).
pub fn map_file(file: &DiscoveredFile, tool: &str) -> TierMapping {
    let rel = &file.relative_path;
    let kind = &file.kind;

    // ── GCI-tier mappings (global single-user instructions) ──────────────────
    match kind {
        SourceKind::InstructionRoot => {
            // CLAUDE.md / GLOBAL.md / AGENTS.md / RTK.md at root → GCI CASCADE.md
            // RTK.md is a companion to CLAUDE.md — it becomes a library entry.
            let file_name = rel.file_name().and_then(|n| n.to_str()).unwrap_or("");
            let name_lower = file_name.to_lowercase();

            if name_lower == "rtk.md" || name_lower == "global.md" {
                // Companion files become on-demand references at GCI.
                return TierMapping {
                    tier: CascadeTier::Gci,
                    cascade_relative_path: PathBuf::from("library").join("refs").join(file_name),
                    load_mode: LoadMode::Library,
                    reason: format!(
                        "{} companion → GCI library/refs/{}",
                        tool, file_name
                    ),
                };
            }

            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: PathBuf::from("CASCADE.md"),
                load_mode: LoadMode::AlwaysLoaded,
                reason: format!("{} root instruction file → GCI CASCADE.md", tool),
            }
        }

        SourceKind::AlwaysLoadedRule => {
            // rules/* → GCI .cascade/rules/
            let file_name = rel.file_name().and_then(|n| n.to_str()).unwrap_or("rule.md");
            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: PathBuf::from("rules").join(file_name),
                load_mode: LoadMode::AlwaysLoaded,
                reason: format!("always-loaded rule → GCI rules/{}", file_name),
            }
        }

        SourceKind::OnDemandReference => {
            // references/* and references-rules/* → GCI .cascade/library/refs/
            let subpath = map_reference_subpath(rel);
            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: subpath.clone(),
                load_mode: LoadMode::OnDemand,
                reason: format!(
                    "on-demand reference → GCI {}",
                    subpath.display()
                ),
            }
        }

        SourceKind::Standard => {
            // standards/* → GCI .cascade/library/standards/
            let file_name = rel.file_name().and_then(|n| n.to_str()).unwrap_or("standard.md");
            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: PathBuf::from("library")
                    .join("standards")
                    .join(file_name),
                load_mode: LoadMode::OnDemand,
                reason: format!("standard → GCI library/standards/{}", file_name),
            }
        }

        SourceKind::Template => {
            // templates/* → GCI .cascade/library/templates/
            let file_name = rel.file_name().and_then(|n| n.to_str()).unwrap_or("template.md");
            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: PathBuf::from("library")
                    .join("templates")
                    .join(file_name),
                load_mode: LoadMode::Library,
                reason: format!("template → GCI library/templates/{}", file_name),
            }
        }

        SourceKind::Script => {
            // scripts/* → GCI .cascade/library/scripts/
            let file_name = rel.file_name().and_then(|n| n.to_str()).unwrap_or("script");
            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: PathBuf::from("library")
                    .join("scripts")
                    .join(file_name),
                load_mode: LoadMode::Library,
                reason: format!("script → GCI library/scripts/{}", file_name),
            }
        }

        SourceKind::Memory => {
            // memory/* → PRC .cascade/memory/ (project-local memory)
            let subpath = rel.strip_prefix("memory").unwrap_or(rel);
            TierMapping {
                tier: CascadeTier::Prc,
                cascade_relative_path: PathBuf::from("memory").join(subpath),
                load_mode: LoadMode::Library,
                reason: format!("memory file → PRC memory/{}", subpath.display()),
            }
        }

        SourceKind::Inbox => {
            let subpath = rel.strip_prefix("inbox").unwrap_or(rel);
            TierMapping {
                tier: CascadeTier::Prc,
                cascade_relative_path: PathBuf::from("inbox").join(subpath),
                load_mode: LoadMode::Library,
                reason: format!("inbox message → PRC inbox/{}", subpath.display()),
            }
        }

        SourceKind::Idea => {
            let subpath = rel.strip_prefix("ideas").unwrap_or(rel);
            TierMapping {
                tier: CascadeTier::Prc,
                cascade_relative_path: PathBuf::from("ideas").join(subpath),
                load_mode: LoadMode::Library,
                reason: format!("idea → PRC ideas/{}", subpath.display()),
            }
        }

        SourceKind::Task => {
            let subpath = strip_first_component(rel);
            let reason = format!("task/phase file → PRC phases/{}", subpath.display());
            TierMapping {
                tier: CascadeTier::Prc,
                cascade_relative_path: PathBuf::from("phases").join(subpath),
                load_mode: LoadMode::Library,
                reason,
            }
        }

        SourceKind::SiteTier => {
            // sites/* are PCI-level (project-group) instructions.
            let subpath = rel.strip_prefix("sites").unwrap_or(rel);
            let file_name = rel.file_name().and_then(|n| n.to_str()).unwrap_or("file.md");
            let name_lower = file_name.to_lowercase();
            if name_lower == "asi-claude.md"
                || name_lower == "engineering-standard.md"
                || name_lower.ends_with("-template.md")
                || name_lower.ends_with("-scaffold.md")
            {
                return TierMapping {
                    tier: CascadeTier::Pci,
                    cascade_relative_path: PathBuf::from("library").join("refs").join(file_name),
                    load_mode: LoadMode::Library,
                    reason: format!("site-tier reference → PCI library/refs/{}", file_name),
                };
            }
            TierMapping {
                tier: CascadeTier::Pci,
                cascade_relative_path: subpath.to_path_buf(),
                load_mode: LoadMode::OnDemand,
                reason: format!("site-tier file → PCI {}", subpath.display()),
            }
        }

        SourceKind::Unknown => {
            // Unknown files land in the GCI library for manual review.
            let file_name = rel
                .file_name()
                .and_then(|n| n.to_str())
                .unwrap_or("unknown");
            TierMapping {
                tier: CascadeTier::Gci,
                cascade_relative_path: PathBuf::from("library")
                    .join("unclassified")
                    .join(file_name),
                load_mode: LoadMode::Library,
                reason: format!(
                    "unknown kind → GCI library/unclassified/{} (needs manual review)",
                    file_name
                ),
            }
        }
    }
}

/// Compute the absolute destination path for a file given the tier root
/// and the mapping.
///
/// `tier_roots` maps each `CascadeTier` to the absolute path of its `.cascade/`
/// directory. Pass `None` to get just the relative path.
pub fn dest_path(
    mapping: &TierMapping,
    tier_roots: &std::collections::HashMap<CascadeTier, PathBuf>,
) -> PathBuf {
    if let Some(root) = tier_roots.get(&mapping.tier) {
        root.join(&mapping.cascade_relative_path)
    } else {
        mapping.cascade_relative_path.clone()
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Map a reference file's relative path to the `.cascade/library/refs/` subpath.
fn map_reference_subpath(rel: &Path) -> PathBuf {
    // references-rules/* → library/rules/
    if let Ok(rest) = rel.strip_prefix("references-rules") {
        return PathBuf::from("library").join("rules").join(rest);
    }
    // references/* → library/refs/
    if let Ok(rest) = rel.strip_prefix("references") {
        return PathBuf::from("library").join("refs").join(rest);
    }
    // global/* refs → library/refs/
    if let Ok(rest) = rel.strip_prefix("global") {
        return PathBuf::from("library").join("refs").join(rest);
    }
    // Fallback.
    PathBuf::from("library").join("refs").join(
        rel.file_name()
            .unwrap_or_else(|| std::ffi::OsStr::new("ref.md")),
    )
}

/// Strip the first path component from a relative path.
fn strip_first_component(rel: &Path) -> PathBuf {
    let mut components = rel.components();
    components.next(); // drop first
    components.as_path().to_path_buf()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::import_engine::discover::SourceKind;

    fn make_file(rel: &str, kind: SourceKind) -> DiscoveredFile {
        DiscoveredFile {
            path: PathBuf::from(rel),
            relative_path: PathBuf::from(rel),
            kind,
            imports: vec![],
            pointers: vec![],
            readable: true,
            content: String::new(),
        }
    }

    #[test]
    fn claude_md_maps_to_gci_cascade_md() {
        let f = make_file("CLAUDE.md", SourceKind::InstructionRoot);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert_eq!(m.cascade_relative_path, PathBuf::from("CASCADE.md"));
        assert_eq!(m.load_mode, LoadMode::AlwaysLoaded);
    }

    #[test]
    fn rtk_md_maps_to_gci_library() {
        let f = make_file("RTK.md", SourceKind::InstructionRoot);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert!(m.cascade_relative_path.starts_with("library"));
        assert_eq!(m.load_mode, LoadMode::Library);
    }

    #[test]
    fn rule_file_maps_to_gci_rules() {
        let f = make_file("rules/destructive-deny-list.md", SourceKind::AlwaysLoadedRule);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert_eq!(
            m.cascade_relative_path,
            PathBuf::from("rules/destructive-deny-list.md")
        );
        assert_eq!(m.load_mode, LoadMode::AlwaysLoaded);
    }

    #[test]
    fn reference_rule_maps_to_gci_library_rules() {
        let f = make_file(
            "references-rules/delegation-mandate.md",
            SourceKind::OnDemandReference,
        );
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert!(m.cascade_relative_path.starts_with("library/rules"));
        assert_eq!(m.load_mode, LoadMode::OnDemand);
    }

    #[test]
    fn standard_maps_to_gci_library_standards() {
        let f = make_file("standards/rust.md", SourceKind::Standard);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert!(m.cascade_relative_path.starts_with("library/standards"));
    }

    #[test]
    fn memory_file_maps_to_prc() {
        let f = make_file("memory/decisions.md", SourceKind::Memory);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Prc);
        assert!(m.cascade_relative_path.starts_with("memory"));
    }

    #[test]
    fn script_maps_to_gci_library_scripts() {
        let f = make_file("scripts/claude-recall", SourceKind::Script);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert!(m.cascade_relative_path.starts_with("library/scripts"));
        assert_eq!(m.load_mode, LoadMode::Library);
    }

    #[test]
    fn template_maps_to_gci_library_templates() {
        let f = make_file("templates/subagent-context.md", SourceKind::Template);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert!(m.cascade_relative_path.starts_with("library/templates"));
    }

    #[test]
    fn site_tier_instruction_maps_to_pci() {
        let f = make_file("sites/ASI-CLAUDE.md", SourceKind::SiteTier);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Pci);
    }

    #[test]
    fn unknown_maps_to_gci_unclassified() {
        let f = make_file("someweirdfile.txt", SourceKind::Unknown);
        let m = map_file(&f, "claude");
        assert_eq!(m.tier, CascadeTier::Gci);
        assert!(m.cascade_relative_path.starts_with("library/unclassified"));
    }
}
