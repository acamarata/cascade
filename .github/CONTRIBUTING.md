# Contributing to Cascade

Thanks for taking the time to contribute. This file covers everything you need to go from a fresh clone to a passing PR.

---

## Prerequisites

| Requirement | Minimum version | Notes |
|---|---|---|
| Rust (stable) | 1.85 | Install via [rustup.rs](https://rustup.rs) |
| Node.js | 20 | Required for the desktop app and dashboard |
| pnpm | 9 | `npm install -g pnpm` or `corepack enable` |
| Xcode CLI tools | any | macOS only: `xcode-select --install` |

Optional but recommended: `cargo-nextest` for faster test runs.

---

## Critical: the `CC` hijack

This repo's root scripts include a `~/bin/cc` shim that launches Claude Code, not the system C compiler. Any `cargo build` or `cargo test` in this repo will fail with a linker error unless you override `CC` first:

```bash
export CC=/usr/bin/clang
```

Set this in your shell profile or run it before any Cargo command. The workaround is documented here so it does not surprise contributors building from source.

---

## Clone and set up

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade

# Override CC before any cargo call
export CC=/usr/bin/clang

# Install Rust dependencies (no download needed beyond what cargo fetches)
cargo fetch

# Install JS dependencies for the desktop app
pnpm --dir apps/cascade-app install
pnpm --dir apps/cascade-dashboard install
```

---

## Build

### CLI and daemon (Rust only)

```bash
export CC=/usr/bin/clang
cargo build --workspace
```

The CLI binary is at `target/debug/cascade`. The daemon is at `target/debug/cascaded`.

Release build:

```bash
cargo build --workspace --release
```

### Desktop app (Tauri)

```bash
export CC=/usr/bin/clang
cd apps/cascade-app
pnpm tauri build
```

To run the app in dev mode with hot reload:

```bash
pnpm tauri dev
```

---

## Run in dev mode

Start the daemon in the background, then run the CLI or app against it:

```bash
# Terminal 1: run the daemon (debug build, verbose logs)
export CC=/usr/bin/clang
RUST_LOG=debug ./target/debug/cascaded

# Terminal 2: CLI commands talk to the running daemon
./target/debug/cascade status
./target/debug/cascade search "auth"
```

---

## Tests

### Rust

```bash
export CC=/usr/bin/clang
cargo test --workspace
```

With nextest (faster, parallel):

```bash
cargo nextest run --workspace
```

Lint and format checks (CI enforces these):

```bash
cargo clippy --workspace --all-targets -- -D warnings
cargo fmt --all --check
```

### TypeScript (desktop app)

```bash
pnpm --dir apps/cascade-app exec vitest run
pnpm --dir apps/cascade-dashboard exec vitest run
```

---

## Project layout

```
crates/
  cascade-types/       — shared types and tier definitions
  cascade-core/        — resolver, discovery, resolution logic
  cascade-cli/         — CLI entry point and all subcommands
  cascade-daemon/      — background daemon: watcher, IPC, scheduler
  cascade-rag/         — RAG pipeline: indexer, embedder, retriever
  cascade-mcp/         — MCP server with five transports
  cascade-plugins/     — WASM plugin host and PDK
  cascade-providers/   — AI provider adapters and routing table
  cascade-local-llm/   — local model inference (candle-rs)
  cascade-keychain/    — OS keychain integration
  cascade-audit/       — tamper-evident audit log
  cascade-tray/        — system tray integration
apps/
  cascade-app/         — Tauri 2 desktop app (React + Vite + TypeScript)
  cascade-dashboard/   — browser dashboard SPA
plugins/               — first-party WASM plugins (github-issues, gitlab, jira, linear)
examples/plugins/      — annotated example plugins for contributors
data/templates/        — bundled cascade template library
```

---

## Pull request conventions

1. Fork the repo and create a branch from `main`.
2. Keep changes focused. One concern per PR.
3. Add or update tests for any behavior change.
4. Make sure the full test suite passes locally before opening the PR.
5. Sign off your commits with `git commit -s` to certify the [Developer Certificate of Origin](https://developercertificate.org/).
6. Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages: `feat:`, `fix:`, `docs:`, `chore:`, etc.
7. PRs are squash-merged. The final commit message should match the PR title.

CI runs `cargo clippy`, `cargo fmt --check`, `cargo test --workspace`, and TypeScript tests. A PR cannot merge until all checks are green.

---

## Reporting bugs

Use the issue templates. Include:

- Your OS and version
- Cascade version (`cascade --version`)
- Steps to reproduce
- Output of `cascade doctor` if the issue is with setup or config

For security vulnerabilities: do NOT open a public issue. Follow the process in [SECURITY.md](../SECURITY.md).

---

## Plugin development

Run `cascade plugin new my-plugin` to scaffold a new WASM plugin from the bundled template. See the [Plugin Development](../../wiki/Plugin-Development) wiki page for the full guide.

---

## License

By contributing, you agree that your contributions will be licensed under the MIT License. See [LICENSE](../LICENSE).
