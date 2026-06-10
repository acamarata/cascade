# MASTER-TABLES.md — Cascade SQLite / IPC Protocol Tables

**Purpose:** Registry of every SQLite table and IPC protocol type definition in Cascade.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3/P4 plan

| Entity | Type | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| ipc_protocol | Protocol type | IPC request/response envelope, framing spec | 🔲 Planned | P2 | T-P2-E02-* |
| ipc_auth | Protocol type | IPC auth token handshake types | 🔲 Planned | P2 | T-P2-E02-* |
| ipc_command | Protocol type | IPC command enum dispatched to handler | 🔲 Planned | P2 | T-P2-E02-* |
| ipc_request | Protocol type | Full IPC request with command + payload | 🔲 Planned | P2 | T-P2-E02-* |
| ipc_token | Protocol type | IPC auth token value type | 🔲 Planned | P2 | T-P2-E02-* |
| ipc_client | Rust type | Client-side IPC connection handle | 🔲 Planned | P2 | T-P2-E02-* |
| ipc_handlers | Rust type | Handler dispatch table for IPC commands | 🔲 Planned | P2 | T-P2-E02-* |
| cascade_embed_cache | SQLite table | Cached embeddings: content hash → vector | 🔲 Planned | P4 | T-P4-E02-* |
| cascade_embed_cache_version | SQLite table | Schema version for embed cache | 🔲 Planned | P4 | T-P4-E02-* |
| cascade_rag | SQLite table | RAG document chunks: id, content, embedding | 🔲 Planned | P4 | T-P4-E02-* |
| cascade_vec_shard_{N} | SQLite tables | Vector shard tables (N=0..RAG_SHARD_COUNT-1) | 🔲 Planned | P4 | T-P4-E02-* |
| cascade_state | SQLite table | Daemon persistent state: last poll, project list | 🔲 Planned | P2/P3 | T-P2-E01-* |
| cascade_tiers | SQLite table | Instruction tier records: GCI/APC/PPC/PRC/PAC + content | 🔲 Planned | P3 | T-P3-E04-* |
| TemplateTier | Rust type (cascade-types) | Enum of instruction tiers: GCI/APC/PPC/PRC/PAC | 🔲 Planned | P3 | T-P3-E04-* |
| quota_history | SQLite table | Historical quota snapshots: id, ts, payload JSON | ✅ Done | P2 | T-P2-E02-28 |
| record_quota_snapshot | Rust method | EventBus method: insert quota state snapshot, return rowid | ✅ Done | P2 | T-P2-E02-28 |
| prune_history | Rust method | EventBus method: delete quota history rows older than retention_days | ✅ Done | P2 | T-P2-E02-28 |
