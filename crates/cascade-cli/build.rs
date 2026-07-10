//! Build script for cascade-cli.
//!
//! Purpose: generate shell completion scripts for all four supported shells
//! (bash, zsh, fish, powershell) and write them into `OUT_DIR/completions/`
//! so that `cmd/completions.rs` can embed them via `include_str!` at compile
//! time.  The generated filenames follow the clap_complete convention:
//!
//! | Shell      | Filename          |
//! |------------|-------------------|
//! | Bash       | cascade.bash      |
//! | Zsh        | _cascade          |
//! | Fish       | cascade.fish      |
//! | PowerShell | _cascade.ps1      |
//!
//! The CLI is reconstructed here independently (not imported from the crate
//! being compiled, which is not possible in a build script) so it must stay
//! in sync with `cmd/mod.rs`.  When adding new subcommands, update both files.
//!
//! SPORT: .claude/docs/MASTER-CLI.md — cascade completions row

use clap::{CommandFactory, Parser, Subcommand};
use clap_complete::{generate_to, Shell};
use std::{env, path::PathBuf};

// ---------------------------------------------------------------------------
// Mirror of the main CLI (build.rs cannot import from the crate under build)
// ---------------------------------------------------------------------------

/// Cascade AI context framework CLI.
#[derive(Debug, Parser)]
#[command(
    name = "cascade",
    about = "Manage the Cascade AI context framework",
    version,
    propagate_version = true
)]
struct Cli {
    #[arg(short, long, action = clap::ArgAction::Count, global = true)]
    verbose: u8,

    #[command(subcommand)]
    command: Commands,
}

/// All top-level subcommands (mirrored from cmd/mod.rs).
///
/// Hidden commands (Ping, Harness) are intentionally omitted — completion
/// scripts should not expose internal commands.  Keep this list in sync with
/// the Commands enum in cmd/mod.rs.
#[derive(Debug, Subcommand)]
enum Commands {
    /// Scaffold a `.cascade/` directory at the detected or specified tier.
    Init,
    /// Show daemon health, index state, and cascade tier summary.
    Status,
    /// Print the merged cascade for the current working directory.
    Resolve,
    /// Run a RAG search against the active index.
    Search,
    /// Manage `.cascade/inbox/` messages.
    Inbox,
    /// Read or write `.cascade/memory/` files.
    Memory,
    /// Read or write cascade configuration values.
    Config,
    /// Create a tool-specific symlink pointing to CASCADE.md.
    Link,
    /// Remove a tool-specific symlink.
    Unlink,
    /// Remove cascade artifacts; optionally restore archived tools and delete ~/.cascade/.
    Uninstall,
    /// Migrate a legacy `.claude/` or `.opencode/` directory to `.cascade/`.
    Migrate,
    /// Move `GEMINI_API_KEY_*` secrets from vault.env into the OS keychain.
    MigrateKeys,
    /// Lossless migration engine — import a legacy setup.
    Import,
    /// Diagnose cascade health and report issues.
    Doctor,
    /// Control the cascade background daemon.
    Daemon,
    /// Print shell completion script to stdout.
    Completions,
    /// Backup snapshots (list/restore).
    Backup,
    /// Restore an archived tool's files to their original paths.
    Restore,
    /// List and restore pre-generation derived-file snapshots.
    Snapshot,
    /// Manage Cascade context templates (list, apply, diff, upgrade).
    Template,
    /// MCP server token and client setup.
    Mcp,
    /// Generate harness-native instruction files.
    GenerateInstructions,
    /// Configure OpenCode: MCP wiring + per-project instructions injection.
    SetupOc,
    /// Launch a CC or OC subprocess targeting a repo with optional context injection.
    Dispatch,
    /// Watch OpenCode session logs and append assistant turns to .cascade/oc-session-log.md.
    MonitorOc,
    /// Manage installed WASM plugins.
    Plugin,
    /// Inspect and clear daemon caches.
    Cache,
    /// Manage pre-update snapshots (list, apply).
    Rollback,
    /// Check for and apply daemon updates.
    Update,
    /// Manage context fingerprints for cross-session dedup.
    Context,
    /// Manage guardrail policies (eval / list / add / remove).
    Policy,
    /// Manage the AI folder preference (.cascade, .claude, .codex, or custom).
    Folder,
    /// List, download, and remove local LLM model weights.
    Models,
    /// Manage AI provider credentials (add / list / remove / test).
    Provider,
    /// Post-init healthcheck gate — verify a cascade setup is fully operational.
    Verify,
    /// Submit directives to the CEO/Founder AI orchestrator.
    Ceo,
    /// Local content checks (e.g. injection-guard hook).
    Check,
    /// List detected AI coding subscriptions and their auth status.
    Subs,
    /// Phase-Based Development engine (phases, epics, waves, sprints, tickets, steps).
    Pbd,
    /// Run EIE engineering-excellence health checks on a project.
    Health,
    /// Manage the EXPERIMENTAL CC API proxy bridge (default-off).
    #[command(name = "ccapi")]
    CcApi,
    /// Write Cascade-managed values into a harness runtime config.
    Configure,
    /// Manage the fleet account registry (list / status / matrix / detect).
    Accounts,
    /// Manage the cascade-fleet-widget menu-bar LaunchAgent.
    Widget,
    /// Interactive first-run setup: personal_dir, projects_dir, provider key.
    Wizard,
    /// Autonomous Build engine — drive a phase to completion via EOx gates.
    Build,
    /// Manage telemetry opt-in (enable / disable / status).
    Telemetry,
    /// Export ~/.cascade/ to a portable `.cascade-archive.tar.gz`.
    Export,
    /// Security scan subcommands (secrets, client leaks, dep audit, prelaunch checklist).
    Security,
    /// Pull nSentry bug/CI/error reports from a remote ops server into the project inbox.
    Sentry,
    /// Daemon-owned multi-stream nSentry sync (status / run / pause / resume / list).
    Nsentry,
    /// Route a prompt to the best available worker account (quota-aware delegation).
    Conductor,
    /// Inspect or manually trigger the RAM Guardian (OOM-prevention subsystem).
    Ram,
    /// Manage Cascade Continuity intents (auto-resume sessions on quota reset).
    Continuity,
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

fn main() {
    // OUT_DIR is set by cargo; write completions into a subdirectory so the
    // include_str! paths in completions.rs are unambiguous.
    let out_dir = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR not set"));
    let completions_dir = out_dir.join("completions");
    std::fs::create_dir_all(&completions_dir).expect("failed to create OUT_DIR/completions/");

    let mut cmd = Cli::command();
    let binary_name = "cascade";

    for &shell in &[Shell::Bash, Shell::Zsh, Shell::Fish, Shell::PowerShell] {
        generate_to(shell, &mut cmd, binary_name, &completions_dir)
            .unwrap_or_else(|e| panic!("failed to generate {shell} completions: {e}"));
    }
}
