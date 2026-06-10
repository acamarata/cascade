# Stack Template: Rust Crate (Library)

**Tier:** APC · **Stack:** Rust library crate (published to crates.io) · **Language:** Rust 2021 edition

## Idiomatic Layout

```
src/
  lib.rs                # Crate root; public API surface (re-exports only)
  error.rs              # Unified error type (thiserror)
  types.rs              # Shared types used across modules
  {module}/
    mod.rs              # Module public API
    {impl}.rs           # Implementation files
tests/                  # Integration tests (black-box, crate boundary)
  integration_test.rs
benches/                # Criterion benchmarks
  bench_main.rs
examples/               # Runnable examples (cargo run --example)
  basic.rs
docs/                   # Extended documentation (linked from rustdoc)
Cargo.toml
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- Public API: explicit `pub use` in `lib.rs`; never make internals public accidentally
- Error types: one unified `Error` enum per crate via `thiserror`; no `Box<dyn Error>` in public API
- Traits for abstraction: define trait in `lib.rs`, implement in modules
- Feature flags: optional dependencies behind `[features]` in Cargo.toml; document each feature
- Inline unit tests in `#[cfg(test)]` blocks; integration tests in `tests/`

## Key Commands

```bash
cargo build             # Build
cargo test              # All tests (unit + integration + doctests)
cargo test --doc        # Doc tests only
cargo clippy -- -D warnings   # Linter (fail on warnings in CI)
cargo fmt --check       # Format check
cargo doc --open        # Build and view docs
cargo bench             # Benchmarks
cargo publish --dry-run # Pre-publish check
```

## Engineering Rules

- Edition 2021; `rust-edition = "2021"` in Cargo.toml
- MSRV declared in Cargo.toml `rust-version` field; CI tests against MSRV
- Zero `unsafe` blocks without a `// SAFETY:` comment explaining invariants
- All public items have rustdoc with `# Examples` section
- File ceiling: ≤500 lines per .rs file; split by concern beyond limit

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/unit-header-comment-standard.md`
- `.cascade/rules/version-release-lock.md`
