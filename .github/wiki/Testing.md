# Testing

This page describes how to run the Cascade test suite, what the CI checks cover, and how to add tests for new code.

---

## Running the tests

```bash
# All unit and integration tests
cargo test

# Faster with nextest (recommended for local dev)
cargo install cargo-nextest   # one-time install
cargo nextest run

# One crate
cargo test -p cascade-rag

# One specific test
cargo test -p cascade-cli cmd::init::tests::test_init_creates_directory

# With log output visible
RUST_LOG=debug cargo test -- --nocapture
```

---

## Local dev on an external volume: full-workspace test hang

If your checkout lives on an external/USB volume (e.g. macOS with the repo under
`/Volumes/<disk>/...`), `cargo nextest run --workspace` and `cargo test --workspace`
can hang for many minutes during the link step, with no CPU usage and no output —
the linker process sits in macOS's uninterruptible-sleep I/O wait state and even
`kill -9` won't clear it immediately.

**Root cause:** `cascade-daemon` (a workspace member, so it's built by any
`--workspace` command) enables `cascade-rag`'s `fastembed` + `reranker` features
by default (see `crates/cascade-daemon/Cargo.toml`). Those features pull in the
`fastembed` crate, which bundles ONNX runtime and model binaries — producing
large static-link artifacts. When `CARGO_TARGET_DIR` (Cargo's default `target/`,
under the repo root) lives on a USB-attached external volume, the final link step
against those large objects is disk-I/O-bound at USB speeds and can stall for
much longer than local SSD linking, especially when the internal boot volume is
also near capacity (check with `df -h /`).

CI is not affected — GitHub-hosted runners always build on local (fast) disk.
This is a local-machine, external-volume-only issue.

**Working recipe (three options, in order of preference):**

1. **Relocate the target dir to an internal volume** (recommended — biggest win,
   no source changes):

   ```bash
   export CARGO_TARGET_DIR="$HOME/.cargo-target/cascade"
   cargo nextest run --workspace
   ```

   Add the export to your shell profile so it's automatic for this checkout.
   The linker now writes/reads its intermediate and final artifacts on fast
   internal storage instead of over USB.

2. **Cap parallel build jobs** if you still see stalls (reduces concurrent I/O
   pressure against the external volume during linking):

   ```bash
   export CARGO_BUILD_JOBS=4   # tune down from the default (num CPUs)
   ```

3. **Test without the heavy RAG embedding path** when you don't need it, by
   scoping to specific packages instead of `--workspace`:

   ```bash
   cargo nextest run -p cascade-rag --no-default-features
   cargo nextest run --workspace --exclude cascade-daemon
   ```

Combine (1) and (2) for the most reliable full-workspace run on an external
volume. `.config/nextest.toml`'s `slow-timeout` (30s/3 periods) still applies as
a safety net for individual hung tests, but it cannot recover a stalled linker
process running before any test binary starts — that's what this recipe fixes.

---

## Test structure

Each crate has two types of tests:

**Unit tests** - live inside the source file, in a `#[cfg(test)]` module. They test a single function or struct in isolation.

**Integration tests** - live in `crates/<crate>/tests/`. They test the crate's public API, often spinning up a temporary daemon or database.

```
crates/
└── cascade-rag/
    ├── src/
    │   ├── ingest.rs            # unit tests inside
    │   └── lib.rs
    └── tests/
        ├── e01_round_trip.rs    # integration: ingest + search round trip
        └── integration_test.rs  # broader integration scenarios
```

---

## CI checks

Every pull request and push to `main` runs these jobs:

| Job | What it does |
|---|---|
| `build` | `cargo build --workspace` on Ubuntu and macOS |
| `test` | `cargo nextest run --workspace` |
| `clippy` | `cargo clippy -- -D warnings` |
| `fmt` | `cargo fmt --check` |
| `bench-check` | Criterion benchmarks - fails if > 20% regression |
| `e2e-mcp` | End-to-end MCP test: starts daemon, runs protocol checks |
| `e2e-cli` | End-to-end CLI test: installs binary, runs through core commands |

The `bench-check` job uses the Criterion `--load-baseline` flag to compare against the stored baseline. A regression triggers a failing annotation but not an immediate block; the maintainer reviews before merging.

---

## End-to-end tests

The E2E tests start a real daemon process, run CLI commands, and check output. They live in:

- `crates/cascade-mcp/tests/e2e_mcp.rs`
- `crates/cascade-daemon/tests/integration_indexing.rs`

They use `tempdir` for isolation. Each test creates a clean `~/.cascade/` in a temp directory.

Run the E2E tests:

```bash
cargo test -p cascade-mcp --test e2e_mcp
cargo test -p cascade-daemon --test integration_indexing
```

Note: E2E tests start the daemon, so they take longer than unit tests (10–30 seconds each). They are excluded from the default `cargo test` run on CI to keep the standard test job fast; the CI pipeline runs them in a separate job.

---

## Writing tests

**Unit test example:**

```rust
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_chunk_splits_on_heading() {
        let input = "# Section A\n\nContent A.\n\n# Section B\n\nContent B.";
        let chunks = chunk_by_heading(input);
        assert_eq!(chunks.len(), 2);
        assert!(chunks[0].contains("Content A"));
    }
}
```

**Integration test example:**

```rust
// crates/cascade-rag/tests/e01_round_trip.rs
use cascade_rag::ingest;
use tempfile::TempDir;

#[tokio::test]
async fn ingest_and_search_finds_document() {
    let dir = TempDir::new().unwrap();
    let db_path = dir.path().join("test.db");
    // ... setup, ingest, search, assert
}
```

**Rules for new tests:**
- Every new public function needs at least one unit test.
- Every new CLI subcommand gets an integration test that exercises the happy path.
- Tests must not rely on global state. Use `tempfile::TempDir` for filesystem isolation.
- Tests must not make network calls. Stub any external service with a trait mock.

---

## Coverage

Run coverage locally with `cargo-llvm-cov`:

```bash
cargo install cargo-llvm-cov
cargo llvm-cov --workspace --html
open target/llvm-cov/html/index.html
```

CI uploads coverage to Codecov. The current per-crate targets:

| Crate | Statements | Branches | Functions |
|---|---|---|---|
| `cascade-types` | ≥ 90% | ≥ 85% | ≥ 95% |
| `cascade-rag` | ≥ 80% | ≥ 75% | ≥ 85% |
| `cascade-cli` | ≥ 80% | ≥ 75% | ≥ 85% |
| `cascade-daemon` | ≥ 80% | ≥ 75% | ≥ 85% |

---

## Benchmarks

Benchmarks use Criterion.rs and live in `bench/` and `crates/cascade-rag/benches/`.

```bash
# Run all benchmarks
cargo bench

# Run one benchmark group
cargo bench -p cascade-rag -- ingest

# Save as new baseline
cargo bench -- --save-baseline current
```

See [Performance](cascade-performance.md) for benchmark results and the regression gate details.

---

See also: [Development Setup](Development-Setup.md) · [Contributing](Contributing.md) · [Performance](cascade-performance.md)
