//! # cascade_types::template
//!
//! Template system types for the Cascade context framework.
//!
//! ## Purpose
//!
//! Defines the core types used by the template registry and apply pipeline:
//! - [`TemplateTier`] — which cascade tier a template targets
//! - [`TemplateManifest`] — TOML frontmatter parsed from a `.md` template file
//! - [`TemplateRecord`] — manifest + raw Markdown body
//! - [`ApplyResult`] — outcome of applying a template to an existing CASCADE.md
//!
//! ## Inputs / Outputs
//!
//! All types implement `Serialize` + `Deserialize` (serde). `TemplateManifest`
//! uses `snake_case` field names for TOML round-trips. `TemplateTier` serialises
//! to lowercase strings (`"gci"`, `"pci"`, …, `"any"`).
//!
//! ## Constraints
//!
//! This module has no I/O — it is pure data. Loading logic lives in
//! `cascade_core::templates`.
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-types` exports (T-P3-E05-01).

use serde::{Deserialize, Serialize};
use std::fmt;

// ── TemplateTier ─────────────────────────────────────────────────────────────

/// The cascade tier a template targets.
///
/// Each variant corresponds to one level of the six-tier instruction cascade
/// (GCI → PCI → APC → PPC → PRC → PAC), plus `Any` for tier-agnostic templates.
///
/// # Serialization
///
/// Variants serialise to their lowercase string form:
/// `"gci"`, `"pci"`, `"apc"`, `"ppc"`, `"prc"`, `"pac"`, `"any"`.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum TemplateTier {
    /// Global Config Instructions (`~/.cascade/`)
    Gci,
    /// Project Config Instructions (e.g. `~/Sites/.cascade/`)
    Pci,
    /// App-level Config (`~/Sites/{project}/.cascade/`)
    Apc,
    /// Package-level Config (`~/Sites/{project}/{repo}/.cascade/`)
    Ppc,
    /// Repo Config (git repo root `.cascade/`)
    Prc,
    /// Per-App Config (app subdirectory `.cascade/`)
    Pac,
    /// Tier-agnostic — valid at any tier
    Any,
}

impl fmt::Display for TemplateTier {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let s = match self {
            Self::Gci => "gci",
            Self::Pci => "pci",
            Self::Apc => "apc",
            Self::Ppc => "ppc",
            Self::Prc => "prc",
            Self::Pac => "pac",
            Self::Any => "any",
        };
        f.write_str(s)
    }
}

// ── TemplateManifest ─────────────────────────────────────────────────────────

/// TOML frontmatter parsed from a `.md` template file.
///
/// The manifest lives between the opening and closing `---` delimiters at the
/// top of the template file. Fields follow `snake_case` naming to match TOML
/// conventions.
///
/// # Minimal valid manifest (TOML)
///
/// ```toml
/// id = "gci-default"
/// version = "1.0.0"
/// tier = "gci"
/// stacks = []
/// project_shapes = []
/// description = "Minimal GCI starter template"
/// ```
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub struct TemplateManifest {
    /// Unique identifier for this template (e.g. `"gci-default"`, `"rust-lib"`).
    pub id: String,

    /// Semver version string (e.g. `"1.0.0"`). Validated by the registry loader.
    pub version: String,

    /// Which cascade tier this template targets.
    pub tier: TemplateTier,

    /// Technology stack tags this template applies to (e.g. `["rust", "tauri"]`).
    /// Empty means the template applies to all stacks.
    pub stacks: Vec<String>,

    /// Project-shape tags (e.g. `["cli", "lib", "monorepo"]`).
    /// Empty means the template applies to all project shapes.
    pub project_shapes: Vec<String>,

    /// Human-readable description shown in the template browser.
    pub description: String,

    /// Optional id of a parent template this one extends.
    /// When set, the registry resolves inheritance during apply.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub extends: Option<String>,

    /// Minimum cascade version required to use this template
    /// (semver string, e.g. `"0.3.0"`). `None` means no minimum.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub min_cascade_version: Option<String>,
}

// ── TemplateRecord ────────────────────────────────────────────────────────────

/// A fully-parsed template file: manifest metadata + raw Markdown body.
///
/// `body` contains the Markdown content *after* the closing `---` frontmatter
/// delimiter. Trailing/leading whitespace from the body split is preserved as-is.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TemplateRecord {
    /// Parsed frontmatter.
    pub manifest: TemplateManifest,

    /// Raw Markdown body (everything after the closing `---`).
    pub body: String,
}

// ── ApplyResult ───────────────────────────────────────────────────────────────

/// Outcome of applying a template to an existing CASCADE.md file.
///
/// The apply operation is section-aware: it identifies Markdown heading
/// sections in both the template and the target, then merges them.
///
/// # Fields
///
/// - `applied` — section headings that were added to the target.
/// - `conflicts` — headings that existed in the target with *different* content
///   (the template version was NOT applied; user must resolve manually).
/// - `skipped` — headings that already existed in the target with identical
///   content (no-op; not considered a conflict).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ApplyResult {
    /// Sections that were added to the target document.
    pub applied: Vec<String>,

    /// Sections that exist in both template and target with differing content.
    pub conflicts: Vec<String>,

    /// Sections that already existed in the target with identical content.
    pub skipped: Vec<String>,
}

// ── DiffResult ────────────────────────────────────────────────────────────────

/// Outcome of a diff operation comparing a template version against an existing
/// CASCADE.md file.
///
/// Like [`ApplyResult`] but named to reflect the semantic intent of "what would
/// change".  Returned by [`cascade_core::templates::apply::TemplateEngine::diff`].
///
/// # Fields
///
/// - `added`     — sections present in the template but absent from the target.
/// - `conflicts` — sections present in both but with differing content.
/// - `matching`  — sections present in both with identical content (no-op).
///
/// ## SPORT
///
/// Registered in MASTER-TABLES.md under `cascade-types` exports (T-P3-E05-10).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct DiffResult {
    /// Sections in the template not yet present in the target.
    pub added: Vec<String>,

    /// Sections present in both template and target with differing content.
    pub conflicts: Vec<String>,

    /// Sections already in the target that match the template exactly.
    pub matching: Vec<String>,
}

// ── UpgradeResult ─────────────────────────────────────────────────────────────

/// Outcome of upgrading a target CASCADE.md from one template version to another.
///
/// Returned by [`cascade_core::templates::apply::TemplateEngine::upgrade`].
///
/// # Fields
///
/// - `upgraded`    — sections that existed in both old and new versions with
///   differing content; the new version was written to the target.
/// - `added`       — sections that are new in the new template version;
///   appended to the target.
/// - `deprecated`  — sections that existed in the old version but were removed
///   in the new version; a deprecation comment was added (content preserved).
///
/// ## SPORT
///
/// Registered in MASTER-TABLES.md under `cascade-types` exports (T-P3-E05-10).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct UpgradeResult {
    /// Sections overwritten with the new template version.
    pub upgraded: Vec<String>,

    /// Sections newly appended from the new template version.
    pub added: Vec<String>,

    /// Sections from the old version no longer in the new version;
    /// preserved in target with a deprecation notice comment.
    pub deprecated: Vec<String>,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn minimal_manifest() -> TemplateManifest {
        TemplateManifest {
            id: "gci-default".to_string(),
            version: "1.0.0".to_string(),
            tier: TemplateTier::Gci,
            stacks: vec![],
            project_shapes: vec![],
            description: "Minimal GCI starter template".to_string(),
            extends: None,
            min_cascade_version: None,
        }
    }

    // ── TemplateTier Display ──────────────────────────────────────────────────

    #[test]
    fn template_tier_display_all_variants() {
        assert_eq!(TemplateTier::Gci.to_string(), "gci");
        assert_eq!(TemplateTier::Pci.to_string(), "pci");
        assert_eq!(TemplateTier::Apc.to_string(), "apc");
        assert_eq!(TemplateTier::Ppc.to_string(), "ppc");
        assert_eq!(TemplateTier::Prc.to_string(), "prc");
        assert_eq!(TemplateTier::Pac.to_string(), "pac");
        assert_eq!(TemplateTier::Any.to_string(), "any");
    }

    // ── TemplateTier serde round-trip ─────────────────────────────────────────

    #[test]
    fn template_tier_serde_roundtrip() {
        for tier in &[
            TemplateTier::Gci,
            TemplateTier::Pci,
            TemplateTier::Apc,
            TemplateTier::Ppc,
            TemplateTier::Prc,
            TemplateTier::Pac,
            TemplateTier::Any,
        ] {
            let json = serde_json::to_string(tier).expect("serialize tier");
            let back: TemplateTier = serde_json::from_str(&json).expect("deserialize tier");
            assert_eq!(tier, &back, "round-trip failed for {:?}", tier);
        }
    }

    // ── TemplateManifest TOML round-trip ──────────────────────────────────────

    #[test]
    fn manifest_toml_roundtrip_minimal() {
        let m = minimal_manifest();
        let toml_str = toml::to_string(&m).expect("serialize manifest to TOML");
        let back: TemplateManifest = toml::from_str(&toml_str).expect("deserialize TOML manifest");
        assert_eq!(m, back);
    }

    #[test]
    fn manifest_toml_roundtrip_full() {
        let m = TemplateManifest {
            id: "rust-lib".to_string(),
            version: "2.1.0".to_string(),
            tier: TemplateTier::Prc,
            stacks: vec!["rust".to_string(), "cargo".to_string()],
            project_shapes: vec!["lib".to_string()],
            description: "Rust library template".to_string(),
            extends: Some("gci-default".to_string()),
            min_cascade_version: Some("0.3.0".to_string()),
        };
        let toml_str = toml::to_string(&m).expect("serialize manifest to TOML");
        let back: TemplateManifest = toml::from_str(&toml_str).expect("deserialize TOML manifest");
        assert_eq!(m, back);
    }

    #[test]
    fn manifest_deserializes_from_toml_literal() {
        let raw = r#"
id = "gci-default"
version = "1.0.0"
tier = "gci"
stacks = []
project_shapes = []
description = "Minimal GCI starter template"
"#;
        let m: TemplateManifest = toml::from_str(raw).expect("parse TOML literal");
        assert_eq!(m.id, "gci-default");
        assert_eq!(m.version, "1.0.0");
        assert_eq!(m.tier, TemplateTier::Gci);
        assert!(m.stacks.is_empty());
        assert!(m.project_shapes.is_empty());
        assert_eq!(m.description, "Minimal GCI starter template");
        assert!(m.extends.is_none());
        assert!(m.min_cascade_version.is_none());
    }

    // ── TemplateRecord serde round-trip ───────────────────────────────────────

    #[test]
    fn template_record_json_roundtrip() {
        let record = TemplateRecord {
            manifest: minimal_manifest(),
            body: "# Global Instructions\n\nTemplate body goes here.\n".to_string(),
        };
        let json = serde_json::to_string(&record).expect("serialize TemplateRecord");
        let back: TemplateRecord = serde_json::from_str(&json).expect("deserialize TemplateRecord");
        assert_eq!(record, back);
    }

    // ── ApplyResult construction ──────────────────────────────────────────────

    #[test]
    fn apply_result_fields_are_public() {
        let r = ApplyResult {
            applied: vec!["## Overview".to_string()],
            conflicts: vec!["## Rules".to_string()],
            skipped: vec!["## Footer".to_string()],
        };
        assert_eq!(r.applied.len(), 1);
        assert_eq!(r.conflicts.len(), 1);
        assert_eq!(r.skipped.len(), 1);
    }

    #[test]
    fn apply_result_serde_roundtrip() {
        let r = ApplyResult {
            applied: vec!["## A".to_string()],
            conflicts: vec![],
            skipped: vec!["## B".to_string(), "## C".to_string()],
        };
        let json = serde_json::to_string(&r).expect("serialize ApplyResult");
        let back: ApplyResult = serde_json::from_str(&json).expect("deserialize ApplyResult");
        assert_eq!(r, back);
    }
}
