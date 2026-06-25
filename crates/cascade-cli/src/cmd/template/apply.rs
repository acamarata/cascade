//! `cascade template apply`, `diff`, and `upgrade` subcommands.
//!
//! Purpose: Wire the core apply/diff/upgrade engine to CLI args.
//! Inputs: Template id, target path, optional vars and flags.
//! Outputs: Section change summary to stdout; writes to target on non-dry-run.
//! Constraints: No logic changes — CLI plumbing only.

use super::helpers::{
    load_registry, not_found_error, parse_vars, print_apply_result, print_upgrade_result,
    resolve_target, substitute_vars,
};
use super::Command;
use async_trait::async_trait;
use cascade_core::templates::apply::{ApplyOptions, TemplateEngine};
use cascade_types::error::Result;
use clap::Args;
use std::path::PathBuf;

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

    /// Overwrite user-edited sections that diverge from the old template body.
    /// Without `--force`, user edits to unchanged sections are preserved.
    #[arg(long)]
    pub force: bool,
}

#[async_trait]
impl Command for UpgradeArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;

        let new_record = reg
            .get(&self.id)
            .ok_or_else(|| not_found_error(&self.id))?
            .clone();

        let target = resolve_target(self.target.as_deref())?;
        let engine = TemplateEngine::new();

        if self.force {
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
