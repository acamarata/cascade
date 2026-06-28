//! Cascade Security — scanning core for secrets, client leaks, dep audits, and error leaks.
//!
//! # Modules
//! - `secret_scan` — regex-based secret detection in text/files
//! - `client_leak` — secrets in client-side code paths (high severity)
//! - `dep_audit`   — cargo/npm/pip advisory scanning
//! - `error_leak`  — DB errors / stack traces exposed in HTTP responses
//! - `report`      — aggregated `Report` type + `prelaunch_scan`

pub mod secret_scan;
pub mod client_leak;
pub mod dep_audit;
pub mod error_leak;
pub mod report;

pub use report::{Report, prelaunch_scan};
