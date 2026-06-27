//! # build — autonomous Build engine (pews-02)
//!
//! ## Purpose
//! Implements the `BuildEngine` that drives a phase's full EOx gate chain
//! autonomously: topological ticket dispatch → EOSt → EOT → EOS → EOW →
//! EOE → EOP. A pluggable [`TicketDispatcher`] trait keeps the engine
//! testable without real agent processes.
//!
//! ## Sub-modules
//! - [`engine`] — `BuildEngine`, `TicketDispatcher`, `MockDispatcher`
//! - [`dispatch`] — ticket-weight → `TaskClass` mapping (INTERIM table)
//!
//! ## SPORT
//! MASTER-CRATES.md — cascade-core: build module (pews-02)

pub mod dispatch;
pub mod engine;

pub use dispatch::classify_ticket;
pub use engine::{BuildConfig, BuildEngine, MockDispatcher, TicketDispatcher};
