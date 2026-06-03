# MASTER-LIBS.md — Cascade Frontend Libraries / Shared TS Modules

**Purpose:** Registry of shared TypeScript library modules in the Cascade dashboard.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3 plan

| Library | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| IpcClient | src/lib/ipc.ts | IPC client: connect to daemon Unix socket / TCP | 🔲 Planned | P2/P3 | T-P2-E02-*, T-P3-E01-* |
| command-registry | src/lib/command-registry.ts | Palette command registry: register, fuzzy search | 🔲 Planned | P3 | T-P3-E01-* |
| ApiClient | src/lib/api-client.ts | Typed fetch wrapper for /api/* endpoints | 🔲 Planned | P3 | T-P3-E01-* |
