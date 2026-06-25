//! `cascade template` subcommands — list, apply, diff, upgrade, create, validate, export.
//!
//! Purpose: Wire the template system into the CLI with Clap v4 derive macros.
//! Supports seven subcommands — see sub-modules for details.
//!
//! SPORT: MASTER-CLI.md — `cascade template` commands — cmd/template/
//! Task: T-P3-E05-11 / T-P3-E05-13

mod apply;
mod helpers;
mod list;
mod manage;
mod tests;

use super::Command;
use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};

// Re-export public types so external callers see the same paths as before.
pub use apply::{ApplyArgs, DiffArgs, UpgradeArgs};
pub use list::ListArgs;
pub use manage::{CreateArgs, ExportArgs, ValidateArgs};

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
