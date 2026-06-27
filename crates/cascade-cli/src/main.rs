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
mod daemon_install;
mod ipc_client;
mod telemetry;

use cmd::Cli;

#[tokio::main]
async fn main() {
    let cli = Cli::parse();

    // Read the global config to determine whether OTLP telemetry export is
    // enabled. Errors reading or parsing config are silently ignored — CLI
    // must always start regardless of config state.
    let global_config: Option<cascade_types::config::CascadeConfig> = (|| {
        let home = std::env::var("HOME").ok()?;
        let path = std::path::PathBuf::from(home)
            .join(".cascade")
            .join("config.toml");
        let raw = std::fs::read_to_string(path).ok()?;
        toml::from_str(&raw).ok()
    })();

    // Initialise optional OTLP tracer provider. Gated on telemetry.enabled;
    // the provider must live until the end of main to flush pending spans.
    let _otel_provider = if global_config
        .as_ref()
        .map(|c| c.telemetry.enabled)
        .unwrap_or(false)
    {
        let endpoint = global_config
            .as_ref()
            .and_then(|c| c.telemetry.endpoint.as_deref());
        telemetry::init_cli_tracing(endpoint)
    } else {
        None
    };

    // Initialise the global tracing subscriber (stderr compact + optional OTel
    // layer). RUST_LOG overrides the default WARN level.
    telemetry::init_cli_logging(_otel_provider.as_ref());

    if let Err(e) = cli.command.run().await {
        eprintln!("error: {e}");
        std::process::exit(1);
    }
}
