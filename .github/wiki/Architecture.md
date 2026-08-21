# Architecture

Cascade is built as a set of cooperating components. This page describes how they fit together.

## Component overview

```
┌─────────────────────────────────────────────────────────────┐
│  User-facing surfaces                                        │
│  ┌───────────────┐  ┌──────────────────┐  ┌─────────────┐  │
│  │  Tauri app    │  │  cascade CLI     │  │  MCP server │  │
│  │  (GUI editor, │  │  (init, edit,    │  │  (resources,│  │
│  │   wizard,     │  │   search, sync,  │  │   tools,    │  │
│  │   RAG viewer) │  │   serve, status) │  │   prompts)  │  │
│  └───────┬───────┘  └────────┬─────────┘  └──────┬──────┘  │
│          │                   │                    │         │
│          └───────────────────┼────────────────────┘         │
│                              │ IPC (Unix socket / named pipe)│
│          ┌───────────────────┘                              │
│          ▼                                                   │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  cascade-daemon                                        │  │
│  │  - Cascade resolver (tier merging + conflict rules)    │  │
│  │  - Derived file writer (CLAUDE.md, AGENTS.md, etc.)    │  │
│  │  - File watcher (cascade file changes → re-derive)     │  │
│  │  - Indexer coordinator (schedules RAG index jobs)      │  │
│  └──────────────────────┬────────────────────────────────┘  │
│                         │                                    │
│          ┌──────────────┘                                    │
│          ▼                                                   │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  cascade-rag                                           │  │
│  │  - SQLite + sqlite-vec (vector store)                  │  │
│  │  - FTS5 (full-text index)                              │  │
│  │  - E5 Large embedding (local, via ONNX)                │  │
│  │  - RRF fusion (hybrid dense + sparse retrieval)        │  │
│  │  - Chunker, retriever, reranker traits                 │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

## All workspace crates

Cascade is 21 crates under `crates/`, plus the Tauri desktop app under
`apps/cascade-app/src-tauri` and the WASM plugins under `plugins/` and
`examples/plugins/`. The sections after this table go into depth on the few a
contributor meets first; this table is the complete map, so nothing is
invisible to someone reading the repo for the first time.

| Crate | Role |
|---|---|
| `cascade-agents` | Agent registry, tool grants, and agent-library schema for the Cascade orchestration runtime (E-P6-01/06/09). |
| `cascade-audit` | Append-only, hash-chained audit log for Cascade privileged operations. |
| `cascade-ccapi` | EXPERIMENTAL: HTTP+SSE bridge that drives the interactive Claude Code CLI (default-off, opt-in) |
| `cascade-cli` | CLI for Cascade — unified AI instruction manager for coding agents |
| `cascade-core` | Cascade runtime — orchestrates retrieval, chunking, and agent dispatch |
| `cascade-daemon` | Background daemon that runs the Cascade context engine and exposes a local IPC socket |
| `cascade-db` | Cascade embedded data layer: shared SQLite configuration, migration runner, embedded cache + job queue, and optional ANN vector store. Redis is never required. |
| `cascade-fleet-widget` | macOS menu-bar fleet widget — shows live per-account usage from accounts.json and quota-store.json (Claw-Fleet replacement) |
| `cascade-harness` | AI coding harness detection, configuration, and session monitoring for Cascade |
| `cascade-keychain` | Cross-platform OS keychain access for Cascade (macOS Keychain / Linux Secret Service / Windows Credential Manager). |
| `cascade-local-llm` | Local LLM runner for Cascade — candle-rs gemma-2-2b CPU/METAL inference |
| `cascade-mcp` | Model Context Protocol (MCP) server implementation for Cascade |
| `cascade-pdk` | Plugin Development Kit — guest-side bindings and macros for Cascade WASM plugins |
| `cascade-pdk-macro` | Proc-macro support for cascade-pdk — generates WASM ABI entry points |
| `cascade-personal` | Encrypted, isolated structured personal-data store for Cascade. AES-256-GCM at rest; key in OS keychain; mode-aware gate (medical/financial hidden in non-Personal mode). |
| `cascade-plugins` | WASM plugin host for Cascade — load and execute sandboxed provider plugins |
| `cascade-providers` | Provider provisioning, OAuth, and auto-auth import for the Cascade AI context framework |
| `cascade-rag` | Retrieval-Augmented Generation pipeline for Cascade — chunking, embedding, vector search |
| `cascade-security` | Security primitives shared by the daemon, CLI and MCP surfaces (secret redaction, sensitivity classification, policy hooks). |
| `cascade-tray` | OS system-tray abstraction for the Cascade daemon — platform-agnostic trait + state types |
| `cascade-types` | Core traits and types for the Cascade AI context framework |

The remaining workspace members are not library crates: `apps/cascade-app/src-tauri`
is the desktop shell, and `plugins/*` and `examples/plugins/*` are WASM plugins
built against `cascade-pdk`.

## Components

### cascade-daemon

The daemon is the system's core. It runs as a background process, started automatically on first use and managed by your OS service system (launchd on macOS, systemd or user session on Linux, Windows Service on Windows).

Responsibilities:
- Watching cascade files for changes
- Running the resolver to merge tiers
- Writing derived files to the correct paths
- Scheduling index updates

The daemon exposes a Unix socket (macOS/Linux) or named pipe (Windows) for IPC. Both the CLI and the Tauri app communicate with it through this channel.

### cascade-core

The resolver logic lives here. Given a working directory, `cascade-core` walks up the directory tree collecting cascade files at each tier, applies the conflict resolution rules, and returns a merged instruction set. The output is deterministic: the same files always produce the same result.

This crate also owns the tier taxonomy types and the file format parser.

### cascade-rag

The retrieval layer. It maintains a SQLite database (at `~/.cascade/index.db`) with two parallel indexes:

- A vector index (`sqlite-vec`) for dense semantic search using Multilingual E5 Large embeddings, computed locally via ONNX Runtime.
- An FTS5 full-text index for keyword search.

Queries combine both via Reciprocal Rank Fusion (RRF), which tends to outperform either approach alone on mixed queries.

The crate exposes trait objects (`Chunker`, `Retriever`, `EmbeddingProvider`, `Reranker`) so the embedding and chunking strategies can be swapped without changing the query logic.

### cascade-mcp

An MCP-compatible server that wraps `cascade-core` and `cascade-rag`. Supports three transport modes: stdio (for tools that spawn a subprocess), SSE (for tools that connect over HTTP), and Unix socket. Tools connect to this server to query for context rather than reading raw files.

### cascade-cli

The `cascade` binary. Thin wrapper around the daemon IPC and the library crates. Key subcommands include `init`, `status`, `resolve`, `search`, `doctor`, `verify`, `import`, `generate-instructions`, `daemon`, `mcp`, `provider`, `subs`, and `plugin`. See [CLI Reference](CLI-Reference.md) for the full list.

### Tauri app

The desktop GUI. Provides a visual editor for cascade files, a step-by-step onboarding wizard, a RAG search interface, and system tray / OS widget access. Built with Tauri 2 wrapping a React/Vite frontend. Communicates with the daemon over IPC.

## Data flow: editing a cascade file

1. User edits `~/projects/my-app/CASCADE.md` (in any editor or the Tauri app).
2. The file watcher in `cascade-daemon` detects the change.
3. The resolver re-runs the tier merge for any open tool sessions in that project.
4. The daemon writes updated derived files (e.g., `CLAUDE.md`, `.cursorrules`).
5. The indexer queues the changed file for re-embedding.
6. Within seconds, the AI tool in that project sees the updated context.

## Storage layout

```
~/.cascade/
├── CASCADE.md          # GCI (global instructions)
├── config.toml         # daemon configuration
├── index.db            # SQLite: vector index + FTS5
├── cache/              # resolved merge outputs (keyed by project path hash)
└── logs/               # daemon and indexer logs
```

Project-level cascade files live in the project directory itself (`CASCADE.md` at whatever tier is appropriate). Cascade never moves or copies them; it reads them in place.

## Dogfooding: cascade hosts its own PRC

The `cascade` repo itself uses the tool it builds. `.cascade/CASCADE.md` at the
repo root is the PRC tier for the cascade codebase. When you run `cascade resolve`
from inside this repo, `cascade-core`'s resolver walks upward and picks up that
file as the innermost tier, providing stack facts, dev commands, key decisions,
and active phase context to any AI agent working on cascade itself.

This was bootstrapped in T-P4-E05-19 (P4 E-05). The file is tracked in git via a
`.gitignore` negation (`!.cascade/CASCADE.md`). Runtime-only files (`*.json`,
`temp/`) in `.cascade/` remain gitignored.

---

See also: [Home](Home.md) · [Cascade Concepts](Cascade-Concepts.md) · [Daemon Architecture](Daemon-Architecture.md) · [RAG Pipeline](RAG-Pipeline.md)
