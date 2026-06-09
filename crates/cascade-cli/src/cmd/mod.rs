//! Command module — declares all subcommands and the top-level [`Cli`] struct.
//!
//! Each subcommand lives in its own module. All commands implement
//! [`Command::run`] which returns a [`cascade_types::error::Result<()>`].
//! The `main` entry point dispatches through [`Commands::run`].

pub mod backup;
pub mod completions;
pub mod config;
pub mod daemon;
pub mod doctor;
pub mod harness;
pub mod inbox;
pub mod init;
pub mod link;
pub mod memory;
pub mod migrate;
pub mod migrate_keys;
pub mod ping;
pub mod resolve;
pub mod restore;
pub mod search;
pub mod status;
pub mod unlink;

use async_trait::async_trait;
use clap::{Parser, Subcommand};

use cascade_types::error::Result;

/// Cascade AI context framework CLI.
#[derive(Debug, Parser)]
#[command(
    name = "cascade",
    about = "Manage the Cascade AI context framework",
    version,
    propagate_version = true
)]
pub struct Cli {
    /// Increase log verbosity. Pass once for DEBUG, twice for TRACE.
    #[arg(short, long, action = clap::ArgAction::Count, global = true)]
    pub verbose: u8,

    #[command(subcommand)]
    pub command: Commands,
}

/// All top-level subcommands.
#[derive(Debug, Subcommand)]
pub enum Commands {
    /// Scaffold a `.cascade/` directory at the detected or specified tier.
    Init(init::InitArgs),
    /// Show daemon health, index state, and cascade tier summary.
    Status(status::StatusArgs),
    /// Print the merged cascade for the current working directory.
    Resolve(resolve::ResolveArgs),
    /// Run a RAG search against the active index.
    Search(search::SearchArgs),
    /// Manage `.cascade/inbox/` messages.
    Inbox(inbox::InboxArgs),
    /// Read or write `.cascade/memory/` files.
    Memory(memory::MemoryArgs),
    /// Read or write cascade configuration values.
    Config(config::ConfigArgs),
    /// Create a tool-specific symlink pointing to CASCADE.md.
    Link(link::LinkArgs),
    /// Remove a tool-specific symlink.
    Unlink(unlink::UnlinkArgs),
    /// Migrate a legacy `.claude/` or `.opencode/` directory to `.cascade/`.
    Migrate(migrate::MigrateArgs),
    /// Move `GEMINI_API_KEY_*` secrets from vault.env into the OS keychain.
    MigrateKeys(migrate_keys::MigrateKeysArgs),
    /// Diagnose cascade health and report issues.
    Doctor(doctor::DoctorArgs),
    /// Control the cascade background daemon.
    Daemon(daemon::DaemonArgs),
    /// Print shell completion script to stdout.
    Completions(completions::CompletionsArgs),
    /// Backup snapshots (list/restore).
    Backup(backup::BackupArgs),
    /// Restore an archived tool's files to their original paths.
    Restore(restore::RestoreArgs),
    /// Show AI harness detection status.
    #[command(hide = true)]
    Harness(harness::HarnessArgs),
    /// Ping the cascade daemon (hidden diagnostic).
    #[command(hide = true)]
    Ping(ping::PingArgs),
}

impl Commands {
    /// Dispatch to the appropriate subcommand handler.
    pub async fn run(&self) -> Result<()> {
        match self {
            Commands::Init(args) => args.run().await,
            Commands::Status(args) => args.run().await,
            Commands::Resolve(args) => args.run().await,
            Commands::Search(args) => args.run().await,
            Commands::Inbox(args) => args.run().await,
            Commands::Memory(args) => args.run().await,
            Commands::Config(args) => args.run().await,
            Commands::Link(args) => args.run().await,
            Commands::Unlink(args) => args.run().await,
            Commands::Migrate(args) => args.run().await,
            Commands::MigrateKeys(args) => args.run().await,
            Commands::Doctor(args) => args.run().await,
            Commands::Daemon(args) => args.run().await,
            Commands::Completions(args) => args.run().await,
            Commands::Backup(args) => args.run().await,
            Commands::Restore(args) => args.run().await,
            Commands::Harness(args) => args.run().await,
            Commands::Ping(args) => args.run().await,
        }
    }
}

/// Shared trait for all command handlers.
#[async_trait]
pub trait Command {
    /// Execute the command. Returns `Ok(())` on success.
    async fn run(&self) -> Result<()>;
}
