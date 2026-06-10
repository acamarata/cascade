---
id = "rust-binary"
version = "1.0.0"
tier = "any"
stacks = ["rust-binary"]
project_shapes = []
description = "Rust binary crate conventions: Clap v4, tracing, Tokio async runtime, file layout, and common pitfalls."
---

## File Layout

```
src/
  main.rs             # entry point — parse CLI, set up runtime, delegate to run()
  cli.rs              # Clap derive structs — all arg definitions
  commands/           # one file per subcommand
    mod.rs
    init.rs
    run.rs
    status.rs
  config.rs           # config file loading + validation
  error.rs            # top-level error type (thiserror)
  lib.rs              # optional — expose core logic for integration tests
tests/
  integration.rs      # CLI invocation tests via assert_cmd
Cargo.toml
```

Keep `main.rs` to three responsibilities: parse CLI, initialise tracing, call `run()`. Business logic belongs in `commands/` or (if shared with tests) in `lib.rs`.

## Build Tooling

```bash
cargo build                          # debug binary
cargo build --release                # release binary (stripped symbols by default via profile)
cargo run -- <args>                  # run in dev
cargo run --release -- <args>        # run release build
cargo test                           # unit + integration tests
cargo clippy -- -D warnings          # lints as errors
cargo fmt --check                    # formatting CI gate
```

Add a `[profile.release]` section to `Cargo.toml`:

```toml
[profile.release]
strip = true          # strip debug symbols from the binary
lto = "thin"          # Link-Time Optimisation for smaller binary
codegen-units = 1     # better optimisation at cost of slower compile
```

## CLI with Clap v4

Use Clap's `derive` feature for all CLI definitions. Never build `Command` structs manually — the derive macros are the canonical pattern.

```rust
// src/cli.rs
use clap::{Parser, Subcommand};

/// Brief description of what this tool does.
#[derive(Debug, Parser)]
#[command(name = "mytool", version, about, long_about = None)]
pub struct Cli {
    /// Increase verbosity (-v, -vv, -vvv)
    #[arg(short, long, action = clap::ArgAction::Count)]
    pub verbose: u8,

    /// Path to config file
    #[arg(short, long, default_value = "config.toml")]
    pub config: String,

    #[command(subcommand)]
    pub command: Commands,
}

#[derive(Debug, Subcommand)]
pub enum Commands {
    /// Initialise a new project
    Init {
        /// Project name
        name: String,
    },
    /// Run the main operation
    Run {
        /// Optional target path
        path: Option<String>,
    },
}
```

Use `#[command(version)]` (derives from Cargo.toml) and `#[command(about)]` (uses the first line of the doc comment). Never hardcode version strings.

## Async with Tokio

```rust
// src/main.rs
use clap::Parser;
use tracing_subscriber::EnvFilter;

mod cli;
mod commands;
mod config;
mod error;

use cli::Cli;
use error::AppError;

#[tokio::main]
async fn main() -> Result<(), AppError> {
    let cli = Cli::parse();

    // Initialise tracing before any async work.
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::from_default_env()
                .add_directive(verbosity_to_level(cli.verbose).into()),
        )
        .init();

    run(cli).await
}

async fn run(cli: Cli) -> Result<(), AppError> {
    match cli.command {
        cli::Commands::Init { name } => commands::init::run(name).await,
        cli::Commands::Run { path } => commands::run::run(path).await,
    }
}
```

Use `#[tokio::main]` only in `main.rs`. All other async code takes `&tokio::runtime::Handle` or is called from within the runtime context. Do not call `tokio::runtime::Runtime::block_on` inside library functions.

## Structured Logging with tracing

```rust
// src/commands/run.rs
use tracing::{debug, error, info, instrument, warn};

#[instrument(skip(config), fields(path = %path.display()))]
pub async fn run(path: std::path::PathBuf) -> Result<(), crate::error::AppError> {
    info!("starting run");
    debug!(config = ?config, "loaded config");

    match do_work(&path).await {
        Ok(n) => {
            info!(items = n, "completed");
            Ok(())
        }
        Err(e) => {
            error!(err = %e, "run failed");
            Err(e.into())
        }
    }
}
```

Use `#[instrument]` on every significant async function. Use `skip(...)` for large or sensitive parameters. Use structured fields (`field = value`) instead of string interpolation — this enables log filtering and export to structured formats.

Configure `RUST_LOG` for log-level control. The default filter from `EnvFilter::from_default_env()` reads `RUST_LOG`. Document the expected `RUST_LOG` values in your README.

## Testing Convention

```bash
cargo test                     # all tests
cargo test --test integration  # integration tests only
```

Use `assert_cmd` + `predicates` for CLI integration tests:

```rust
// tests/integration.rs
use assert_cmd::Command;
use predicates::str::contains;

#[test]
fn help_exits_zero() {
    Command::cargo_bin("mytool")
        .unwrap()
        .arg("--help")
        .assert()
        .success()
        .stdout(contains("Usage:"));
}

#[test]
fn init_creates_config() {
    let dir = tempfile::tempdir().unwrap();
    Command::cargo_bin("mytool")
        .unwrap()
        .args(["init", "my-project"])
        .current_dir(dir.path())
        .assert()
        .success();
    assert!(dir.path().join("config.toml").exists());
}
```

Always use `tempfile::tempdir()` for tests that write to the filesystem — never use hardcoded paths or current directory.

## Common Pitfalls

- **`unwrap()` in `main`.** Use `?` or log-and-exit with a clear error message. `unwrap()` gives users a Rust backtrace — that is rarely helpful. Return `Result<(), AppError>` from `main` instead.
- **Not flushing tracing on exit.** Some tracing subscribers buffer output. On normal `Ok(())` exit the runtime flushes automatically, but on panic the buffer may be lost. Add a `#[tokio::test]` on critical path tests to catch this.
- **Tokio runtime in tests.** Use `#[tokio::test]` for async test functions. Do not create a manual runtime in tests — it interacts poorly with test parallelism.
- **Clap version in `--version`.** `#[command(version)]` reads from `Cargo.toml` at compile time. If you forget to update `Cargo.toml` before tagging, the binary reports the wrong version.
- **Binary size.** A release binary that includes debug symbols can be 10× larger than stripped. Always set `strip = true` in the release profile.

## Performance Notes

For startup-sensitive CLIs, avoid large static initialisations. Lazy-initialise expensive resources (config parsing, connection pools) only when the subcommand actually needs them.

For throughput-sensitive workloads, use `tokio::spawn` for truly independent async tasks. Avoid spawning tasks just to achieve parallelism — the Tokio thread pool handles this automatically for `.await` points.

Profile with `cargo flamegraph` (requires `perf` on Linux or `dtrace` on macOS) before optimising. Never optimise without a measured baseline.
