//! # build — autonomous Build engine (pews-02)
//!
//! ## Purpose
//! Implements the `BuildEngine` that drives a phase's full EOx gate chain
//! autonomously: topological ticket dispatch → EOSt → EOT → EOS → EOW →
//! EOE → EOP. A pluggable [`TicketDispatcher`] trait keeps the engine
//! testable without real agent processes.
//!
//! ## Sub-modules
//! - [`engine`] — `BuildConfig` and core `BuildEngine` orchestration
//! - [`dispatchers`] — `TicketDispatcher`, `MockDispatcher`, and `FleetDispatcher`
//! - [`dispatch`] — ticket-weight → `TaskClass` mapping (INTERIM table) + fleet CLI-shelling
//! - [`topo`] — internal topological ticket ordering
//!
//! ## SPORT
//! MASTER-CRATES.md — cascade-core: build module (pews-02)

pub mod dispatch;
pub mod dispatchers;
pub mod engine;
pub mod topo;

#[cfg(test)]
mod engine_tests;
#[cfg(test)]
mod test_support;

pub use dispatch::{
    classify_ticket, cli_binary_for_task_class, run_fleet_cli, FleetOutcome, FleetRunner,
    RealFleetRunner,
};
pub use dispatchers::{FleetDispatcher, MockDispatcher, TicketDispatcher};
pub use engine::{BuildConfig, BuildEngine};
