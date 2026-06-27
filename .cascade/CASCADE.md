<!-- cascade:applied { id="prc-default", version="1.0.0", applied_at="2026-06-10T00:00:00Z" } -->

# Cascade PRC — Per-Repo Instructions

This file is the Per-Repo Cascade (PRC) tier for the `cascade` repository itself.
It is read by `cascaded` and `cascade resolve` when CWD is inside this repo.
Inherits from GCI → APC → PPC. Rules here are specific to this repo.

## Repo Purpose

Name: cascade
Path: `/home/user/projects/acamarata/cascade/`
Purpose: FOSS multi-agent context-cascade tool for AI coding agents (Claude Code,
OpenCode, Codex, Cursor, Aider, Windsurf). Resolves a tiered instruction cascade
(GCI→PCI→APC→PPC→PRC→PAC), manages AI harnesses/accounts/subscriptions, and
provides RAG (BGE-M3 + FTS5 + RRF), sub-agent routing, and context compression.
Primary language: Rust (+ TypeScript/React for Tauri GUI)
Visibility: Private (push only at end-of-phase with user authorization)

## Tech Stack

| Layer | Technology | Version |
|---|---|---|
| Daemon/CLI | Rust (Tokio async) | 1.85+ (rust-toolchain.toml) |
| Desktop GUI | Tauri 2 + React + Vite + TypeScript + Tailwind + shadcn | Tauri ^2 |
| RAG engine | BGE-M3 via fastembed ONNX + FTS5 + sqlite-vec + RRF | workspace |
| Plugin system | wasmtime (WASM, capability-gated) | 22 |
| Key storage | OS keychain via `KeyStorage` trait | macOS Keychain / Linux Secret Service |
| IPC | Unix socket JSON-RPC 2.0 | — |
| Build tool | cargo workspace | resolver = "2" |
| JS package mgr | pnpm | (pnpm-workspace.yaml) |
| Test runner | cargo test + assert_cmd | — |
| Formatter | rustfmt (`cargo fmt`) | — |
| Linter | clippy (`-D warnings`) | — |

## Workspace Layout

```
cascade/
  crates/
    cascade-types/     # shared traits + types (no deps on other crates)
    cascade-core/      # resolution, templates, memory, RAG core logic
    cascade-cli/       # `cascade` binary (Clap v4 derive)
    cascade-daemon/    # `cascaded` daemon (Tokio, Unix socket IPC)
    cascade-rag/       # RAG pipeline (BGE-M3 + FTS5 + sqlite-vec + RRF)
    cascade-mcp/       # MCP server + client (Unix socket transport)
    cascade-harness/   # harness detection + policy enforcement
    cascade-keychain/  # OS keychain abstraction (KeyStorage trait)
    cascade-audit/     # audit log writer
    cascade-providers/ # AI provider registry (Anthropic, OpenAI, Gemini, Ollama)
    cascade-local-llm/ # local model runner (Ollama + Mellum2)
    cascade-pdk/       # WASM plugin development kit
    cascade-pdk-macro/ # proc-macro for plugin interface derive
    cascade-plugins/   # plugin host (wasmtime)
    cascade-tray/      # system tray integration
  apps/
    cascade-app/       # Tauri 2 desktop GUI (src-tauri/ + src/ React)
  plugins/             # built-in WASM plugins (github-issues, linear, etc.)
  examples/plugins/    # plugin examples (hello-world, echo-tool, clock-widget)
  data/templates/      # bundled cascade tier/stack/shape templates
  src/                 # legacy shell tools (non-destructive sequencing)
  .cascade/            # repo's own cascade context (this dir; PRC tier)
  .cascade/CASCADE.md  # this file
```

## Key Decisions (ADR-style)

- **Rust daemon:** `cascaded` uses Tokio for async I/O; all heavy work is
  off-thread. Chosen over Go/Node for zero-GC, safe concurrency, and WASM host
  support in the same process via wasmtime.
- **WASM plugins (capability-gated):** plugins run in wasmtime sandbox with
  explicit capability grants (file read, network, keychain). No native code in
  plugins.
- **RRF RAG:** Reciprocal Rank Fusion merges BM25 (FTS5) and vector (BGE-M3 +
  sqlite-vec) results. Chosen over single-signal retrieval for recall+precision
  balance without a heavy reranker at query time.
- **MCP via Unix socket:** `cascaded` exposes an MCP-compatible endpoint over
  a Unix socket at `~/.cascade/mcp.sock`. Avoids HTTP server overhead; works
  natively with Claude Code's MCP stdio bridge.
- **KeyStorage trait in cascade-types:** breaks the E7↔E10 dependency cycle;
  concrete implementations live in cascade-keychain.
- **Tauri 2 + React/Vite:** desktop GUI wraps the same React SPA used by the
  web preview. Tauri IPC calls into cascaded via the sidecar pattern.
- **Non-destructive sequencing (ADR-014):** legacy shell tools in `src/` stay
  until Rust replacements pass QA. Never delete first.
- **cascade is in ALLOW_AI_TERMS_REPOS:** `cascade`, `Claude`, `cascaded`
  references are legitimate in this codebase; pre-commit AI-term hook is
  allowlisted.

## Active Phase Context (P4 — RAG, MCP & Plugin Ecosystem)

P4 has 6 epics / 140 tickets. Current branch: `wip/rust-rewrite`.

| Epic | Title | Status |
|---|---|---|
| E-01 | RAG pipeline (BGE-M3 + FTS5 + RRF) | Complete |
| E-02 | MCP server + client | Complete |
| E-03 | WASM plugin system | Complete |
| E-04 | Cache, rollback, context dedup | In progress |
| E-05 | Cascade self-host + dogfooding | In progress |
| E-06 | Policy + dispatch enforcement | Pending |

## Dev Setup

```bash
# Build all crates
cargo build

# Build + run the CLI
cargo run -p cascade-cli -- --help

# Run the daemon (dev mode)
cargo run -p cascade-daemon

# Run tests (all crates)
cargo test

# Lint
cargo clippy -- -D warnings

# Format
cargo fmt

# Tauri desktop app (requires pnpm + Rust toolchain)
pnpm install
pnpm --filter cascade-app tauri dev
```

Default local model: `qwen2.5-coder:7b` (Ollama). Fallback: `llama3.2:3b`.

## Testing Convention

- Unit tests: co-located inside crate modules (`#[cfg(test)]` blocks).
- Integration tests: per-crate `tests/` directory.
- End-to-end: `crates/cascade-mcp/tests/` (MCP protocol) and
  `crates/cascade-daemon/tests/` (indexing).
- Run before merge: `cargo test --workspace` must pass with zero failures.
- Clippy must exit 0 with `-D warnings`.

## Known Gotchas

- **macOS sandbox:** Tauri apps require explicit `NSAppleEventsUsageDescription`
  and other entitlements in `apps/cascade-app/src-tauri/` for file-watcher and
  keychain access.
- **File-watcher false positives on Linux:** `notify` (inotify backend) emits
  spurious `Modify` events on some ext4 filesystems. Debounce with a 200 ms
  quiet-period before triggering re-index.
- **wasmtime ABI:** plugin WIT bindings must be regenerated after any change to
  `cascade-pdk/wit/*.wit`; run `cargo build -p cascade-pdk` to trigger the
  proc-macro re-expansion.
- **sqlite-vec + rusqlite bundled:** using `rusqlite` with the `bundled` feature
  and `sqlite-vec` extension together requires the extension to be loaded after
  the connection opens. See `cascade-rag/src/db/` for the load sequence.
- **IPC socket path:** `~/.cascade/cascaded.sock` — if the daemon crashes, the
  stale socket file prevents restart. Remove it with `cascade daemon stop` or
  `rm ~/.cascade/cascaded.sock`.
- **`cargo build` from repo root:** always build from the workspace root, not
  from individual crate dirs, to ensure workspace-level dependency resolution.

## Commit Convention

Format: `{type}({scope}): {summary}`

Types: `feat` | `fix` | `refactor` | `test` | `docs` | `chore` | `ci`

Scope examples: `rag`, `mcp`, `daemon`, `cli`, `plugins`, `types`, `core`

Example: `feat(rag): add RRF merge for FTS5+vector results`

Keep summary under 72 characters. Reference the task ID (e.g. `T-P4-E05-19`) in
the commit body when working from a formal task queue.

## Key Files

| File | Purpose |
|---|---|
| `Cargo.toml` | Workspace root — workspace members + shared deps |
| `crates/cascade-types/src/lib.rs` | All shared traits and types |
| `crates/cascade-core/src/resolution.rs` | Tier resolver (walks CWD upward) |
| `crates/cascade-cli/src/cmd/mod.rs` | All CLI subcommand registrations |
| `crates/cascade-daemon/src/main.rs` | Daemon entry point |
| `crates/cascade-rag/src/lib.rs` | RAG pipeline public API |
| `crates/cascade-mcp/src/tool.rs` | MCP tool definitions |
| `.cascade/CASCADE.md` | This file — PRC tier for the cascade repo |
| `data/templates/tier/prc-default.md` | Default PRC template |
| `research.md` | Provider/model research (authoritative for provider layer) |
