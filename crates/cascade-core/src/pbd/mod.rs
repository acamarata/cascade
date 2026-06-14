//! PBD (Phase-Based Development) engine.
//!
//! # Purpose
//! Canonical implementation of the on-disk PBD phase-tracking system.
//! Manages the full hierarchy: Phase → Epic → Wave → Sprint → Ticket → Step.
//!
//! # Sub-modules
//! - `schema` — type definitions, status enums, transition rules.
//! - `store` — filesystem I/O: load, save, atomic writes, INDEX.yaml, events.jsonl.
//! - `protocol` — end-of-X runners (eot/eos/eow/eoe/eop) with injectable external seams.
//!
//! # Usage
//! ```rust,no_run
//! use cascade_core::pbd::store::{PbdStore, resolve_phases_root};
//! use cascade_core::pbd::schema::{Phase, PhaseStatus};
//!
//! let root = resolve_phases_root(None);
//! let store = PbdStore::new(&root);
//! store.init().unwrap();
//! ```

pub mod active_work;
pub mod masters;
pub mod protocol;
pub mod schema;
pub mod store;
pub mod validate;
#[cfg(test)]
mod tests;

// Convenience re-exports
pub use masters::{
    index_pbd, index_repo, MasterEntity, MasterList, MasterPhaseEntry, MasterPhases,
    MasterTicketEntry, MasterTickets,
};
pub use protocol::{
    run_eoe, run_eop, run_eos, run_eot, run_eow, ExternalChecks, NoExternalChecks, ProtocolResult,
};
pub use schema::{
    CurrentPointers, Epic, EpicStatus, EventLevel, IndexEntry, PbdEvent, PbdIndex, Phase,
    PhaseStatus, Sprint, SprintStatus, Step, StepStatus, Ticket, TicketStatus, Wave, WaveStatus,
};
pub use store::{locate_phases_root, resolve_phases_root, PbdStore};
pub use validate::{validate, Issue, IssueLevel, ValidationResult};
pub use active_work::{
    build_active_work, build_active_work_with_tasks, ActiveTaskEntry, ActiveTicketEntry,
    ActiveWorkBlock,
};
