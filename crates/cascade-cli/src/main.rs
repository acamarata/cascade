//! cascade — command-line interface for the Cascade AI context framework.
//!
//! # Commands
//!
//! | Command | Description |
//! |---------|-------------|
//! | `init [tier]` | Scaffold `.cascade/` at the detected or specified tier |
//! | `status` | Report daemon health, index state, and cascade tier health |
//! | `resolve` | Print the merged cascade for the current working directory |
//! | `search <query>` | Run a RAG search against the active index |
//! | `inbox list/send/archive` | Manage `.cascade/inbox/` messages |
//! | `memory read/write` | Read or write `.cascade/memory/` files |
//! | `config get/set/list` | Manage cascade configuration values |
//! | `link --tool` | Create tool-specific symlink (CLAUDE.md, AGENTS.md, …) |
//! | `unlink --tool` | Remove a tool-specific symlink |
//! | `migrate` | Migrate a legacy `.claude/` or `.opencode/` directory |
//! | `doctor` | Diagnose cascade health and report issues |
//! | `daemon start/stop/restart/status` | Control the background daemon |
//!
//! # Usage
//!
//! ```text
//! cascade init              # auto-detect tier
//! cascade init gci          # force GCI tier at ~/.cascade/
//! cascade resolve           # print merged cascade to stdout
//! cascade resolve --json    # machine-readable JSON
//! cascade status            # daemon + index + tier health
//! cascade doctor            # full diagnostic report
//! cascade doctor --fix      # auto-repair safe issues
//! ```
//!
//! # Verbosity
//!
//! Pass `--verbose` once for `DEBUG`, twice for `TRACE`. Default level is
//! `WARN`. The `RUST_LOG` environment variable overrides the flag.

use clap::Parser;

mod cmd;
mod ipc_client;

use cmd::Cli;

#[tokio::main]
async fn main() {
    let cli = Cli::parse();

    // Initialise tracing subscriber. RUST_LOG env var overrides --verbose.
    let level = match cli.verbose {
        0 => tracing::Level::WARN,
        1 => tracing::Level::DEBUG,
        _ => tracing::Level::TRACE,
    };
    tracing_subscriber::fmt()
        .with_max_level(level)
        .with_target(false)
        .compact()
        .init();

    if let Err(e) = cli.command.run().await {
        eprintln!("error: {e}");
        std::process::exit(1);
    }
}
