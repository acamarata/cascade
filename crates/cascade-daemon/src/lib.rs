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
pub mod config;
// W-05 scaffold IPC handlers (T-P3-E03-39b / T-P3-E03-40 / T-P3-E03-41)
pub mod ipc_auto_auth;
pub mod ipc_pool_register;
pub mod ipc_provision;
// T-P3-E04-26: provider IPC command handlers
pub mod ipc_providers;
// T-P3-E04-27: token usage accumulator
pub mod usage;
// T-P3-E04-29: usage analytics IPC handlers
pub mod ipc_usage_analytics;
// T-P3-E04-15: provider health-check background task + cached HealthState
pub mod provider_health;
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
pub mod proxy;
pub mod quota_poller;
pub mod regen;
pub mod shutdown;
pub mod state;
pub mod supervisor;
pub mod telemetry;
pub mod tray;

pub use config::Config;
