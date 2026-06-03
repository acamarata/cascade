# MASTER-MODULES.md — Cascade Core Rust Modules

**Purpose:** Registry of every significant module inside cascade-core.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3 plan

| Module | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| resolve | crates/cascade-core/src/resolve.rs | Instruction cascade resolver: walk tier path, merge GCI→APC→PPC→PRC→PAC | 🔲 Planned | P2 | T-P2-E01-* |
| embed | crates/cascade-core/src/embed.rs | Embedding dispatch: chunk text, call provider, cache result | 🔲 Planned | P2/P4 | T-P2-E01-*, T-P4-E02-* |
| watcher | crates/cascade-core/src/watcher.rs | File-system watch: debounce, filter .claude/ changes | 🔲 Planned | P2 | T-P2-E01-* |
| config | crates/cascade-core/src/config.rs | Config loading: ~/.cascade/config.toml, env overrides | 🔲 Planned | P2 | T-P2-E01-* |
| cache | crates/cascade-core/src/cache.rs | Instruction cache: TTL-based tier cache, invalidate on change | 🔲 Planned | P2/P3 | T-P2-E01-* |
| providers | crates/cascade-core/src/providers.rs | Provider registry: load adapters, route embed/completion | 🔲 Planned | P3 | T-P3-E05-* |
| health | crates/cascade-core/src/health.rs | Health state aggregation for daemon healthcheck | 🔲 Planned | P2 | T-P2-E01-* |
| index | crates/cascade-core/src/index.rs | RAG index coordination: dispatch to cascade-rag | 🔲 Planned | P4 | T-P4-E02-* |
| storage | crates/cascade-core/src/storage.rs | Persistent KV storage abstraction over SQLite | 🔲 Planned | P2/P3 | T-P2-E01-* |
