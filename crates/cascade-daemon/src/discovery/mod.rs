//! Project discovery subsystem — scanner, classifier, and registry.
//!
//! Purpose: discover all project directories under the configured roots,
//! classify them by type (Rust, Node, Next, Vite, etc.), and persist the
//! results in a local SQLite registry.
//!
//! Sub-modules:
//! - `classifier` — pure marker-file-based type detection
//! - `registry`   — SQLite-backed `ProjectRecord` store
//! - `scanner`    — walks project roots and calls classifier + registry
//!
//! SPORT: cascade-daemon / discovery module (rag-13)

pub mod classifier;
pub mod registry;
pub mod scanner;

// Re-exports are lib API surface; the bin compiles this module tree
// privately, so they count as unused imports there.
#[allow(unused_imports)]
pub use classifier::classify;
#[allow(unused_imports)]
pub use registry::{ProjectRegistry, RegistryError};
#[allow(unused_imports)]
pub use scanner::{dedup_nested_roots, ProjectScanner};
