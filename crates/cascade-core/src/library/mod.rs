//! # cascade_core::library
//!
//! File-based library of reusable AI-workflow items stored in
//! `~/.cascade/library/`.
//!
//! ## Modules
//!
//! - [`types`]   — `LibraryItem` struct + `LibraryItemKind` enum.
//! - [`reader`]  — `list_items` / `get_item` (read-only).
//! - [`writer`]  — `upsert_item` / `delete_item` (write + delete).
//! - [`inject`]  — `inject_item` / `eject_item` (harness injection engine, T-P3-E07-07).
//!
//! ## Directory layout
//!
//! ```text
//! ~/.cascade/library/
//! ├── prompts/          ← LibraryItemKind::Prompt
//! ├── agents/           ← LibraryItemKind::Agent
//! ├── personas/         ← LibraryItemKind::Persona
//! └── slash-commands/   ← LibraryItemKind::SlashCommand
//! ```
//!
//! Each file is `{slug}.yaml` containing a serialised [`LibraryItem`].
//!
//! ## Schema
//!
//! Full schema specification: `cascade-core/docs/library-schema.md`.
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — library module (T-P3-E07-01/02/03/07)

pub mod inject;
pub mod reader;
pub mod types;
pub mod writer;

pub use inject::{eject_item, inject_item, InjectionOutcome, InjectionTarget};
pub use reader::{get_item, list_items};
pub use types::{LibraryItem, LibraryItemKind};
pub use writer::{delete_item, upsert_item};

#[cfg(test)]
mod tests;
