//! `cascade template` subcommands — list, apply, diff, upgrade.
//!
//! Purpose: Wire the template system into the CLI with Clap v4 derive macros.
//! Supports four subcommands:
//!
//! - `cascade template list [--tier <tier>] [--stack <stack>] [--shape <shape>] [--upgradeable] [--json]`
//! - `cascade template apply --id <id> --target <path> [--var k=v]... [--dry-run] [--force]`
//! - `cascade template diff --id <id> --target <path>`
//! - `cascade template upgrade --id <id> --target <path> [--dry-run] [--force]`
//!
//! Variable substitution (`{{var}}`) is handled entirely at this CLI layer;
//! substituted content is fed into the core apply engine (per T-P3-E05-09 spec
//! deferred note).
//!
//! SPORT: MASTER-CLI.md — `cascade template` commands — cmd/template.rs
//! Task: T-P3-E05-11 / T-P3-E05-13

use async_trait::async_trait;
use cascade_core::templates::{
    apply::{ApplyOptions, TemplateEngine},
    registry::{TemplateFilter, TemplateRegistry},
};
use cascade_types::{
    error::{CascadeError, Result},
    TemplateTier,
};
use clap::{Args, Subcommand};
use std::path::{Path, PathBuf};

// ── Stamp parsing (CLI-layer) ─────────────────────────────────────────────────
// Mirrors the stamp format written by cascade-core::templates::apply.
// Pattern: <!-- cascade:applied { id="...", version="...", applied_at="..." } -->

const CLI_STAMP_PREFIX: &str = "<!-- cascade:applied";

/// Extract `(id, version)` pairs from all stamp comments in `content`.
fn read_stamps_from_content(content: &str) -> Vec<(String, String)> {
    let mut stamps = Vec::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with(CLI_STAMP_PREFIX) {
            continue;
        }
        if let (Some(id), Some(ver)) = (
            cli_extract_attr(trimmed, "id"),
            cli_extract_attr(trimmed, "version"),
        ) {
            stamps.push((id, ver));
        }
    }
    stamps
}

/// Extract `key="value"` from a stamp comment line.
fn cli_extract_attr(line: &str, key: &str) -> Option<String> {
    let needle = format!("{}=\"", key);
    let start = line.find(&needle)? + needle.len();
    let rest = &line[start..];
    let end = rest.find('"')?;
    Some(rest[..end].to_string())
}

use super::Command;

// ── Top-level args ─────────────────────────────────────────────────────────────

/// Manage Cascade context templates.
#[derive(Debug, Args)]
pub struct TemplateArgs {
    #[command(subcommand)]
    pub command: TemplateCommand,
}

/// Template subcommands.
#[derive(Debug, Subcommand)]
pub enum TemplateCommand {
    /// List available templates (optionally filtered by tier, stack, or shape).
    List(ListArgs),
    /// Apply a template to a CASCADE.md file.
    Apply(ApplyArgs),
    /// Show what applying a template would change (no writes).
    Diff(DiffArgs),
    /// Upgrade an already-applied template to a newer version.
    Upgrade(UpgradeArgs),
    /// Scaffold a new user template in ~/.cascade/templates/.
    Create(CreateArgs),
    /// Validate a template file against the TemplateManifest schema.
    Validate(ValidateArgs),
    /// Export a template to a standalone .md file for sharing.
    Export(ExportArgs),
}

#[async_trait]
impl Command for TemplateArgs {
    async fn run(&self) -> Result<()> {
        match &self.command {
            TemplateCommand::List(a) => a.run().await,
            TemplateCommand::Apply(a) => a.run().await,
            TemplateCommand::Diff(a) => a.run().await,
            TemplateCommand::Upgrade(a) => a.run().await,
            TemplateCommand::Create(a) => a.run().await,
            TemplateCommand::Validate(a) => a.run().await,
            TemplateCommand::Export(a) => a.run().await,
        }
    }
}

// ── cascade template list ──────────────────────────────────────────────────────

/// Arguments for `cascade template list`.
#[derive(Debug, Args)]
pub struct ListArgs {
    /// Filter by tier (gci, pci, apc, ppc, prc, pac, any).
    #[arg(long, value_name = "TIER")]
    pub tier: Option<String>,

    /// Filter by stack tag (e.g. `rust`, `tauri`).
    #[arg(long, value_name = "STACK")]
    pub stack: Option<String>,

    /// Filter by project shape tag (e.g. `cli`, `lib`, `monorepo`).
    #[arg(long, value_name = "SHAPE")]
    pub shape: Option<String>,

    /// Only show templates that have a newer registry version than the version
    /// currently stamped in the target CASCADE.md (i.e. templates eligible for
    /// `cascade template upgrade`).  Silently skips templates not applied to the
    /// target.
    #[arg(long)]
    pub upgradeable: bool,

    /// Path to the target CASCADE.md to read applied versions from.
    /// Defaults to the nearest `.cascade/CASCADE.md` walking up from CWD.
    /// Only used when `--upgradeable` is set.
    #[arg(long, value_name = "PATH")]
    pub target: Option<PathBuf>,

    /// Output as JSON array instead of a table.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for ListArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;

        let filter = TemplateFilter {
            tier: self.tier.as_deref().map(parse_tier).transpose()?,
            stack: self.stack.clone(),
            project_shape: self.shape.clone(),
        };

        let mut records = reg.list(&filter);
        // Stable sort by id for deterministic output.
        records.sort_by(|a, b| a.manifest.id.cmp(&b.manifest.id));

        // ── --upgradeable filter ─────────────────────────────────────────────
        // Read the applied version stamps from the target CASCADE.md and keep
        // only those templates whose registry version is newer than the stamped
        // version.  Templates not found in the stamp map are silently dropped
        // (not applied → not eligible for upgrade).
        if self.upgradeable {
            let target = resolve_target(self.target.as_deref())?;
            let stamps: std::collections::HashMap<String, String> = if target.exists() {
                let content = std::fs::read_to_string(&target).map_err(|e| {
                    CascadeError::Other(format!("read target {}: {}", target.display(), e))
                })?;
                read_stamps_from_content(&content).into_iter().collect()
            } else {
                std::collections::HashMap::new()
            };

            records.retain(|r| {
                let id = &r.manifest.id;
                if let Some(current_ver) = stamps.get(id) {
                    // available_upgrade returns Some only when registry > stamped.
                    reg.available_upgrade(id, current_ver).is_some()
                } else {
                    false // template not applied to target → not upgradeable
                }
            });
        }

        if self.json {
            // Emit a JSON array of manifest objects (no body — keeps output lean).
            let items: Vec<_> = records
                .iter()
                .map(|r| {
                    serde_json::json!({
                        "id":      r.manifest.id,
                        "version": r.manifest.version,
                        "tier":    r.manifest.tier.to_string(),
                        "stacks":  r.manifest.stacks,
                        "shapes":  r.manifest.project_shapes,
                        "description": r.manifest.description,
                    })
                })
                .collect();
            println!(
                "{}",
                serde_json::to_string_pretty(&items)
                    .map_err(|e| { CascadeError::Other(format!("json serialize: {e}")) })?
            );
        } else {
            if records.is_empty() {
                if self.upgradeable {
                    eprintln!(
                        "No upgradeable templates found — all applied templates are up to date."
                    );
                } else {
                    eprintln!("No templates found. Check ~/.cascade/templates/ or the bundled data/templates/.");
                }
                return Ok(());
            }
            // Print a plain-text table without an external dep.
            print_template_table(&records);
        }

        Ok(())
    }
}

/// Print a fixed-width table for a slice of template records.
fn print_template_table(records: &[&cascade_types::TemplateRecord]) {
    // Compute column widths.
    let id_w = records
        .iter()
        .map(|r| r.manifest.id.len())
        .max()
        .unwrap_or(2)
        .max(2);
    let ver_w = records
        .iter()
        .map(|r| r.manifest.version.len())
        .max()
        .unwrap_or(7)
        .max(7);
    let tier_w = records
        .iter()
        .map(|r| r.manifest.tier.to_string().len())
        .max()
        .unwrap_or(4)
        .max(4);
    let stacks_w = records
        .iter()
        .map(|r| {
            if r.manifest.stacks.is_empty() {
                3
            } else {
                r.manifest.stacks.join(",").len()
            }
        })
        .max()
        .unwrap_or(6)
        .max(6);
    let desc_w = 40usize;

    let sep = format!(
        "+-{}-+-{}-+-{}-+-{}-+-{}-+",
        "-".repeat(id_w),
        "-".repeat(ver_w),
        "-".repeat(tier_w),
        "-".repeat(stacks_w),
        "-".repeat(desc_w)
    );
    println!("{sep}");
    println!(
        "| {:<id_w$} | {:<ver_w$} | {:<tier_w$} | {:<stacks_w$} | {:<desc_w$} |",
        "id",
        "version",
        "tier",
        "stacks",
        "description",
        id_w = id_w,
        ver_w = ver_w,
        tier_w = tier_w,
        stacks_w = stacks_w,
        desc_w = desc_w
    );
    println!("{sep}");
    for r in records {
        let stacks_str = if r.manifest.stacks.is_empty() {
            "any".to_string()
        } else {
            r.manifest.stacks.join(",")
        };
        let desc_str = truncate(&r.manifest.description, desc_w);
        println!(
            "| {:<id_w$} | {:<ver_w$} | {:<tier_w$} | {:<stacks_w$} | {:<desc_w$} |",
            r.manifest.id,
            r.manifest.version,
            r.manifest.tier.to_string(),
            stacks_str,
            desc_str,
            id_w = id_w,
            ver_w = ver_w,
            tier_w = tier_w,
            stacks_w = stacks_w,
            desc_w = desc_w
        );
    }
    println!("{sep}");
    println!("{} template(s)", records.len());
}

fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        s.to_string()
    } else {
        format!(
            "{}…",
            &s[..s
                .char_indices()
                .nth(max.saturating_sub(1))
                .map_or(s.len(), |(i, _)| i)]
        )
    }
}

// ── cascade template apply ─────────────────────────────────────────────────────

/// Arguments for `cascade template apply`.
#[derive(Debug, Args)]
pub struct ApplyArgs {
    /// Template id to apply (e.g. `gci-default`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Path to the target CASCADE.md file.
    /// Defaults to the nearest `.cascade/CASCADE.md` walking up from CWD.
    #[arg(long, value_name = "PATH")]
    pub target: Option<PathBuf>,

    /// Variable substitution values: `--var key=value` (repeatable).
    /// Replaces `{{key}}` in the template body before applying.
    #[arg(long = "var", value_name = "KEY=VALUE", num_args = 1, action = clap::ArgAction::Append)]
    pub vars: Vec<String>,

    /// Print what would change without writing any files.
    #[arg(long)]
    pub dry_run: bool,

    /// Overwrite conflicting sections in the target.
    #[arg(long)]
    pub force: bool,
}

#[async_trait]
impl Command for ApplyArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;
        let record = reg
            .get(&self.id)
            .ok_or_else(|| not_found_error(&self.id))?
            .clone();

        let target = resolve_target(self.target.as_deref())?;
        let sub_map = parse_vars(&self.vars)?;

        // Substitute variables into the template body before applying.
        let substituted_body = substitute_vars(&record.body, &sub_map)?;
        let mut subst_record = record.clone();
        subst_record.body = substituted_body;

        let opts = ApplyOptions {
            dry_run: self.dry_run,
            force: self.force,
            root: None,
        };
        let engine = TemplateEngine::new();
        let result = engine.apply(&subst_record, &target, &opts)?;

        if self.dry_run {
            println!(
                "Dry-run: would apply template '{}' to {}",
                self.id,
                target.display()
            );
        } else if result.applied.is_empty()
            && result.conflicts.is_empty()
            && result.skipped.is_empty()
        {
            println!(
                "Template '{}' already applied (idempotent) — no changes.",
                self.id
            );
            return Ok(());
        }

        print_apply_result(&result, self.dry_run);
        Ok(())
    }
}

// ── cascade template diff ──────────────────────────────────────────────────────

/// Arguments for `cascade template diff`.
#[derive(Debug, Args)]
pub struct DiffArgs {
    /// Template id to diff (e.g. `gci-default`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Path to the target CASCADE.md file.
    /// Defaults to the nearest `.cascade/CASCADE.md` walking up from CWD.
    #[arg(long, value_name = "PATH")]
    pub target: Option<PathBuf>,
}

#[async_trait]
impl Command for DiffArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;
        let record = reg.get(&self.id).ok_or_else(|| not_found_error(&self.id))?;

        let target = resolve_target(self.target.as_deref())?;

        let engine = TemplateEngine::new();
        let result = engine.diff(record, &target)?;

        println!("Diff: template '{}' vs {}", self.id, target.display());
        print_apply_result(&result, true);
        Ok(())
    }
}

// ── cascade template upgrade ───────────────────────────────────────────────────

/// Arguments for `cascade template upgrade`.
#[derive(Debug, Args)]
pub struct UpgradeArgs {
    /// Template id to upgrade (e.g. `gci-default`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Path to the target CASCADE.md file.
    /// Defaults to the nearest `.cascade/CASCADE.md` walking up from CWD.
    #[arg(long, value_name = "PATH")]
    pub target: Option<PathBuf>,

    /// Print what would change without writing any files.
    #[arg(long)]
    pub dry_run: bool,

    /// Overwrite user-edited sections that diverge from the old template body
    /// in addition to the normal three-way upgrade logic.  Without `--force`,
    /// user edits to sections whose content has not changed between old and new
    /// template versions are left intact.  With `--force`, all conflicting
    /// sections are replaced with the new template version.
    #[arg(long)]
    pub force: bool,
}

#[async_trait]
impl Command for UpgradeArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;

        // The new_record is the latest version in the registry.
        let new_record = reg
            .get(&self.id)
            .ok_or_else(|| not_found_error(&self.id))?
            .clone();

        let target = resolve_target(self.target.as_deref())?;
        let engine = TemplateEngine::new();

        if self.force {
            // --force: apply the new record, overwriting ALL conflicting sections
            // (including user edits that diverge from the old template body).
            let opts = ApplyOptions {
                dry_run: self.dry_run,
                force: true,
                root: std::env::var_os("HOME").map(PathBuf::from),
            };
            let result = engine.apply(&new_record, &target, &opts)?;
            if self.dry_run {
                println!(
                    "Dry-run (force): would upgrade template '{}' in {}",
                    self.id,
                    target.display()
                );
            } else {
                println!(
                    "Upgraded (force) template '{}' in {}",
                    self.id,
                    target.display()
                );
            }
            print_apply_result(&result, self.dry_run);
        } else {
            // Normal path: three-way upgrade via upgrade_by_id.
            // Reads the current stamp from the target to find the old version,
            // then applies the three-way diff: preserves user sections, adds
            // deprecation comments for removed sections, reports upgraded/added/deprecated.
            let result = engine.upgrade_by_id(
                &reg,
                &self.id,
                &new_record,
                &target,
                self.dry_run,
                std::env::var_os("HOME").map(PathBuf::from),
            )?;

            if self.dry_run {
                println!(
                    "Dry-run: would upgrade template '{}' in {}",
                    self.id,
                    target.display()
                );
            } else {
                println!("Upgraded template '{}' in {}", self.id, target.display());
            }
            print_upgrade_result(&result, self.dry_run);
        }

        Ok(())
    }
}

// ── Helpers ────────────────────────────────────────────────────────────────────

/// Load the template registry from bundled + user template directories.
///
/// Priority: bundled (lower) < `~/.cascade/templates/` (higher).
/// Missing directories are silently skipped by the registry.
fn load_registry() -> Result<TemplateRegistry> {
    // Bundled templates — resolved relative to the binary's location or a
    // compile-time baked path. For development, fall back to the workspace root.
    let bundled = bundled_templates_dir();
    let user = user_templates_dir();

    let mut dirs: Vec<&Path> = Vec::new();
    // bundled first (lower priority), user second (higher priority).
    if let Some(ref b) = bundled {
        dirs.push(b.as_path());
    }
    if let Some(ref u) = user {
        dirs.push(u.as_path());
    }

    Ok(TemplateRegistry::load_dirs(&dirs))
}

/// Return the bundled `data/templates/` directory, or `None` if undiscoverable.
fn bundled_templates_dir() -> Option<PathBuf> {
    // Try environment variable override (set by tests or CI).
    if let Ok(v) = std::env::var("CASCADE_BUNDLED_TEMPLATES") {
        return Some(PathBuf::from(v));
    }
    // Try relative to the executable (installed binary case).
    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            let candidate = parent.join("../share/cascade/templates");
            if candidate.exists() {
                return Some(candidate);
            }
        }
    }
    None
}

/// Return `~/.cascade/templates/`, or `None` if HOME is unset.
fn user_templates_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".cascade").join("templates"))
}

/// Resolve the target path:
/// 1. If `--target` was given, use it.
/// 2. Otherwise, walk up from CWD looking for `.cascade/CASCADE.md`.
/// 3. If not found, fall back to `<cwd>/.cascade/CASCADE.md`.
fn resolve_target(explicit: Option<&Path>) -> Result<PathBuf> {
    if let Some(p) = explicit {
        return Ok(p.to_path_buf());
    }
    let cwd = std::env::current_dir()
        .map_err(|e| CascadeError::Other(format!("cannot determine CWD: {e}")))?;
    // Walk up looking for .cascade/CASCADE.md.
    for ancestor in cwd.ancestors() {
        let candidate = ancestor.join(".cascade").join("CASCADE.md");
        if candidate.exists() {
            return Ok(candidate);
        }
    }
    // Fallback: CWD/.cascade/CASCADE.md (may not exist yet — apply creates it).
    Ok(cwd.join(".cascade").join("CASCADE.md"))
}

/// Parse `--tier` string into [`TemplateTier`].
fn parse_tier(s: &str) -> Result<TemplateTier> {
    match s.to_lowercase().as_str() {
        "gci" => Ok(TemplateTier::Gci),
        "pci" => Ok(TemplateTier::Pci),
        "apc" => Ok(TemplateTier::Apc),
        "ppc" => Ok(TemplateTier::Ppc),
        "prc" => Ok(TemplateTier::Prc),
        "pac" => Ok(TemplateTier::Pac),
        "any" => Ok(TemplateTier::Any),
        other => Err(CascadeError::Other(format!(
            "unknown tier '{other}' — valid values: gci, pci, apc, ppc, prc, pac, any"
        ))),
    }
}

/// Parse `["key=value", ...]` into a `HashMap<String, String>`.
fn parse_vars(vars: &[String]) -> Result<std::collections::HashMap<String, String>> {
    let mut map = std::collections::HashMap::new();
    for entry in vars {
        let (k, v) = entry.split_once('=').ok_or_else(|| {
            CascadeError::Other(format!(
                "invalid --var '{entry}': expected key=value format"
            ))
        })?;
        map.insert(k.to_string(), v.to_string());
    }
    Ok(map)
}

/// Substitute `{{key}}` placeholders in `body` using `vars`.
///
/// Returns `Err` if any `{{key}}` placeholder has no corresponding entry in
/// `vars` and no inline default (defaults are not supported in this version —
/// all referenced variables must be provided via `--var`).
fn substitute_vars(body: &str, vars: &std::collections::HashMap<String, String>) -> Result<String> {
    let mut result = body.to_string();
    // Find all {{...}} patterns and substitute them.
    let mut output = String::with_capacity(result.len());
    let bytes = result.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        // Look for `{{`
        if i + 1 < bytes.len() && bytes[i] == b'{' && bytes[i + 1] == b'{' {
            // Find closing `}}`
            if let Some(close) = result[i + 2..].find("}}") {
                let key = &result[i + 2..i + 2 + close];
                let key = key.trim();
                if let Some(val) = vars.get(key) {
                    output.push_str(val);
                } else if key.contains(':') {
                    // Support `{{key:default}}` inline defaults.
                    let (var_name, default) = key.split_once(':').unwrap();
                    let var_name = var_name.trim();
                    if let Some(val) = vars.get(var_name) {
                        output.push_str(val);
                    } else {
                        output.push_str(default.trim());
                    }
                } else {
                    return Err(CascadeError::Other(format!(
                        "missing variable '{{{{{}}}}}' — provide it with --var {key}=<value>",
                        key
                    )));
                }
                i += 2 + close + 2; // skip past `}}`
                continue;
            }
        }
        output.push(bytes[i] as char);
        i += 1;
    }
    result = output;
    Ok(result)
}

/// Build the "not found" error message with a helpful hint.
fn not_found_error(id: &str) -> CascadeError {
    CascadeError::Other(format!(
        "Template '{id}' not found. Run `cascade template list` to see available templates."
    ))
}

/// Print a human-readable summary of an [`cascade_types::UpgradeResult`].
fn print_upgrade_result(result: &cascade_types::UpgradeResult, dry_run: bool) {
    let prefix = if dry_run { "[dry-run] " } else { "" };
    if !result.upgraded.is_empty() {
        println!("{prefix}Updated sections ({}):", result.upgraded.len());
        for s in &result.upgraded {
            println!("  ~ {s}");
        }
    }
    if !result.added.is_empty() {
        println!("{prefix}Newly added sections ({}):", result.added.len());
        for s in &result.added {
            println!("  + {s}");
        }
    }
    if !result.deprecated.is_empty() {
        println!(
            "{prefix}Deprecated sections ({}) — preserved with deprecation notice:",
            result.deprecated.len()
        );
        for s in &result.deprecated {
            println!("  ! {s}");
        }
    }
    if result.upgraded.is_empty() && result.added.is_empty() && result.deprecated.is_empty() {
        println!("{prefix}Already up to date — no changes needed.");
    }
}

/// Print a human-readable summary of an [`cascade_types::ApplyResult`].
fn print_apply_result(result: &cascade_types::ApplyResult, dry_run: bool) {
    let prefix = if dry_run { "[dry-run] " } else { "" };
    if !result.applied.is_empty() {
        println!("{prefix}Applied sections ({}):", result.applied.len());
        for s in &result.applied {
            println!("  + {s}");
        }
    }
    if !result.conflicts.is_empty() {
        println!(
            "{prefix}Conflicts ({}): existing content preserved:",
            result.conflicts.len()
        );
        for s in &result.conflicts {
            println!("  ! {s}");
        }
        if !dry_run {
            eprintln!("Tip: use --force to overwrite conflicting sections.");
        }
    }
    if !result.skipped.is_empty() {
        println!(
            "{prefix}Already present ({}): no change needed:",
            result.skipped.len()
        );
        for s in &result.skipped {
            println!("  = {s}");
        }
    }
    if result.applied.is_empty() && result.conflicts.is_empty() && result.skipped.is_empty() {
        println!("{prefix}Nothing to change (template already fully applied).");
    }
}

// ── cascade template create ────────────────────────────────────────────────────

/// Arguments for `cascade template create`.
///
/// Scaffolds a new user template at `~/.cascade/templates/<id>.md` (or `--output`).
/// The scaffold contains pre-filled TOML frontmatter and placeholder Markdown body.
/// Exits with an error if the file already exists (non-destructive).
#[derive(Debug, Args)]
pub struct CreateArgs {
    /// Unique id for the new template (e.g. `my-team-prc`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Tier to pre-fill in the manifest (gci, pci, apc, ppc, prc, pac, any).
    /// Defaults to `any`.
    #[arg(long, value_name = "TIER", default_value = "any")]
    pub tier: String,

    /// Optional parent template id for the `extends` field.
    #[arg(long, value_name = "PARENT")]
    pub extends: Option<String>,

    /// Output path for the scaffold file.
    /// Defaults to `~/.cascade/templates/<id>.md`.
    #[arg(long, value_name = "PATH")]
    pub output: Option<PathBuf>,
}

#[async_trait]
impl Command for CreateArgs {
    async fn run(&self) -> Result<()> {
        // Validate tier early.
        parse_tier(&self.tier)?;

        // Determine output path.
        let out_path = match &self.output {
            Some(p) => p.clone(),
            None => {
                let home = std::env::var_os("HOME").ok_or_else(|| {
                    CascadeError::Other("HOME is not set; use --output to specify a path".into())
                })?;
                PathBuf::from(home)
                    .join(".cascade")
                    .join("templates")
                    .join(format!("{}.md", self.id))
            }
        };

        // Non-destructive: refuse to overwrite an existing file.
        if out_path.exists() {
            return Err(CascadeError::Other(format!(
                "Template file already exists: {}  — delete it first or choose a different id.",
                out_path.display()
            )));
        }

        // Ensure parent directory exists.
        if let Some(parent) = out_path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| {
                CascadeError::Other(format!("cannot create directory {}: {e}", parent.display()))
            })?;
        }

        // Build TOML frontmatter.
        let extends_line = match &self.extends {
            Some(p) => format!("extends = \"{p}\"\n"),
            None => String::new(),
        };
        let scaffold = format!(
            "---\n\
             id = \"{id}\"\n\
             version = \"1.0.0\"\n\
             tier = \"{tier}\"\n\
             stacks = []\n\
             project_shapes = []\n\
             description = \"\"\n\
             {extends}---\n\
             \n\
             ## Purpose\n\
             \n\
             <!-- Describe what this template is for and when to use it. -->\n\
             \n\
             ## Key Conventions\n\
             \n\
             <!-- List the key conventions this template enforces. -->\n\
             \n\
             ## File Layout\n\
             \n\
             <!-- Describe the expected directory/file structure for projects using this template. -->\n",
            id = self.id,
            tier = self.tier,
            extends = extends_line,
        );

        std::fs::write(&out_path, &scaffold)
            .map_err(|e| CascadeError::Other(format!("write {}: {e}", out_path.display())))?;

        println!("Created template scaffold: {}", out_path.display());
        println!(
            "Edit the file, then run: cascade template validate {}",
            out_path.display()
        );
        Ok(())
    }
}

// ── cascade template validate ──────────────────────────────────────────────────

/// Arguments for `cascade template validate`.
///
/// Parses a template file and validates:
/// - Frontmatter parses as `TemplateManifest` (TOML schema).
/// - `id` field matches the filename slug (sans `.md` extension).
/// - `version` is a valid semver string.
/// - `description` is non-empty.
/// - Body contains at least 2 Markdown `##` sections.
///
/// Prints `PASS` on success (exit 0) or a numbered list of errors (exit 1).
#[derive(Debug, Args)]
pub struct ValidateArgs {
    /// Path to the `.md` template file to validate.
    #[arg(value_name = "PATH")]
    pub path: PathBuf,
}

#[async_trait]
impl Command for ValidateArgs {
    async fn run(&self) -> Result<()> {
        let errors = validate_template_file(&self.path)?;
        if errors.is_empty() {
            println!("PASS  {}", self.path.display());
            Ok(())
        } else {
            eprintln!("FAIL  {}", self.path.display());
            for (i, e) in errors.iter().enumerate() {
                eprintln!("  {}. {e}", i + 1);
            }
            std::process::exit(1);
        }
    }
}

/// Validate a template file path; returns a list of actionable error strings.
/// Returns `Err` only for I/O or parse errors that prevent further checks.
fn validate_template_file(path: &Path) -> Result<Vec<String>> {
    use cascade_core::templates::parser::parse_template_file;

    let record = parse_template_file(path)
        .map_err(|e| CascadeError::Other(format!("cannot parse {}: {e}", path.display())))?;

    let mut errors: Vec<String> = Vec::new();

    // 1. id must match the filename slug (stem without extension).
    let file_stem = path.file_stem().and_then(|s| s.to_str()).unwrap_or("");
    if record.manifest.id != file_stem {
        errors.push(format!(
            "id field '{}' does not match filename slug '{}' — rename the file or update the id field",
            record.manifest.id, file_stem
        ));
    }

    // 2. version must be valid semver (major.minor.patch, no pre-release required).
    if !is_valid_semver(&record.manifest.version) {
        errors.push(format!(
            "version '{}' is not valid semver — use the form MAJOR.MINOR.PATCH (e.g. 1.0.0)",
            record.manifest.version
        ));
    }

    // 3. description must be non-empty.
    if record.manifest.description.trim().is_empty() {
        errors.push(
            "description is empty — add a short human-readable description of this template".into(),
        );
    }

    // 4. body must contain at least 2 `##` sections.
    let section_count = record.body.lines().filter(|l| l.starts_with("## ")).count();
    if section_count < 2 {
        errors.push(format!(
            "body has {section_count} `##` section(s) — at least 2 are required \
             (e.g. ## Purpose and ## Key Conventions)"
        ));
    }

    Ok(errors)
}

/// Check that a version string is valid semver (MAJOR.MINOR.PATCH).
///
/// Accepts optional pre-release (`-alpha.1`) and build metadata (`+build.1`)
/// per semver 2.0.0, but the three numeric dot-separated components are required.
fn is_valid_semver(v: &str) -> bool {
    // Strip optional build metadata.
    let v = v.split('+').next().unwrap_or(v);
    // Strip optional pre-release.
    let v = v.split('-').next().unwrap_or(v);
    let parts: Vec<&str> = v.split('.').collect();
    if parts.len() != 3 {
        return false;
    }
    parts.iter().all(|p| p.parse::<u64>().is_ok())
}

// ── cascade template export ────────────────────────────────────────────────────

/// Arguments for `cascade template export`.
///
/// Exports a template (bundled or user) to a directory as a standalone `.md`
/// file that preserves frontmatter + body, ready for sharing.
#[derive(Debug, Args)]
pub struct ExportArgs {
    /// Template id to export (e.g. `gci-default`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Directory to write the exported file into.
    /// Defaults to the current working directory.
    #[arg(long, value_name = "DIR")]
    pub output: Option<PathBuf>,
}

#[async_trait]
impl Command for ExportArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;
        let record = reg.get(&self.id).ok_or_else(|| not_found_error(&self.id))?;

        let out_dir = match &self.output {
            Some(d) => d.clone(),
            None => std::env::current_dir()
                .map_err(|e| CascadeError::Other(format!("cannot determine CWD: {e}")))?,
        };

        std::fs::create_dir_all(&out_dir).map_err(|e| {
            CascadeError::Other(format!(
                "cannot create directory {}: {e}",
                out_dir.display()
            ))
        })?;

        let out_file = out_dir.join(format!("{}.md", self.id));

        // Build the file content: TOML frontmatter block + body.
        let fm = toml::to_string(&record.manifest)
            .map_err(|e| CascadeError::Other(format!("serialize manifest: {e}")))?;
        let content = format!("---\n{fm}---\n{}", record.body);

        std::fs::write(&out_file, &content)
            .map_err(|e| CascadeError::Other(format!("write {}: {e}", out_file.display())))?;

        println!("Exported template '{}' → {}", self.id, out_file.display());
        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::{TemplateManifest, TemplateRecord};
    use serial_test::serial;
    use std::fs;
    use std::io::Write;
    use tempfile::TempDir;

    // ── Fixtures ──────────────────────────────────────────────────────────────

    fn write_template_file(dir: &TempDir, name: &str, id: &str, tier: &str, body: &str) {
        let path = dir.path().join(name);
        let mut f = fs::File::create(&path).unwrap();
        writeln!(
            f,
            "---\nid = \"{id}\"\nversion = \"1.0.0\"\ntier = \"{tier}\"\nstacks = []\nproject_shapes = []\ndescription = \"Test {id}\"\n---\n{body}"
        )
        .unwrap();
    }

    fn make_record_with_body(id: &str, body: &str) -> TemplateRecord {
        TemplateRecord {
            manifest: TemplateManifest {
                id: id.to_string(),
                version: "1.0.0".to_string(),
                tier: TemplateTier::Gci,
                stacks: vec![],
                project_shapes: vec![],
                description: format!("Test {id}"),
                extends: None,
                min_cascade_version: None,
            },
            body: body.to_string(),
        }
    }

    // ── Arg parsing ───────────────────────────────────────────────────────────

    #[test]
    fn parse_list_args_defaults() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ListArgs,
        }

        let cli = Cli::try_parse_from(["list"]).unwrap();
        assert!(cli.args.tier.is_none());
        assert!(cli.args.stack.is_none());
        assert!(cli.args.shape.is_none());
        assert!(!cli.args.json);
    }

    #[test]
    fn parse_list_args_all_flags() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ListArgs,
        }

        let cli = Cli::try_parse_from([
            "list", "--tier", "gci", "--stack", "rust", "--shape", "lib", "--json",
        ])
        .unwrap();
        assert_eq!(cli.args.tier.as_deref(), Some("gci"));
        assert_eq!(cli.args.stack.as_deref(), Some("rust"));
        assert_eq!(cli.args.shape.as_deref(), Some("lib"));
        assert!(cli.args.json);
    }

    #[test]
    fn parse_apply_args() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ApplyArgs,
        }

        let cli = Cli::try_parse_from([
            "apply",
            "--id",
            "gci-default",
            "--target",
            "/tmp/test.md",
            "--var",
            "project=myproject",
            "--var",
            "author=Alice",
            "--dry-run",
            "--force",
        ])
        .unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert_eq!(cli.args.target.as_deref(), Some(Path::new("/tmp/test.md")));
        assert_eq!(cli.args.vars, vec!["project=myproject", "author=Alice"]);
        assert!(cli.args.dry_run);
        assert!(cli.args.force);
    }

    #[test]
    fn parse_diff_args() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: DiffArgs,
        }

        let cli = Cli::try_parse_from(["diff", "--id", "gci-default"]).unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert!(cli.args.target.is_none());
    }

    #[test]
    fn parse_upgrade_args() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: UpgradeArgs,
        }

        let cli = Cli::try_parse_from(["upgrade", "--id", "gci-default", "--dry-run"]).unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert!(cli.args.dry_run);
        assert!(!cli.args.force, "--force should default to false");
    }

    #[test]
    fn parse_upgrade_args_with_force() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: UpgradeArgs,
        }

        let cli = Cli::try_parse_from(["upgrade", "--id", "gci-default", "--force"]).unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert!(cli.args.force);
        assert!(!cli.args.dry_run);
    }

    #[test]
    fn parse_list_args_upgradeable_flag() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ListArgs,
        }

        let cli = Cli::try_parse_from(["list", "--upgradeable"]).unwrap();
        assert!(cli.args.upgradeable);
        assert!(cli.args.target.is_none());

        let cli2 =
            Cli::try_parse_from(["list", "--upgradeable", "--target", "/tmp/target.md"]).unwrap();
        assert!(cli2.args.upgradeable);
        assert_eq!(
            cli2.args.target.as_deref(),
            Some(Path::new("/tmp/target.md"))
        );
    }

    // ── list: fixture registry ────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn list_against_fixture_registry() {
        let tmp = TempDir::new().unwrap();
        write_template_file(&tmp, "gci.md", "gci-default", "gci", "## Overview\nBody.\n");
        write_template_file(
            &tmp,
            "prc-rust.md",
            "prc-rust",
            "prc",
            "## Rust Rules\nBody.\n",
        );
        write_template_file(&tmp, "any-base.md", "any-base", "any", "## Base\nBody.\n");
        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", tmp.path()) };
        unsafe { std::env::remove_var("HOME") }; // ensure no user override

        let reg = load_registry().unwrap();
        assert_eq!(reg.len(), 3);

        let all = reg.list(&TemplateFilter::default());
        assert_eq!(all.len(), 3);

        let filter = TemplateFilter {
            tier: Some(TemplateTier::Gci),
            ..Default::default()
        };
        let gci_matches = reg.list(&filter);
        // Should match gci-default (Gci) and any-base (Any matches all).
        assert_eq!(gci_matches.len(), 2);
    }

    // ── list: --upgradeable filter ────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn list_upgradeable_returns_only_templates_with_newer_registry_version() {
        let tmp = TempDir::new().unwrap();
        // Template registry has version 1.1.0 for "gci-default".
        let path = tmp.path().join("gci.md");
        fs::write(&path, "---\nid = \"gci-default\"\nversion = \"1.1.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Overview\nNew body.\n").unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", tmp.path()) };
        unsafe { std::env::remove_var("HOME") };

        // Target file stamped with version 1.0.0 (older → should appear as upgradeable).
        let target_dir = TempDir::new().unwrap();
        let target = target_dir.path().join("CASCADE.md");
        // Write a stamp at version 1.0.0 (the "already applied" version).
        let stamp = r#"<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->"#.to_string();
        fs::write(&target, format!("## Overview\nOld body.\n\n{}\n", stamp)).unwrap();

        let reg = load_registry().unwrap();
        let filter = TemplateFilter::default();
        let all = reg.list(&filter);
        assert_eq!(all.len(), 1, "registry should have 1 template");

        // Read stamps from the target and filter upgradeable.
        let content = fs::read_to_string(&target).unwrap();
        let stamps: std::collections::HashMap<String, String> =
            read_stamps_from_content(&content).into_iter().collect();

        let mut records = all;
        records.retain(|r| {
            if let Some(cv) = stamps.get(&r.manifest.id) {
                reg.available_upgrade(&r.manifest.id, cv).is_some()
            } else {
                false
            }
        });

        assert_eq!(records.len(), 1, "gci-default should be upgradeable");
        assert_eq!(records[0].manifest.version, "1.1.0");
    }

    #[test]
    #[serial(global_env)]
    fn list_upgradeable_returns_empty_when_already_up_to_date() {
        let tmp = TempDir::new().unwrap();
        // Registry version = 1.0.0, target stamped with 1.0.0 → no upgrade.
        let path = tmp.path().join("gci.md");
        fs::write(&path, "---\nid = \"gci-default\"\nversion = \"1.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Overview\nBody.\n").unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", tmp.path()) };
        unsafe { std::env::remove_var("HOME") };

        let target_dir = TempDir::new().unwrap();
        let target = target_dir.path().join("CASCADE.md");
        let stamp = r#"<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->"#;
        fs::write(&target, format!("## Overview\nBody.\n\n{}\n", stamp)).unwrap();

        let reg = load_registry().unwrap();
        let content = fs::read_to_string(&target).unwrap();
        let stamps: std::collections::HashMap<String, String> =
            read_stamps_from_content(&content).into_iter().collect();

        let mut records = reg.list(&TemplateFilter::default());
        records.retain(|r| {
            if let Some(cv) = stamps.get(&r.manifest.id) {
                reg.available_upgrade(&r.manifest.id, cv).is_some()
            } else {
                false
            }
        });

        assert!(
            records.is_empty(),
            "no template should be upgradeable when already up to date"
        );
    }

    // ── upgrade: three-way engine wired via upgrade_by_id ────────────────────

    /// Verify the upgrade subcommand correctly routes to `engine.upgrade_by_id`
    /// (three-way semantics) rather than force-apply.  Specifically:
    ///
    /// - A target stamped at version 1.0.0 is successfully upgraded.
    /// - The stamp is updated to 1.1.0.
    /// - User content is preserved when the template section is unchanged between
    ///   old and new (because upgrade_by_id reconstructs the old record from the
    ///   registry body — both old_versioned and new have identical bodies here,
    ///   so no forced overwrite occurs, preserving user content).
    #[test]
    #[serial(global_env)]
    fn upgrade_wires_to_upgrade_by_id_stamp_updated() {
        use cascade_core::templates::apply::TemplateEngine;

        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };

        let engine = TemplateEngine::new();

        // Target stamped at 1.0.0 — stamp indicates this was applied previously.
        let stamp = r#"<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->"#;
        let target = tmp.path().join("CASCADE.md");
        fs::write(&target, format!("## Rules\nuser content\n\n{}\n", stamp)).unwrap();

        // Registry has gci-default at 1.1.0.
        let reg_dir = TempDir::new().unwrap();
        fs::write(
            reg_dir.path().join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.1.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Rules\nnew content\n",
        ).unwrap();
        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", reg_dir.path()) };
        let reg = load_registry().unwrap();
        let new_record = reg.get("gci-default").unwrap().clone();

        // upgrade_by_id should succeed (target has a 1.0.0 stamp for gci-default).
        // Since old_versioned body == new body (both come from registry), no section
        // is "changed between old and new" → UpgradeResult is empty (no-op on content).
        // The stamp IS updated to 1.1.0 because we were not yet at 1.1.0.
        let result = engine
            .upgrade_by_id(
                &reg,
                "gci-default",
                &new_record,
                &target,
                false,
                Some(tmp.path().to_path_buf()),
            )
            .unwrap();

        let content = fs::read_to_string(&target).unwrap();
        // Stamp must be updated to 1.1.0.
        assert!(
            content.contains(r#"version="1.1.0""#),
            "stamp must be updated to 1.1.0: {content}"
        );
        // User content preserved (section body unchanged between old and new template versions).
        assert!(
            content.contains("user content"),
            "user content should be preserved"
        );
        // Result should not error.
        let _ = result; // UpgradeResult accepted (may be empty when body unchanged).
    }

    /// Verify that upgrade with --force falls back to apply(force=true) and
    /// overwrites all conflicting sections.
    #[test]
    #[serial(global_env)]
    fn upgrade_force_overwrites_all_conflicting_sections() {
        use cascade_core::templates::apply::{ApplyOptions, TemplateEngine};

        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };

        let engine = TemplateEngine::new();

        // Target has user-edited ## Rules section.
        let target = tmp.path().join("CASCADE.md");
        fs::write(&target, "## Rules\nuser content\n").unwrap();

        // Registry has new content for ## Rules.
        let reg_dir = TempDir::new().unwrap();
        fs::write(
            reg_dir.path().join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.1.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Rules\nnew content\n",
        ).unwrap();
        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", reg_dir.path()) };
        let reg = load_registry().unwrap();
        let new_record = reg.get("gci-default").unwrap().clone();

        // --force path: apply with force=true.
        let opts = ApplyOptions {
            dry_run: false,
            force: true,
            root: Some(tmp.path().to_path_buf()),
        };
        let result = engine.apply(&new_record, &target, &opts).unwrap();

        assert!(
            result.applied.contains(&"## Rules".to_string()),
            "force should overwrite conflicting section: {:?}",
            result
        );
        let content = fs::read_to_string(&target).unwrap();
        assert!(
            content.contains("new content"),
            "force should write new template content"
        );
        assert!(
            !content.contains("user content"),
            "old user content should be replaced"
        );
    }

    #[test]
    #[serial(global_env)]
    fn read_stamps_from_content_extracts_id_and_version() {
        let content = r#"## Rules
some content

<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->
<!-- cascade:applied { id="prc-rust", version="2.3.1", applied_at="2026-02-01T00:00:00Z" } -->
"#;
        let stamps = read_stamps_from_content(content);
        assert_eq!(stamps.len(), 2);
        let map: std::collections::HashMap<_, _> = stamps.into_iter().collect();
        assert_eq!(map["gci-default"], "1.0.0");
        assert_eq!(map["prc-rust"], "2.3.1");
    }

    #[test]
    fn read_stamps_from_content_empty_file() {
        let stamps = read_stamps_from_content("");
        assert!(stamps.is_empty());
    }

    // ── apply: dry-run returns plan without writing ────────────────────────────

    #[test]
    #[serial(global_env)]
    fn apply_dry_run_does_not_write() {
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };

        let record = make_record_with_body("gci-default", "## Overview\nNew content.\n");
        let target = tmp.path().join("CASCADE.md");

        let engine = TemplateEngine::new();
        let opts = ApplyOptions {
            dry_run: true,
            root: Some(tmp.path().to_path_buf()),
            ..Default::default()
        };
        let result = engine.apply(&record, &target, &opts).unwrap();

        assert!(result.applied.contains(&"## Overview".to_string()));
        assert!(!target.exists(), "dry-run must not create the file");
    }

    // ── apply: variable substitution ─────────────────────────────────────────

    #[test]
    fn substitute_vars_basic() {
        let mut vars = std::collections::HashMap::new();
        vars.insert("project".to_string(), "cascade".to_string());
        vars.insert("author".to_string(), "Aric".to_string());

        let body = "# {{project}}\nMaintained by {{author}}.\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, "# cascade\nMaintained by Aric.\n");
    }

    #[test]
    fn substitute_vars_with_default() {
        let vars = std::collections::HashMap::new();
        let body = "License: {{license:MIT}}\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, "License: MIT\n");
    }

    #[test]
    fn substitute_vars_with_default_override() {
        let mut vars = std::collections::HashMap::new();
        vars.insert("license".to_string(), "Apache-2.0".to_string());
        let body = "License: {{license:MIT}}\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, "License: Apache-2.0\n");
    }

    #[test]
    fn substitute_vars_missing_var_returns_error() {
        let vars = std::collections::HashMap::new();
        let body = "# {{project_name}}\n";
        let result = substitute_vars(body, &vars);
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("project_name"),
            "error should name the missing var: {msg}"
        );
        assert!(
            msg.contains("--var"),
            "error should hint at --var flag: {msg}"
        );
    }

    #[test]
    fn substitute_vars_no_placeholders() {
        let vars = std::collections::HashMap::new();
        let body = "# No placeholders here.\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, body);
    }

    // ── apply: missing-var error ──────────────────────────────────────────────

    #[test]
    fn parse_vars_invalid_format() {
        let bad = vec!["no-equals-sign".to_string()];
        let result = parse_vars(&bad);
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("key=value"),
            "error should mention format: {msg}"
        );
    }

    #[test]
    fn parse_vars_valid() {
        let entries = vec!["key=val".to_string(), "foo=bar=baz".to_string()];
        let map = parse_vars(&entries).unwrap();
        assert_eq!(map["key"], "val");
        assert_eq!(map["foo"], "bar=baz"); // split_once keeps rest after first =
    }

    // ── parse_tier ────────────────────────────────────────────────────────────

    #[test]
    fn parse_tier_valid_values() {
        assert_eq!(parse_tier("gci").unwrap(), TemplateTier::Gci);
        assert_eq!(parse_tier("prc").unwrap(), TemplateTier::Prc);
        assert_eq!(parse_tier("any").unwrap(), TemplateTier::Any);
        assert_eq!(parse_tier("GCI").unwrap(), TemplateTier::Gci); // case-insensitive
    }

    #[test]
    fn parse_tier_invalid_value() {
        let result = parse_tier("bogus");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("bogus"),
            "error should mention the invalid value: {msg}"
        );
    }

    // ── truncate helper ───────────────────────────────────────────────────────

    #[test]
    fn truncate_short_string_unchanged() {
        assert_eq!(truncate("hello", 10), "hello");
    }

    #[test]
    fn truncate_long_string_shortened() {
        let long = "a".repeat(50);
        let result = truncate(&long, 10);
        assert!(
            result.len() <= 12,
            "truncated result should be short: {result}"
        );
        assert!(result.ends_with('…') || result.len() <= 10);
    }
    // ── create: scaffolds valid template ─────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn create_writes_scaffold_with_output_override() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("my-tmpl.md");

        let args = CreateArgs {
            id: "my-tmpl".to_string(),
            tier: "prc".to_string(),
            extends: None,
            output: Some(out.clone()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("create should succeed");

        assert!(out.exists(), "scaffold file should be created");
        let content = fs::read_to_string(&out).unwrap();
        assert!(content.contains("id = \"my-tmpl\""), "id: {content}");
        assert!(content.contains("tier = \"prc\""), "tier: {content}");
        assert!(
            content.contains("version = \"1.0.0\""),
            "version: {content}"
        );
        assert!(content.contains("## Purpose"), "Purpose: {content}");
        assert!(
            content.contains("## Key Conventions"),
            "Key Conventions: {content}"
        );
        assert!(content.contains("## File Layout"), "File Layout: {content}");
    }

    #[test]
    #[serial(global_env)]
    fn create_refuses_to_overwrite_existing_file() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("existing.md");
        fs::write(&out, "already here").unwrap();

        let args = CreateArgs {
            id: "existing".to_string(),
            tier: "any".to_string(),
            extends: None,
            output: Some(out.clone()),
        };
        let result = tokio::runtime::Runtime::new().unwrap().block_on(args.run());
        assert!(result.is_err(), "should error on existing file");
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("already exists"), "error: {msg}");
    }

    #[test]
    #[serial(global_env)]
    fn create_includes_extends_when_set() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("child.md");

        let args = CreateArgs {
            id: "child".to_string(),
            tier: "prc".to_string(),
            extends: Some("gci-default".to_string()),
            output: Some(out.clone()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("create with extends should succeed");

        let content = fs::read_to_string(&out).unwrap();
        assert!(
            content.contains("extends = \"gci-default\""),
            "extends: {content}"
        );
    }

    // ── validate: passes on well-formed scaffold ──────────────────────────────

    #[test]
    #[serial(global_env)]
    fn validate_passes_on_well_formed_scaffold() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("well-formed.md");

        let args = CreateArgs {
            id: "well-formed".to_string(),
            tier: "prc".to_string(),
            extends: None,
            output: Some(out.clone()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("create should succeed");

        let content = fs::read_to_string(&out).unwrap();
        let fixed = content.replace("description = \"\"", "description = \"My test template\"");
        fs::write(&out, fixed).unwrap();

        let errors = validate_template_file(&out).expect("validate should not I/O error");
        assert!(errors.is_empty(), "scaffold should be valid: {errors:?}");
    }

    #[test]
    fn validate_errors_on_empty_description() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("test-desc.md");
        fs::write(&path,
            "---\nid = \"test-desc\"\nversion = \"1.0.0\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"\"\n---\n             ## Purpose\n\nText.\n\n## Key Conventions\n\nMore text.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(!errors.is_empty(), "should have errors");
        assert!(
            errors.iter().any(|e| e.contains("description")),
            "desc: {errors:?}"
        );
    }

    #[test]
    fn validate_errors_on_id_mismatch() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("filename-slug.md");
        fs::write(&path,
            "---\nid = \"wrong-id\"\nversion = \"1.0.0\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"desc\"\n---\n             ## Section One\n\nText.\n\n## Section Two\n\nMore.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(
            errors
                .iter()
                .any(|e| e.contains("id field") && e.contains("filename slug")),
            "id mismatch: {errors:?}"
        );
    }

    #[test]
    fn validate_errors_on_too_few_sections() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("one-section.md");
        fs::write(&path,
            "---\nid = \"one-section\"\nversion = \"1.0.0\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"desc\"\n---\n             ## Only Section\n\nJust one.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(
            errors.iter().any(|e| e.contains("section")),
            "sections: {errors:?}"
        );
    }

    #[test]
    fn validate_errors_on_bad_semver() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("bad-version.md");
        fs::write(&path,
            "---\nid = \"bad-version\"\nversion = \"not-semver\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"desc\"\n---\n             ## Section One\n\nText.\n\n## Section Two\n\nMore.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(
            errors.iter().any(|e| e.contains("semver")),
            "semver: {errors:?}"
        );
    }

    // ── is_valid_semver helper ────────────────────────────────────────────────

    #[test]
    fn semver_valid_versions() {
        assert!(is_valid_semver("1.0.0"));
        assert!(is_valid_semver("0.3.0"));
        assert!(is_valid_semver("10.20.30"));
        assert!(is_valid_semver("1.0.0-alpha.1"));
        assert!(is_valid_semver("1.0.0+build.1"));
    }

    #[test]
    fn semver_invalid_versions() {
        assert!(!is_valid_semver("1.0"));
        assert!(!is_valid_semver("1"));
        assert!(!is_valid_semver("not-semver"));
        assert!(!is_valid_semver("1.0.0.0"));
        assert!(!is_valid_semver(""));
    }

    // ── export: writes standalone .md ────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn export_writes_template_to_dir() {
        let bundled_dir = TempDir::new().unwrap();
        write_template_file(
            &bundled_dir,
            "gci-default.md",
            "gci-default",
            "gci",
            "## Overview\nBody content.\n",
        );
        let out_dir = TempDir::new().unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", bundled_dir.path()) };
        unsafe { std::env::remove_var("HOME") };

        let args = ExportArgs {
            id: "gci-default".to_string(),
            output: Some(out_dir.path().to_path_buf()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("export should succeed");

        let exported = out_dir.path().join("gci-default.md");
        assert!(exported.exists(), "exported file should exist");
        let content = fs::read_to_string(&exported).unwrap();
        assert!(content.contains("id = \"gci-default\""), "id: {content}");
        assert!(content.contains("## Overview"), "body: {content}");
        assert!(content.starts_with("---"), "frontmatter: {content}");
    }

    #[test]
    #[serial(global_env)]
    fn export_errors_on_unknown_id() {
        let bundled_dir = TempDir::new().unwrap();
        let out_dir = TempDir::new().unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", bundled_dir.path()) };
        unsafe { std::env::remove_var("HOME") };

        let args = ExportArgs {
            id: "nonexistent".to_string(),
            output: Some(out_dir.path().to_path_buf()),
        };
        let result = tokio::runtime::Runtime::new().unwrap().block_on(args.run());
        assert!(result.is_err(), "should error on unknown id");
    }
}
