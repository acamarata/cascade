//! # dispatch — ticket-weight → TaskClass mapping (INTERIM table)
//!
//! ## Purpose
//! Maps a ticket's `weight` field to a [`TaskClass`] so the
//! [`super::engine::BuildEngine`] can route each ticket to the correct
//! delegate lane via the fleet Router.
//!
//! ## Inputs
//! `weight: Option<&str>` — the `weight` field from a [`crate::pbd::Ticket`]
//! (values: `"S"`, `"M"`, `"L"`, `"XL"` or numeric strings).
//!
//! ## Outputs
//! [`TaskClass`] — passed to `Router::select` to choose a provider lane.
//!
//! ## Constraints
//! - No I/O, no async, no panics outside `#[cfg(test)]`.
//! - This table is **INTERIM** — superseded once the agents-01 roster exposes
//!   per-role default tiers.
//!
//! ## SPORT
//! MASTER-CRATES.md — cascade-core: build::dispatch

// TODO(agents-01 supersedes): replace this static table with AgentRole::default_tier
// lookups once the full roster is wired. The Router seam stays; only this
// classify_ticket function changes.

use crate::routing::TaskClass;

/// Classify a ticket weight into a [`TaskClass`] for routing.
///
/// # Mapping (INTERIM)
///
/// | Weight | TaskClass   | Rationale                           |
/// |--------|-------------|-------------------------------------|
/// | S / 1  | `Cheap`     | Trivial change; free Flash tier OK  |
/// | M / 2  | `BulkExec`  | Standard implementation             |
/// | L / 3  | `BulkExec`  | Larger feature; still bulk          |
/// | XL / 4 | `FinalGate` | Architectural; needs best model     |
/// | other  | `BulkExec`  | Safe default for unknown weight     |
pub fn classify_ticket(weight: Option<&str>) -> TaskClass {
    // TODO(agents-01 supersedes): replace with AgentRole::default_tier lookup
    match weight.unwrap_or("").trim().to_ascii_uppercase().as_str() {
        "S" | "1" => TaskClass::Cheap,
        "M" | "2" => TaskClass::BulkExec,
        "L" | "3" => TaskClass::BulkExec,
        "XL" | "4" => TaskClass::FinalGate,
        _ => TaskClass::BulkExec,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn small_maps_to_cheap() {
        assert_eq!(classify_ticket(Some("S")), TaskClass::Cheap);
        assert_eq!(classify_ticket(Some("s")), TaskClass::Cheap);
        assert_eq!(classify_ticket(Some("1")), TaskClass::Cheap);
    }

    #[test]
    fn medium_and_large_map_to_bulk() {
        assert_eq!(classify_ticket(Some("M")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("L")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("2")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("3")), TaskClass::BulkExec);
    }

    #[test]
    fn xl_maps_to_final_gate() {
        assert_eq!(classify_ticket(Some("XL")), TaskClass::FinalGate);
        assert_eq!(classify_ticket(Some("xl")), TaskClass::FinalGate);
        assert_eq!(classify_ticket(Some("4")), TaskClass::FinalGate);
    }

    #[test]
    fn none_and_unknown_default_to_bulk() {
        assert_eq!(classify_ticket(None), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("HUGE")), TaskClass::BulkExec);
    }
}
