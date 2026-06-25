//! # cascade_core::templates::apply
//!
//! Template apply engine — merges a [`TemplateRecord`]'s sections into an
//! existing (or new) `CASCADE.md` file.
//!
//! ## Purpose
//!
//! [`TemplateEngine`] is the single entry-point for all "apply template"
//! operations in the Cascade CLI and GUI.
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-core::templates` exports
//! (T-P3-E05-09).

mod engine;
mod merge;
mod options;
mod path;
mod stamp;
mod upgrade_helpers;

// ── Public re-exports (preserve the original public API surface) ──────────────

pub use engine::TemplateEngine;
pub use options::ApplyOptions;
pub(crate) use stamp::make_stamp;

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests;
