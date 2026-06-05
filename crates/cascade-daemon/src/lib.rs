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
pub mod dashboard;
pub mod event_bus;
pub mod harness_bridge;
pub mod healthcheck;
pub mod hook_runner;
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
