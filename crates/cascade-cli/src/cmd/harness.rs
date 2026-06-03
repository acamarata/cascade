//! `cascade harness` — detect and report on available AI coding harnesses.
//!
//! Subcommands:
//!   cascade harness status — show running harnesses (CC, OC) in a human-readable table
//!   cascade harness detect — raw JSON output of all detected harnesses
//!
//! Calls the IPC `harness.detect` and `harness.status` endpoints.

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};

use super::Command;

/// Manage AI coding harnesses.
#[derive(Debug, Args)]
pub struct HarnessArgs {
    #[command(subcommand)]
    pub subcommand: HarnessSubcommand,
}

#[derive(Debug, Subcommand)]
pub enum HarnessSubcommand {
    /// Show the status of available harnesses in a human-readable table.
    Status(HarnessStatusArgs),
    /// Print raw JSON of all detected harnesses.
    Detect(HarnessDetectArgs),
}

/// Arguments for `cascade harness status`.
#[derive(Debug, Args)]
pub struct HarnessStatusArgs {
    /// Output as JSON instead of a table.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade harness detect`.
#[derive(Debug, Args)]
pub struct HarnessDetectArgs {
    /// (Placeholder for future filtering options)
    #[arg(long, hide = true)]
    pub _reserved: Option<bool>,
}

#[async_trait]
impl Command for HarnessArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            HarnessSubcommand::Status(args) => args.run().await,
            HarnessSubcommand::Detect(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for HarnessStatusArgs {
    async fn run(&self) -> Result<()> {
        // TODO(T-P2-E03-20): IPC call to harness.status endpoint
        // For now, placeholder output demonstrating the table format.
        if self.json {
            println!("{{}}");
        } else {
            println!("{:<12} {:<10} {:<10} BINARY", "HARNESS", "PID", "RUNNING");
            println!("{:<12} {:<10} {:<10} -", "ClaudeCode", "-", "false");
            println!("{:<12} {:<10} {:<10} -", "OpenCode", "-", "false");
        }
        Ok(())
    }
}

#[async_trait]
impl Command for HarnessDetectArgs {
    async fn run(&self) -> Result<()> {
        // TODO(T-P2-E03-20): IPC call to harness.detect endpoint
        println!("[]");
        Ok(())
    }
}
