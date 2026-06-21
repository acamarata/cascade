//! cascade-daemon library surface.
//!
//! The daemon binary (`cascaded`) lives in `main.rs`. This lib.rs re-exports
//! the public module surface so integration tests and the cascade-cli crate
//! can reference types without pulling in the binary entry point.

// WHY dead_code allowed: this crate is an early P2 scaffold. Many types,
// methods, and constants are defined for future wiring (P3+) and are not
// yet referenced by the binary entry point. Suppressed at the crate level
// so -D warnings (clippy gate) focuses on new issues, not scaffolded stubs.
#![allow(dead_code)]

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
// T-P4-E01-26: auto-RAG file watcher (notify-based)
pub mod rag_watcher;
// T-P4-E04-03: parallel indexing pipeline (WorkerPool → IndexManager)
pub mod indexer;
// T-P4-E01-29: IPC search endpoint (rag.* JSON-RPC methods)
pub mod regen;
pub mod search_handler;
// T-P4-E01-32: external drive mount/unmount watch — pause/resume RAG indexing
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

pub use config::Config;
