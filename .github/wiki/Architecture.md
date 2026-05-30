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
│  │  - BGE-M3 embedding (local, via ONNX)                  │  │
│  │  - RRF fusion (hybrid dense + sparse retrieval)        │  │
│  │  - Chunker, retriever, reranker traits                 │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

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

- A vector index (`sqlite-vec`) for dense semantic search using BGE-M3 embeddings, computed locally via ONNX Runtime.
- An FTS5 full-text index for keyword search.

Queries combine both via Reciprocal Rank Fusion (RRF), which tends to outperform either approach alone on mixed queries.

The crate exposes trait objects (`Chunker`, `Retriever`, `EmbeddingProvider`, `Reranker`) so the embedding and chunking strategies can be swapped without changing the query logic.

### cascade-mcp

An MCP-compatible server that wraps `cascade-core` and `cascade-rag`. Supports three transport modes: stdio (for tools that spawn a subprocess), SSE (for tools that connect over HTTP), and Unix socket. Tools connect to this server to query for context rather than reading raw files.

### cascade-cli

The `cascade` binary. Thin wrapper around the daemon IPC and the library crates. Subcommands: `init`, `edit`, `sync`, `search`, `serve`, `status`, `doctor`. See [CLI Reference](CLI-Reference.md) for details.

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
