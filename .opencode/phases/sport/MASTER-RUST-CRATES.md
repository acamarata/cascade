# MASTER-RUST-CRATES.md — Cascade Rust Workspace Crates (alias)

**Purpose:** Registry of every Rust crate in the Cascade workspace. Alias for MASTER-CRATES.md with expanded provider detail.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3/P4 plan

| Crate | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| cascade-types | crates/cascade-types | Shared type definitions: CacheModel, P2 schema, TemplateTier, IPC types, MCP types | 🔲 Planned | P2/P3/P4 | T-P2-E01-*, T-P4-E03-* |
| cascade-core | crates/cascade-core | Core resolution, file-watcher, state, config, embed, search dispatch | 🔲 Planned | P2/P3 | T-P2-E01-*, T-P3-E01-* |
| cascade-cli | crates/cascade-cli | CLI binary: all subcommands, completions, UX | 🔲 Planned | P2/P3 | T-P2-E02-*, T-P3-E01-* |
| cascade-daemon | crates/cascade-daemon | Tokio runtime daemon: supervisor, poller, IPC server, HTTP server | 🔲 Planned | P2/P3 | T-P2-E01-*, T-P3-E01-* |
| cascade-rag | crates/cascade-rag | Local RAG engine: embedding, indexing, RRF search, SQLite shards | 🔲 Planned | P4 | T-P4-E02-* |
| cascade-mcp | crates/cascade-mcp | MCP server + client: JSON-RPC, transports, primitives | 🔲 Planned | P4 | T-P4-E03-* |
| cascade-plugins | crates/cascade-plugins | WASM plugin system: host, sandbox, manifest loader | 🔲 Planned | P4 | T-P4-E04-* |
| cascade-providers | crates/cascade-providers | AI provider adapters: Anthropic, OpenAI, Gemini, Ollama, OpenRouter, DeepSeek | 🔲 Planned | P3 | T-P3-E05-* |
| cascade-local-llm | crates/cascade-local-llm | Local LLM runner: llama.cpp / candle binding, model management | 🔲 Planned | P3/P4 | T-P3-E05-* |
| cascade-tray | crates/cascade-tray | NSStatusItem menubar tray app (macOS), cross-OS tray abstraction | 🔲 Planned | P2 | T-P2-E04-* |
