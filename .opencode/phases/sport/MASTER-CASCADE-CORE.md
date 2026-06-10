# MASTER-CASCADE-CORE.md — Cascade Core Subsystem Overview

**Purpose:** High-level registry of all cascade-core public API surfaces and subsystems.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3 plan

| Subsystem | Module | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| Resolution engine | cascade-core::resolve | Tier-aware instruction cascade resolution for any FS path | 🔲 Planned | P2 | T-P2-E01-* |
| Config loader | cascade-core::config | ~/.cascade/config.toml load + env override | 🔲 Planned | P2 | T-P2-E01-* |
| Instruction cache | cascade-core::cache | TTL cache for resolved tier content | 🔲 Planned | P2 | T-P2-E01-* |
| Storage layer | cascade-core::storage | SQLite KV abstraction for daemon state | 🔲 Planned | P2 | T-P2-E01-* |
| Embedding dispatch | cascade-core::embed | Chunk + embed via provider, persist to cache | 🔲 Planned | P2/P4 | T-P2-E01-*, T-P4-E02-* |
| File watcher | cascade-core::watcher | Debounced .claude/ change events | 🔲 Planned | P2 | T-P2-E01-* |
| Health aggregation | cascade-core::health | Collect per-subsystem health, expose to daemon | 🔲 Planned | P2 | T-P2-E01-* |
| Provider registry | cascade-core::providers | Load + route to AI provider adapters | 🔲 Planned | P3 | T-P3-E05-* |
| RAG coordinator | cascade-core::index | Delegate to cascade-rag for index/search | 🔲 Planned | P4 | T-P4-E02-* |
