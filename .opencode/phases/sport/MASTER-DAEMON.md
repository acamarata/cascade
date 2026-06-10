# MASTER-DAEMON.md — Cascade Daemon Modules

**Purpose:** Registry of every module inside the cascade-daemon crate.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P2/P3/P4 plan

| Module | Path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| supervisor | crates/cascade-daemon/src/supervisor.rs | Process supervision: spawn/restart child tasks, watch_loop | 🔲 Planned | P2 | T-P2-E01-* |
| watch_loop | crates/cascade-daemon/src/supervisor.rs | Main daemon event loop inside supervisor | 🔲 Planned | P2 | T-P2-E01-* |
| watcher | crates/cascade-daemon/src/watcher.rs | File-system watcher: debounced inotify/FSEvents/kqueue | 🔲 Planned | P2 | T-P2-E01-* |
| quota_poller | crates/cascade-daemon/src/quota_poller.rs | Poll Claude/OpenAI quota state, write quota-state.json | 🔲 Planned | P2 | T-P2-E01-* |
| project_poller | crates/cascade-daemon/src/project_poller.rs | Poll ~/Sites project state, emit change events | 🔲 Planned | P2 | T-P2-E01-* |
| shutdown | crates/cascade-daemon/src/shutdown.rs | Graceful shutdown: SIGTERM/SIGINT handler, drain queues | 🔲 Planned | P2 | T-P2-E01-* |
| ipc | crates/cascade-daemon/src/ipc/ | IPC server: Unix socket + TCP listener, auth, request routing | 🔲 Planned | P2 | T-P2-E02-* |
| health | crates/cascade-daemon/src/health.rs | Healthcheck endpoint: /health, health_state aggregation | 🔲 Planned | P2/P3 | T-P2-E01-* |
| health_check_task | crates/cascade-daemon/src/health.rs | Background healthcheck task looping every N seconds | 🔲 Planned | P2 | T-P2-E01-* |
| scheduler | crates/cascade-daemon/src/scheduler.rs | Task scheduler: cron-like periodic task dispatch | 🔲 Planned | P3 | T-P3-E06-* |
| dispatch | crates/cascade-daemon/src/dispatch.rs | Route incoming IPC/HTTP requests to correct handler | 🔲 Planned | P2/P3 | T-P2-E02-*, T-P3-E06-* |
| http | crates/cascade-daemon/src/http/ | HTTP server: Axum router, API handlers for dashboard | 🔲 Planned | P3 | T-P3-E02-* |
