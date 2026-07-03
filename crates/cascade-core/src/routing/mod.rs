//! # routing — quota-aware multi-source routing matrix
//!
//! ## Purpose
//!
//! Implements the v1.2 routing matrix described in CASCADE-COMPLETE-VISION.md §5.
//! The matrix maps `TaskClass` values to an ordered preference list of delegate
//! lanes, applies quota headroom checks, enforces the sensitive-data firewall,
//! and returns a `RoutingDecision` with the selected lane (or an error with reason).
//!
//! Since E1-S6 the account-choice step (spill iteration + saturation gating +
//! TaskClass→Tier mapping) is delegated to the shared `crate::selection` module.
//!
//! ## Modules
//!
//! | Module | Purpose |
//! |--------|---------|
//! | [`task_class`] | `TaskClass` enum + display |
//! | [`delegate`]   | `DelegateLane` abstraction + concrete lane impls |
//! | `gfp_http`     | GFP lane HTTP bridge to the :3762 GP proxy (E1-S6) |
//! | [`router`]     | `Router::select()` — the matrix logic |
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing module (v1.2)

pub mod delegate;
pub mod gfp_http;
pub mod router;
pub mod task_class;

pub use delegate::{DelegateLane, DelegateTarget, LaneAvailability};
pub use router::{RoutingDecision, RoutingEvent, RoutingObserver, Router, RouterConfig};
pub use task_class::TaskClass;
