//! # context_pack
//!
//! Context session store — atomic file I/O for `.cascade/contexts/{slug}.yaml`.
//!
//! ## Purpose
//!
//! A context session is a named, ordered collection of pinned items (file paths,
//! notes, URLs) that the user wants to inject into an AI coding session.
//! Sessions are persisted as individual YAML files under `.cascade/contexts/`.
//!
//! Two roots are supported:
//! - **Global:** `~/.cascade/contexts/` — available to all projects.
//! - **Project-level:** `{project_root}/.cascade/contexts/` — per-project sessions.
//!
//! ## IPC commands
//!
//! | Command            | Operation                                          |
//! |--------------------|----------------------------------------------------|
//! | `list_contexts`    | Scan a contexts dir → `Vec<ContextMeta>`           |
//! | `get_context`      | Load one session by id → `ContextSession`          |
//! | `upsert_context`   | Atomic create-or-replace a session YAML            |
//! | `delete_context`   | Idempotent remove a session YAML                   |
//!
//! ## File layout
//!
//! ```text
//! .cascade/contexts/
//!   my-session.yaml          # ContextSession YAML
//!   another-session.yaml
//! ```
//!
//! Each `.yaml` file contains a [`ContextSession`] serialized with `serde_yaml`.
//! DateTime fields are RFC3339 strings.
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — context_pack module (T-P3-E07-08)

pub mod packer;
pub mod reader;
pub mod types;
pub mod writer;

// Re-export the public API surface.
pub use packer::{pack_codebase, PackError, MAX_OUTPUT_BYTES};
pub use reader::{get_context, list_contexts};
pub use types::{ContextItem, ContextMeta, ContextSession, ItemType};
pub use writer::{delete_context, upsert_context};

#[cfg(test)]
mod tests;
