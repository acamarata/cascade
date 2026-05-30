//! Command module — declares all subcommands and the top-level [`Cli`] struct.
//!
//! Each subcommand lives in its own module. All commands implement
//! [`Command::run`] which returns a [`cascade_types::error::Result<()>`].
//! The `main` entry point dispatches through [`Commands::run`].

pub mod config;
pub mod daemon;
pub mod doctor;
pub mod inbox;
pub mod init;
pub mod link;
pub mod memory;
pub mod migrate;
pub mod resolve;
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
    /// Diagnose cascade health and report issues.
    Doctor(doctor::DoctorArgs),
    /// Control the cascade background daemon.
    Daemon(daemon::DaemonArgs),
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
            Commands::Doctor(args) => args.run().await,
            Commands::Daemon(args) => args.run().await,
        }
    }
}

/// Shared trait for all command handlers.
#[async_trait]
pub trait Command {
    /// Execute the command. Returns `Ok(())` on success.
    async fn run(&self) -> Result<()>;
}
