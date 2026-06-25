//! `cascade template list` subcommand.
//!
//! Purpose: List available templates with optional filtering by tier/stack/shape,
//! and support for --upgradeable (shows only templates with newer registry versions).
//! Inputs: ListArgs with optional filter flags.
//! Outputs: Plain-text table or JSON array to stdout.
//! Constraints: No external table crate; ASCII table rendered manually.

use super::helpers::{load_registry, parse_tier, read_stamps_from_content, resolve_target};
use super::Command;
use async_trait::async_trait;
use cascade_core::templates::registry::TemplateFilter;
use cascade_types::error::{CascadeError, Result};
use clap::Args;
use std::path::PathBuf;

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

    /// Only show templates eligible for `cascade template upgrade`.
    /// Silently skips templates not applied to the target.
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
        records.sort_by(|a, b| a.manifest.id.cmp(&b.manifest.id));

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
                    reg.available_upgrade(id, current_ver).is_some()
                } else {
                    false
                }
            });
        }

        if self.json {
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
            print_template_table(&records);
        }

        Ok(())
    }
}

/// Print a fixed-width table for a slice of template records.
pub(super) fn print_template_table(records: &[&cascade_types::TemplateRecord]) {
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

pub(super) fn truncate(s: &str, max: usize) -> String {
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
