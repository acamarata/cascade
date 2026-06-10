# Stack Template: Rust Binary (CLI)

**Tier:** APC · **Stack:** Rust CLI binary · **Language:** Rust 2021 edition

## Idiomatic Layout

```
src/
  main.rs               # Entry point: parse CLI args, dispatch to commands
  cli.rs                # Clap command/subcommand definitions
  commands/
    {cmd}.rs            # One file per subcommand; calls services
  services/             # Business logic (testable, no CLI coupling)
  config.rs             # Config struct; loaded from file + env + flags
  error.rs              # Unified error type (thiserror/anyhow)
  output.rs             # Output formatting (table, JSON, plain)
tests/                  # Integration tests (CLI invocation via assert_cmd)
benches/
Cargo.toml
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- `main.rs` is thin: parse args → dispatch command → handle top-level error
- Commands call services; services contain all business logic; commands do I/O only
- Config layering: file → env vars → CLI flags (later wins)
- Output: `--json` flag for machine-readable output; human-readable default
- Shell completions: generated via clap_complete, committed to `completions/`

## Key Commands

```bash
cargo build --release   # Optimized binary
cargo test              # All tests
cargo clippy -- -D warnings   # Lint (fail on warnings)
cargo fmt --check       # Format check
cargo run -- <args>     # Run in dev mode
cargo install --path .  # Local install
```

## Engineering Rules

- Clap: derive-based API (`#[derive(Parser, Subcommand)]`); structured help strings
- Error display: user-facing errors via `anyhow` with `.context()`; internal errors via `thiserror`
- Exit codes: 0 success, 1 general error, 2 CLI usage error
- Release profile: `[profile.release]` with `lto = true`, `codegen-units = 1`
- File ceiling: ≤400 lines per .rs file; commands ≤150 lines (logic in services)

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/unit-header-comment-standard.md`
- `.cascade/rules/version-release-lock.md`
