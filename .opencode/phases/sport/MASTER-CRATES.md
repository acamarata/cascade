# MASTER-CRATES.md — Cascade Rust Workspace Crates

**Purpose:** Registry of every Rust crate in the Cascade workspace.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-06-09 (E-03 complete)
**Source:** Cascade P2/P3/P4 plan
**CI Matrix:** ✅ 5 triples × test/lint/coverage/deny (`.github/workflows/ci.yml`, T-P2-E01-10)

| Crate | Path | Description | Status | Test Harness | Phase | Creating tickets |
|---|---|---|---|---|---|---|
| cascade-types | crates/cascade-types | Shared type definitions: CacheModel, P2 schema, TemplateTier, IPC types | 🔲 Planned | ✅ | P2/P3/P4 | T-P2-E01-* |
| cascade-core | crates/cascade-core | Core resolution, file-watcher, state, config, embed, search dispatch | 🔲 Planned | ✅ | P2/P3 | T-P2-E01-*, T-P3-E01-* |
| cascade-cli | crates/cascade-cli | CLI binary: all subcommands, completions, UX | 🔲 Planned | ✅ | P2/P3 | T-P2-E02-*, T-P3-E01-* |
| cascade-daemon | crates/cascade-daemon | Tokio runtime daemon: supervisor, poller, IPC server, HTTP server | 🔲 Planned | ✅ | P2/P3 | T-P2-E01-*, T-P3-E01-* |
| cascade-rag | crates/cascade-rag | Local RAG engine: embedding, indexing, RRF search, SQLite shards | 🔲 Planned | ✅ | P4 | T-P4-E02-* |
| cascade-mcp | crates/cascade-mcp | MCP server + client: JSON-RPC, transports, primitives | 🔲 Planned | ✅ | P4 | T-P4-E03-* |
| cascade-plugins | crates/cascade-plugins | WASM plugin system: host, sandbox, manifest loader | 🔲 Planned | ✅ | P4 | T-P4-E04-* |
| cascade-providers | crates/cascade-providers | AI provider adapters: google_oauth, google_provision, auto_auth_import; Anthropic/OpenAI/Ollama/OpenRouter planned | 🟡 Partial | 🔲 | P3 | T-P3-E03-07..08,43; T-P3-E05-* |
| cascade-local-llm | crates/cascade-local-llm | Local LLM runner: llama.cpp / candle binding, model management | 🔲 Planned | 🔲 | P3/P4 | T-P3-E05-* |
| cascade-tray | crates/cascade-tray | NSStatusItem menubar tray app (macOS), cross-OS tray abstraction | 🔲 Planned | 🔲 | P2 | T-P2-E04-* |
| cascade-app/scanner | apps/cascade-app/src-tauri/src/scanner/ | Rust module: scan_global_homes + scan_dev_tree for legacy .claude dirs | ✅ Done | ✅ | P3 | T-P3-E03-14..16 |
| cascade-app/archive | apps/cascade-app/src-tauri/src/archive/ | Rust module: archive_preflight, archive_legacy_tools, manifest r/w, restore_tool | ✅ Done | ✅ | P3 | T-P3-E03-24..26,33 |
| cascade-app/merge | apps/cascade-app/src-tauri/src/merge/ | Rust module: read_legacy_content, run_ai_merge, write_cascade_content, conflict detect, prompts | ✅ Done | ✅ | P3 | T-P3-E03-17..20 |
| cascade-types/provision | crates/cascade-types/src/provision/ | Types: GoogleProvision, PoolKey, AutoAuthToken | ✅ Done | ✅ | P3 | T-P3-E03-08,43 |
| cascade-types/pool | crates/cascade-types/src/pool/ | Types: GeminiPoolKey, pool registration structs | ✅ Done | ✅ | P3 | T-P3-E03-04..06 |
| cascade-types/auto_auth | crates/cascade-types/src/auto_auth/ | Types: AutoAuthEntry, import/scan structs | ✅ Done | ✅ | P3 | T-P3-E03-07 |
