//! cascade-daemon library surface.
//!
//! The daemon binary (`cascaded`) lives in `main.rs`. This lib.rs re-exports
//! the public module surface so integration tests and the cascade-cli crate
//! can reference types without pulling in the binary entry point.

pub mod event_bus;
pub mod healthcheck;
pub mod ipc;
pub mod log;
pub mod shutdown;
pub mod supervisor;
