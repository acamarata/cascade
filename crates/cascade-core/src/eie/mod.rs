//! Engineering-excellence (EIE) health checks for user projects.
//!
//! # Purpose
//! Provides a lightweight, extensible health-check framework that Cascade
//! can run against any source tree. v1 ships two checks:
//! - **FileSizeCheck** — flags source files exceeding a configurable line cap.
//! - **DuplicationCheck** — detects near-duplicate blocks via rolling-hash
//!   over N-line windows.
//!
//! # Extension
//! Add future checks by implementing the [`Check`] trait and registering them
//! in [`all_checks`]. The runner and scoring logic is untouched.
//!
//! # Inputs
//! - `path: &Path` — root of the project to analyse.
//! - [`HealthConfig`] — tunable thresholds.
//!
//! # Outputs
//! [`HealthReport`] with a 0–100 score and per-check issues.
//!
//! # Constraints
//! - Pure sync I/O (no async); cheap enough for interactive CLI use.
//! - Respects `.gitignore` and skips `target/`, `node_modules/`, `.git/`, `dist/`.

pub mod checks;
pub mod report;
pub mod runner;

pub use checks::{Check, DuplicationCheck, FileSizeCheck};
pub use report::{DuplicationIssue, FileSizeIssue, HealthReport};
pub use runner::{HealthConfig, all_checks, run_health_check};
