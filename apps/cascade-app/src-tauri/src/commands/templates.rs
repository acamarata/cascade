//! # commands::templates
//!
//! Tauri IPC commands for the template engine — bridges the React frontend to
//! cascade-core's template registry, apply, diff, and upgrade operations.
//!
//! ## Purpose
//!
//! These commands are direct cascade-core calls (no daemon round-trip).
//! The template engine is stateless; the registry is loaded on each call from
//! the bundled templates directory and the user's `~/.cascade/templates/`.
//!
//! ## Commands
//!
//! | Tauri command      | core call                           |
//! |--------------------|-------------------------------------|
//! | `template_list`    | TemplateRegistry::list              |
//! | `template_apply`   | TemplateEngine::apply               |
//! | `template_diff`    | TemplateEngine::diff_sections       |
//! | `template_upgrade` | TemplateEngine::upgrade_by_id       |
//!
//! ## IPC naming
//!
//! Tauri automatically maps snake_case command names to camelCase on the JS
//! side (e.g. `template_list` → `invoke("template_list")`).  All struct fields
//! use `#[serde(rename_all = "camelCase")]` to match the frontend convention.
//!
//! ## Constraints
//!
//! - The target path passed from the GUI must be an absolute path to
//!   a CASCADE.md file. Confinement is enforced by TemplateEngine::apply.
//! - Errors are returned as descriptive strings, not opaque codes.
//!
//! ## SPORT
//!
//! MASTER-COMMANDS.md — template IPC commands (T-P3-E05-15, T-P3-E05-17)

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use tauri::State;

use cascade_core::templates::{
    apply::{ApplyOptions, TemplateEngine},
    registry::{TemplateFilter, TemplateRegistry},
};
use cascade_types::{paths::global_cascade_dir, TemplateTier};

use crate::state::AppState;

// ── Registry loader ────────────────────────────────────────────────────────────

/// Build a [`TemplateRegistry`] from bundled + user template directories.
///
/// Priority (highest last): bundled `<app resources>/templates/`, then
/// `~/.cascade/templates/` (user overrides bundled on id conflict).
///
/// Missing directories are silently skipped — registry may be empty if neither
/// exists.
fn load_registry() -> TemplateRegistry {
    // User-level override dir: ~/.cascade/templates/
    let user_dir = global_cascade_dir().join("templates");

    // Bundled templates are embedded at build time next to the binary.
    // We resolve them relative to the current exe's directory.
    let bundled_dir = std::env::current_exe()
        .ok()
        .and_then(|p| p.parent().map(|d| d.join("templates")))
        .unwrap_or_else(|| PathBuf::from("templates"));

    TemplateRegistry::load_dirs(&[bundled_dir.as_path(), user_dir.as_path()])
}

// ── IPC wire types ─────────────────────────────────────────────────────────────

/// Filter parameters for `template_list`.
///
/// All fields optional — `None` matches everything.
#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TemplateFilterIpc {
    /// Match templates whose tier equals this value (e.g. `"gci"`, `"prc"`).
    pub tier: Option<String>,

    /// Match templates that include this stack tag (e.g. `"rust"`, `"tauri"`).
    pub stack: Option<String>,

    /// Match templates that include this project-shape tag (e.g. `"cli"`, `"lib"`).
    pub shape: Option<String>,
}

/// One template entry returned by `template_list`.
///
/// Mirrors `cascade_types::TemplateManifest` + body string.
/// All fields present; consumers may display or preview any of them.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TemplateEntryIpc {
    pub id: String,
    pub version: String,
    pub tier: String,
    pub stacks: Vec<String>,
    pub project_shapes: Vec<String>,
    pub description: String,
    pub extends: Option<String>,
    pub min_cascade_version: Option<String>,
    /// Raw Markdown body — used for preview rendering on the frontend.
    pub body: String,
}

/// Outcome of `template_apply`.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApplyResultIpc {
    /// Section headings added to the target.
    pub applied: Vec<String>,
    /// Section headings that conflicted (not overwritten unless `force = true`).
    pub conflicts: Vec<String>,
    /// Section headings already in the target with identical content (skipped).
    pub skipped: Vec<String>,
}

/// Outcome of `template_diff`.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DiffResultIpc {
    /// Sections present in the template but absent from the target.
    pub added: Vec<String>,
    /// Sections present in both but with differing content.
    pub conflicts: Vec<String>,
    /// Sections already matching the template exactly.
    pub matching: Vec<String>,
}

/// Outcome of `template_upgrade`.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpgradeResultIpc {
    /// Sections overwritten with the new template version.
    pub upgraded: Vec<String>,
    /// Sections newly appended from the new template version.
    pub added: Vec<String>,
    /// Sections from the old version no longer in the new version (preserved
    /// in target with a deprecation notice comment).
    pub deprecated: Vec<String>,
}

// ── Tier string → TemplateTier ────────────────────────────────────────────────

/// Parse a tier string (e.g. `"gci"`) into a [`TemplateTier`].
///
/// Returns `None` for unknown strings so an invalid filter produces an
/// empty list rather than an error (per T-P3-E05-15 acceptance criterion).
fn parse_tier(s: &str) -> Option<TemplateTier> {
    match s.to_ascii_lowercase().as_str() {
        "gci" => Some(TemplateTier::Gci),
        "pci" => Some(TemplateTier::Pci),
        "apc" => Some(TemplateTier::Apc),
        "ppc" => Some(TemplateTier::Ppc),
        "prc" => Some(TemplateTier::Prc),
        "pac" => Some(TemplateTier::Pac),
        "any" => Some(TemplateTier::Any),
        _ => None,
    }
}

// ── Commands ──────────────────────────────────────────────────────────────────

/// List templates from the registry, applying optional filter criteria.
///
/// JS: `invoke("template_list", { filter: { tier: "gci" } })`
///
/// Returns all matching templates with their full body.  An invalid tier
/// string (unknown tier name) returns an empty array rather than an error,
/// so the frontend can safely pass any user input without crashing.
///
/// # Arguments
/// - `filter` — optional criteria (`tier`, `stack`, `shape`)
///
/// # Returns
/// Array of [`TemplateEntryIpc`] sorted by id.
#[tauri::command]
pub async fn template_list(
    filter: Option<TemplateFilterIpc>,
    _state: State<'_, AppState>,
) -> Result<Vec<TemplateEntryIpc>, String> {
    let filter = filter.unwrap_or(TemplateFilterIpc {
        tier: None,
        stack: None,
        shape: None,
    });

    // Build the core filter; an unknown tier string produces an empty list.
    let tier = match &filter.tier {
        Some(t) => {
            let parsed = parse_tier(t);
            if filter.tier.is_some() && parsed.is_none() {
                // Unknown tier → return empty (acceptance criterion).
                return Ok(vec![]);
            }
            parsed
        }
        None => None,
    };

    let core_filter = TemplateFilter {
        tier,
        stack: filter.stack.clone(),
        project_shape: filter.shape.clone(),
    };

    let registry = load_registry();
    let mut records = registry.list(&core_filter);

    // Sort by id for deterministic ordering.
    records.sort_by(|a, b| a.manifest.id.cmp(&b.manifest.id));

    let entries = records
        .into_iter()
        .map(|r| TemplateEntryIpc {
            id: r.manifest.id.clone(),
            version: r.manifest.version.clone(),
            tier: r.manifest.tier.to_string(),
            stacks: r.manifest.stacks.clone(),
            project_shapes: r.manifest.project_shapes.clone(),
            description: r.manifest.description.clone(),
            extends: r.manifest.extends.clone(),
            min_cascade_version: r.manifest.min_cascade_version.clone(),
            body: r.body.clone(),
        })
        .collect();

    Ok(entries)
}

/// Apply a template to a target CASCADE.md file.
///
/// JS: `invoke("template_apply", { id, target, dryRun, force })`
///
/// Calls `TemplateEngine::apply` directly — no daemon round-trip.
/// When `dryRun = true`, returns the plan without modifying any file.
///
/// # Arguments
/// - `id`     — template id (e.g. `"gci-default"`)
/// - `target` — absolute path to the CASCADE.md file to write
/// - `dry_run` — if `true`, return plan without writing
/// - `force`   — if `true`, overwrite conflicting sections
///
/// # Errors
/// Returns a descriptive string on failure (template not found, confinement
/// violation, I/O error).
#[tauri::command]
pub async fn template_apply(
    id: String,
    target: String,
    dry_run: bool,
    force: bool,
    _state: State<'_, AppState>,
) -> Result<ApplyResultIpc, String> {
    let registry = load_registry();
    let record = registry
        .resolve(&id)
        .map_err(|e| format!("template '{}' not found: {}", id, e))?;

    let target_path = Path::new(&target);
    let engine = TemplateEngine::new();
    let opts = ApplyOptions {
        dry_run,
        force,
        root: None, // defaults to $HOME
    };

    let result = engine
        .apply(&record, target_path, &opts)
        .map_err(|e| format!("apply failed: {}", e))?;

    Ok(ApplyResultIpc {
        applied: result.applied,
        conflicts: result.conflicts,
        skipped: result.skipped,
    })
}

/// Compare a template against an existing CASCADE.md without writing anything.
///
/// JS: `invoke("template_diff", { id, target })`
///
/// Calls `TemplateEngine::diff_sections` — pure read-only diff.
/// Returns which sections would be added, which conflict, and which already
/// match.
///
/// # Arguments
/// - `id`     — template id (e.g. `"gci-default"`)
/// - `target` — absolute path to the CASCADE.md to compare against
///
/// # Errors
/// Returns a descriptive string on failure (template not found, I/O error).
#[tauri::command]
pub async fn template_diff(
    id: String,
    target: String,
    _state: State<'_, AppState>,
) -> Result<DiffResultIpc, String> {
    let registry = load_registry();
    let record = registry
        .resolve(&id)
        .map_err(|e| format!("template '{}' not found: {}", id, e))?;

    let target_path = Path::new(&target);
    let engine = TemplateEngine::new();

    let result = engine
        .diff_sections(&record, target_path)
        .map_err(|e| format!("diff failed: {}", e))?;

    Ok(DiffResultIpc {
        added: result.added,
        conflicts: result.conflicts,
        matching: result.matching,
    })
}

/// Upgrade a target CASCADE.md from an older template version to the current one.
///
/// JS: `invoke("template_upgrade", { id, target, dryRun })`
///
/// Reads the applied-version stamp from `target` to determine the old version,
/// then calls `TemplateEngine::upgrade_by_id`. Three-way merges template
/// changes into the target while preserving user customisations.
///
/// When `dryRun = true`, returns the upgrade plan without modifying any file.
///
/// # Arguments
/// - `id`     — template id (must match a stamp already in the target)
/// - `target` — absolute path to the CASCADE.md to upgrade
/// - `dry_run` — if `true`, return plan without writing
///
/// # Errors
/// Returns a descriptive string on failure (stamp not found in target,
/// template not in registry, confinement violation, I/O error).
#[tauri::command]
pub async fn template_upgrade(
    id: String,
    target: String,
    dry_run: bool,
    _state: State<'_, AppState>,
) -> Result<UpgradeResultIpc, String> {
    let registry = load_registry();
    let new_record = registry
        .resolve(&id)
        .map_err(|e| format!("template '{}' not found: {}", id, e))?;

    let target_path = Path::new(&target);
    let engine = TemplateEngine::new();

    let result = engine
        .upgrade_by_id(&registry, &id, &new_record, target_path, dry_run, None)
        .map_err(|e| format!("upgrade failed: {}", e))?;

    Ok(UpgradeResultIpc {
        upgraded: result.upgraded,
        added: result.added,
        deprecated: result.deprecated,
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Override $HOME so load_registry() returns a known-empty user dir.
    ///
    /// We redirect HOME to a fresh temp dir that has no templates/ subdir.
    /// The bundled dir (resolved from the current exe) will also be absent
    /// in test — TemplateRegistry silently skips missing directories.
    fn with_home(tmp: &TempDir) {
        std::env::set_var("HOME", tmp.path());
    }

    // ── T-15-A: template_list returns empty array for unknown tier ────────────

    #[test]
    #[serial(global_env)]
    fn template_list_unknown_tier_returns_empty() {
        let tmp = TempDir::new().unwrap();
        with_home(&tmp);

        // Build the filter manually (bypassing the async Tauri command).
        let filter = TemplateFilterIpc {
            tier: Some("not_a_real_tier".to_string()),
            stack: None,
            shape: None,
        };

        // parse_tier returns None → immediate Ok(vec![])
        let result = parse_tier(filter.tier.as_deref().unwrap_or(""));
        assert!(result.is_none(), "unknown tier must parse as None");
    }

    // ── T-15-B: parse_tier covers all seven known tiers ───────────────────────

    #[test]
    fn parse_tier_all_known_variants() {
        assert!(parse_tier("gci").is_some());
        assert!(parse_tier("pci").is_some());
        assert!(parse_tier("apc").is_some());
        assert!(parse_tier("ppc").is_some());
        assert!(parse_tier("prc").is_some());
        assert!(parse_tier("pac").is_some());
        assert!(parse_tier("any").is_some());
        assert!(parse_tier("GCI").is_some(), "case-insensitive");
        assert!(parse_tier("bogus").is_none());
    }

    // ── T-15-C: load_registry with user template returns non-empty list ────────

    #[test]
    #[serial(global_env)]
    fn load_registry_picks_up_user_templates() {
        let tmp = TempDir::new().unwrap();
        with_home(&tmp);

        // Create user templates dir: ~/.cascade/templates/
        let user_tmpl_dir = tmp.path().join(".cascade").join("templates");
        fs::create_dir_all(&user_tmpl_dir).unwrap();

        // Write two templates.
        let t = TempDir::new().unwrap();
        // We need to write into user_tmpl_dir directly.
        let p1 = user_tmpl_dir.join("gci.md");
        let p2 = user_tmpl_dir.join("prc.md");
        fs::write(
            &p1,
            "---\nid = \"gci-default\"\nversion = \"1.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n## GCI\nbody\n",
        ).unwrap();
        fs::write(
            &p2,
            "---\nid = \"prc-rust\"\nversion = \"1.0.0\"\ntier = \"prc\"\nstacks = [\"rust\"]\nproject_shapes = []\ndescription = \"test\"\n---\n## PRC\nbody\n",
        ).unwrap();
        drop(t);

        let reg = load_registry();
        assert!(reg.len() >= 2, "expected at least 2 templates, got {}", reg.len());
        assert!(reg.get("gci-default").is_some());
        assert!(reg.get("prc-rust").is_some());
    }

    // ── T-15-D: TemplateEntryIpc includes body field ──────────────────────────

    #[test]
    #[serial(global_env)]
    fn template_entry_ipc_has_body() {
        let tmp = TempDir::new().unwrap();
        with_home(&tmp);

        let user_tmpl_dir = tmp.path().join(".cascade").join("templates");
        fs::create_dir_all(&user_tmpl_dir).unwrap();
        fs::write(
            user_tmpl_dir.join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n## Preview Section\npreview content\n",
        ).unwrap();

        let reg = load_registry();
        let records = reg.list(&TemplateFilter::default());
        assert!(!records.is_empty());

        let entry = TemplateEntryIpc {
            id: records[0].manifest.id.clone(),
            version: records[0].manifest.version.clone(),
            tier: records[0].manifest.tier.to_string(),
            stacks: records[0].manifest.stacks.clone(),
            project_shapes: records[0].manifest.project_shapes.clone(),
            description: records[0].manifest.description.clone(),
            extends: records[0].manifest.extends.clone(),
            min_cascade_version: records[0].manifest.min_cascade_version.clone(),
            body: records[0].body.clone(),
        };

        assert!(!entry.body.is_empty(), "body must be populated");
        assert!(entry.body.contains("preview content"), "body must contain template content");
    }

    // ── T-15-E: apply dry_run does not write file ─────────────────────────────

    #[test]
    #[serial(global_env)]
    fn apply_dry_run_does_not_write() {
        let tmp = TempDir::new().unwrap();
        with_home(&tmp);

        // Seed user templates
        let user_tmpl_dir = tmp.path().join(".cascade").join("templates");
        fs::create_dir_all(&user_tmpl_dir).unwrap();
        fs::write(
            user_tmpl_dir.join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n## Overview\nsome content\n",
        ).unwrap();

        let target = tmp.path().join("CASCADE.md");
        let registry = load_registry();
        let record = registry.resolve("gci-default").unwrap();
        let engine = TemplateEngine::new();
        let opts = ApplyOptions {
            dry_run: true,
            force: false,
            root: Some(tmp.path().to_path_buf()),
        };

        let result = engine.apply(&record, &target, &opts).unwrap();
        assert!(result.applied.contains(&"## Overview".to_string()));
        assert!(!target.exists(), "dry-run must not create the file");
    }

    // ── T-17-A: UpgradeResultIpc serialises correctly ─────────────────────────

    #[test]
    fn upgrade_result_ipc_serialises() {
        let r = UpgradeResultIpc {
            upgraded: vec!["## Rules".to_string()],
            added: vec![],
            deprecated: vec!["## OldSection".to_string()],
        };
        let json = serde_json::to_string(&r).expect("should serialise");
        assert!(json.contains("\"upgraded\""));
        assert!(json.contains("\"added\""));
        assert!(json.contains("\"deprecated\""));
    }

    // ── T-17-B: upgrade_by_id on already-current stamp → all empty ────────────
    //
    // We can't call make_stamp (pub(crate)), so we write the stamp string
    // directly in the format the apply module produces.

    #[test]
    #[serial(global_env)]
    fn upgrade_already_current_returns_empty() {
        let tmp = TempDir::new().unwrap();
        with_home(&tmp);

        // Seed user template: gci-default @ 1.0.0
        let user_tmpl_dir = tmp.path().join(".cascade").join("templates");
        fs::create_dir_all(&user_tmpl_dir).unwrap();
        fs::write(
            user_tmpl_dir.join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n## Intro\ncontent\n",
        ).unwrap();

        // First apply — writes the stamp.
        let target = tmp.path().join("CASCADE.md");
        let registry = load_registry();
        let record = registry.resolve("gci-default").unwrap();
        let engine = TemplateEngine::new();
        let opts = ApplyOptions {
            dry_run: false,
            force: false,
            root: Some(tmp.path().to_path_buf()),
        };
        engine.apply(&record, &target, &opts).unwrap();
        assert!(target.exists(), "apply must create the file");

        // Now upgrade — same version already applied → all-empty result.
        let result = engine
            .upgrade_by_id(&registry, "gci-default", &record, &target, false, Some(tmp.path().to_path_buf()))
            .unwrap();

        assert!(result.upgraded.is_empty(), "already current — no upgrade needed");
        assert!(result.added.is_empty());
        assert!(result.deprecated.is_empty());
    }
}
