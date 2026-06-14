//! Command module — declares all subcommands and the top-level [`Cli`] struct.
//!
//! Each subcommand lives in its own module. All commands implement
//! [`Command::run`] which returns a [`cascade_types::error::Result<()>`].
//! The `main` entry point dispatches through [`Commands::run`].

pub mod backup;
// E-P8-03: native PBD phase-tracking engine
pub mod pbd;
// E-P6-04: T0 CEO / Founder orchestrator
pub mod ceo;
// E-P7-06: subscription adapter detection + config-generation
pub mod subs;
// E-P5-04: post-init healthcheck gate
pub mod verify;
// E-P5-08: provider credential CLI (add/list/remove/test)
pub mod provider;
// T-P4-E04-11: cascade cache stats + cache clear
pub mod cache;
// E-P5-07: local LLM model catalog + download + remove
pub mod models;
// E-P5-06: AI-folder preference
pub mod folder;
// T-P4-E06-13: cascade policy eval/list/add/remove + dispatch enforcement
pub mod policy;
// T-P4-E04-21: cascade context clear-session / cleanup-expired
pub mod context;
// T-P4-E04-15: cascade rollback list/apply
pub mod rollback;
// T-P4-E04-16: cascade update check/apply/auto
pub mod completions;
pub mod config;
pub mod daemon;
pub mod dispatch;
pub mod doctor;
pub mod generate_instructions;
pub mod harness;
pub mod inbox;
pub mod init;
pub mod link;
pub mod mcp;
pub mod memory;
pub mod migrate;
pub mod migrate_keys;
pub mod monitor_oc;
pub mod ping;
pub mod plugin;
pub mod resolve;
pub mod restore;
pub mod search;
pub mod setup_oc;
pub mod status;
pub mod template;
pub mod uninstall;
pub mod unlink;
pub mod update;

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
    /// Remove cascade artifacts; optionally restore archived tools and delete ~/.cascade/.
    Uninstall(uninstall::UninstallArgs),
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
    /// Manage Cascade context templates (list, apply, diff, upgrade).
    Template(template::TemplateArgs),
    /// MCP server token and client setup.
    Mcp(mcp::McpArgs),
    /// Generate harness-native instruction files (CLAUDE.md, AGENTS.md, settings.json).
    GenerateInstructions(generate_instructions::GenerateInstructionsArgs),
    /// Configure OpenCode: MCP wiring + per-project instructions injection.
    SetupOc(setup_oc::SetupOcArgs),
    /// Launch a CC or OC subprocess targeting a repo with optional context injection.
    Dispatch(dispatch::DispatchArgs),
    /// Watch OpenCode session logs and append assistant turns to .cascade/oc-session-log.md.
    MonitorOc(monitor_oc::MonitorOcArgs),
    /// Show AI harness detection status.
    #[command(hide = true)]
    Harness(harness::HarnessArgs),
    /// Ping the cascade daemon (hidden diagnostic).
    #[command(hide = true)]
    Ping(ping::PingArgs),
    /// Manage installed WASM plugins.
    Plugin(plugin::PluginArgs),
    /// Inspect and clear daemon caches.
    Cache(cache::CacheArgs),
    /// Manage pre-update snapshots (list, apply).
    Rollback(rollback::RollbackArgs),
    /// Check for and apply daemon updates.
    Update(update::UpdateArgs),
    /// Manage context fingerprints for cross-session dedup (T-P4-E04-21).
    Context(context::ContextArgs),
    /// Manage guardrail policies (eval / list / add / remove).
    Policy(policy::PolicyArgs),
    /// Manage the AI folder preference (.cascade, .claude, .codex, or custom).
    Folder(folder::FolderArgs),
    /// List, download, and remove local LLM model weights.
    Models(models::ModelsArgs),
    /// Manage AI provider credentials (add / list / remove / test).
    Provider(provider::ProviderArgs),
    /// Post-init healthcheck gate — verify a cascade setup is fully operational.
    Verify(verify::VerifyArgs),
    /// Submit directives to the CEO/Founder AI orchestrator.
    Ceo(ceo::CeoArgs),
    /// List detected AI coding subscriptions and their auth status (E-P7-06).
    Subs(subs::SubsArgs),
    /// Phase-Based Development engine (phases, epics, waves, sprints, tickets, steps).
    Pbd(pbd::PbdArgs),
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
            Commands::Uninstall(args) => args.run().await,
            Commands::Migrate(args) => args.run().await,
            Commands::MigrateKeys(args) => args.run().await,
            Commands::Doctor(args) => args.run().await,
            Commands::Daemon(args) => args.run().await,
            Commands::Completions(args) => args.run().await,
            Commands::Backup(args) => args.run().await,
            Commands::Restore(args) => args.run().await,
            Commands::Template(args) => args.run().await,
            Commands::Mcp(args) => args.run().await,
            Commands::GenerateInstructions(args) => args.run().await,
            Commands::SetupOc(args) => args.run().await,
            Commands::Dispatch(args) => args.run().await,
            Commands::MonitorOc(args) => args.run().await,
            Commands::Harness(args) => args.run().await,
            Commands::Ping(args) => args.run().await,
            Commands::Plugin(args) => args.run().await,
            Commands::Cache(args) => args.run().await,
            Commands::Rollback(args) => args.run().await,
            Commands::Update(args) => args.run().await,
            Commands::Context(args) => args.run().await,
            Commands::Policy(args) => args.run().await,
            Commands::Folder(args) => args.run().await,
            Commands::Models(args) => args.run().await,
            Commands::Provider(args) => args.run().await,
            Commands::Verify(args) => args.run().await,
            Commands::Ceo(args) => args.run().await,
            Commands::Subs(args) => args.run().await,
            Commands::Pbd(args) => args.run().await,
        }
    }
}

/// Shared trait for all command handlers.
#[async_trait]
pub trait Command {
    /// Execute the command. Returns `Ok(())` on success.
    async fn run(&self) -> Result<()>;
}
