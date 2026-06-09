// Scanner module — legacy AI tool home discovery.
//
// Purpose: Provides the scanner service interface for discovering legacy AI
//   tool homes both in global system paths and within a dev-tree root.
//   Types live in types.rs; implementations live in global.rs and dev_tree.rs.
//
// Inputs:  None — consumers use the sub-module public APIs directly.
// Outputs: Re-exports LegacyToolHome, ScanResult, ToolId, LegacyFile, FileKind
//          for use by commands/scanner.rs.
//
// Constraints:
//   - All scan functions are synchronous (no async).
//   - No scanning logic in this module — delegate to sub-modules.
//
// SPORT: MASTER-COMPONENTS.md — scanner module — scanner/mod.rs
// Task: T-P3-E03-09

pub mod dev_tree;
pub mod global;
pub mod types;

// Re-export the core types at the scanner:: level so callers can write
// `scanner::LegacyToolHome` rather than `scanner::types::LegacyToolHome`.
pub use types::{FileKind, LegacyFile, LegacyToolHome, ScanResult, ToolId};
