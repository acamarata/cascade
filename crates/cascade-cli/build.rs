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
/// Hidden commands (Ping) are intentionally omitted — completion scripts
/// should not expose internal commands.  Keep this list in sync with the
/// Commands enum in cmd/mod.rs.
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
    /// Migrate a legacy `.claude/` or `.opencode/` directory to `.cascade/`.
    Migrate,
    /// Diagnose cascade health and report issues.
    Doctor,
    /// Control the cascade background daemon.
    Daemon,
    /// Print shell completion script to stdout.
    Completions,
    /// Backup snapshots (list/restore).
    Backup,
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
