//! cascade-daemon library surface.
//!
//! The daemon binary (`cascaded`) lives in `main.rs`. This lib.rs re-exports
//! the public module surface so integration tests and the cascade-cli crate
//! can reference types without pulling in the binary entry point.

pub mod audit;
// E-P6-04: CEO orchestrator IPC methods (ceo_directive / ceo_status / ceo_approve)
pub mod ipc_ceo;
// T-P4-E04-10: in-memory mtime-validated chunk cache for cascade file reads
pub mod chunk_cache;
pub mod config;
// T-P4-E04-10: cascade instruction-file loader (goes through ChunkCache)
pub mod loader;
// W-05 scaffold IPC handlers (T-P3-E03-39b / T-P3-E03-40 / T-P3-E03-41)
#[cfg(feature = "gfp")]
pub mod ipc_auto_auth;
#[cfg(feature = "gfp")]
pub mod ipc_pool_register;
#[cfg(feature = "gfp")]
pub mod ipc_provision;
// T-P3-E04-26: provider IPC command handlers
pub mod ipc_providers;
// T-P3-E04-27: token usage accumulator
pub mod usage;
// T-P3-E04-29: usage analytics IPC handlers
pub mod ipc_usage_analytics;
// T-P3-E04-15: provider health-check background task + cached HealthState
pub mod dashboard;
pub mod event_bus;
pub mod harness_bridge;
pub mod healthcheck;
pub mod hook_runner;
pub mod http;
pub mod ipc;
pub mod ipc_handlers;
pub mod key_index;
pub mod log;
pub mod provider_health;
#[cfg(feature = "gemini-proxy")]
pub mod proxy;
#[cfg(feature = "gfp")]
pub mod quota_poller;
#[cfg(feature = "gfp")]
pub mod project_poller;
// ram-guardian: OOM-prevention subsystem — memory sampling + conservative
// stray rustc/vitest reaper.
pub mod ram_guardian;
// disk-guardian: boot-volume-exhaustion prevention subsystem — free-space
// sampling + conservative stray build-artifact reaper. Sibling to
// ram_guardian.
pub mod disk_guardian;
// T-P4-E01-26: auto-RAG file watcher (notify-based)
pub mod rag_watcher;
// T-P4-E04-03: parallel indexing pipeline (WorkerPool → IndexManager)
pub mod indexer;
// T-P4-E01-29: IPC search endpoint (rag.* JSON-RPC methods)
pub mod regen;
pub mod search_handler;
// T-P4-E01-32: external drive mount/unmount watch — pause/resume RAG indexing
// rag-11: MCP self-registration trigger point (frame-01 stub)
pub mod mcp_registration;
pub mod shutdown;
pub mod state;
pub mod supervisor;
pub mod telemetry;
pub mod tray;
pub mod volume_watcher;

// T-P4-E04-12/13: delta bundle format, snapshot layout, Ed25519 verification
pub mod updates;

// E-P8-01: kanban Task IPC handlers (task_create/update/list/get/delete/move)
pub mod ipc_tasks;

// E-P6-02 T-04: round-robin account rotation selector
pub mod rotation_selector;

// E-P6-02 T-05: hard-stop per-window budget guard
pub mod budget_guard;

// E-P6-03 v1.1: fleet poller loop — periodic quota-store.json refresh
pub mod fleet_poller;

// live Claude Max usage fetcher (fetch_claude_usage + parse_usage_response)
pub mod claude_usage;

// auto-01: background automation scheduler
pub mod scheduler;

// auto-02: real ProviderRouter + SafeToolInvoker for AutomationRunner
pub mod automation_router;

// nsentry-sync: daemon-owned multi-stream nSentry sync subsystem
pub mod nsentry_sync;

// continuity: reset-time triggers to auto-resume Claude Code sessions once a
// usage-cap window clears (E2-S1)
pub mod continuity;

// E2-S3: POST-processing middleware — background response digest →
// ~/.cascade/context-sync JSONL (the rag_watcher index-refresh nudge)
pub mod context_sync;

// agents-02: ProviderBoardLlm — BoardLlm impl backed by ProviderRegistry
pub mod board_llm_impl;

// rag-13: project discovery + taxonomy + registry
pub mod discovery;

// A1 (vNEXT Phase A): models/models.yaml drift check — embeds the canonical
// provider-family list at compile time and compares it against the live
// providers.json set on daemon boot (non-fatal WARN on drift).
pub mod model_drift;

pub use config::Config;

#[cfg(test)]
pub(crate) mod test_support;
