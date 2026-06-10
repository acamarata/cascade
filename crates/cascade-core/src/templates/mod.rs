//! # cascade_core::templates
//!
//! Template loading, parsing, and registry for the Cascade template system.
//!
//! ## Modules
//!
//! - [`parser`] — parses a single `.md` template file into a [`TemplateRecord`]
//! - [`registry`] — multi-directory loader and filter engine
//! - [`section_parser`] — splits Markdown into named [`Section`]s
//! - [`apply`] — merges a template into an existing `CASCADE.md`
//!
//! ## Quick start
//!
//! ```no_run
//! use cascade_core::templates::registry::{TemplateRegistry, TemplateFilter};
//! use cascade_core::templates::apply::{TemplateEngine, ApplyOptions};
//! use cascade_types::TemplateTier;
//! use std::path::Path;
//!
//! let bundled = Path::new("data/templates");
//! let reg = TemplateRegistry::load_dirs(&[bundled]);
//!
//! let gci_templates = reg.list(&TemplateFilter {
//!     tier: Some(TemplateTier::Gci),
//!     ..Default::default()
//! });
//!
//! // Apply the first template found to a target CASCADE.md
//! if let Some(record) = gci_templates.first() {
//!     let engine = TemplateEngine::new();
//!     let target = Path::new("/home/user/.cascade/CLAUDE.md");
//!     let result = engine.diff(record, target).unwrap();
//!     println!("{} sections would be applied", result.applied.len());
//! }
//! ```

pub mod apply;
pub mod inheritance;
pub mod parser;
pub mod registry;
pub mod section_parser;
pub mod upgrade;
pub mod version;

pub use apply::{ApplyOptions, TemplateEngine};
pub use registry::{TemplateFilter, TemplateRegistry};
