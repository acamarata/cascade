//! # routing — quota-aware multi-source routing matrix
//!
//! ## Purpose
//!
//! Implements the v1.2 routing matrix described in CASCADE-COMPLETE-VISION.md §5.
//! The matrix maps `TaskClass` values to an ordered preference list of delegate
//! lanes, applies quota headroom checks, enforces the sensitive-data firewall,
//! and returns a `RoutingDecision` with the selected lane (or an error with reason).
//!
//! ## Modules
//!
//! | Module | Purpose |
//! |--------|---------|
//! | [`task_class`] | `TaskClass` enum + display |
//! | [`delegate`]   | `DelegateLane` abstraction + concrete lane impls |
//! | [`router`]     | `Router::select()` — the matrix logic |
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing module (v1.2)

pub mod delegate;
pub mod router;
pub mod task_class;

pub use delegate::{DelegateLane, DelegateTarget, LaneAvailability};
pub use router::{RoutingDecision, Router, RouterConfig};
pub use task_class::TaskClass;
