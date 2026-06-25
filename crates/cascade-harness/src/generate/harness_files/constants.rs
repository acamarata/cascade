//! Shared marker strings and delimiter constants.

/// Base idempotency marker prefix — written near the top of every generated
/// harness file.  The full marker line includes a content hash:
/// `<!-- cascade:unified-harness sha256=<32hexchars> -->`.
///
/// Detection uses [`UNIFIED_HARNESS_MARKER_BASE`] so that files written by
/// older versions of cascade (without hashes) are still recognised.
pub const UNIFIED_HARNESS_MARKER: &str = "<!-- cascade:unified-harness -->";
/// Prefix shared by all harness marker variants (with and without hash).
pub const UNIFIED_HARNESS_MARKER_BASE: &str = "<!-- cascade:unified-harness";

/// Delimiter for the injected active-work section.
/// Used to find and replace the section on re-runs (idempotent).
pub const ACTIVE_WORK_BEGIN: &str = "<!-- cascade:active-work-begin -->";
pub const ACTIVE_WORK_END: &str = "<!-- cascade:active-work-end -->";
