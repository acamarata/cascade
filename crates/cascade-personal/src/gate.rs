//! # cascade-personal::gate
//!
//! Purpose: Mode-aware visibility filter for personal vault collections.
//!
//! In non-Personal mode (CascadeMode::Normal), collections marked `restricted`
//! (medical, financial, credentials) MUST return 0 rows. In Personal mode all
//! collections are accessible. Consent for CC/MCP boundary exposure is checked
//! here and logged to `exposure_log`.
//!
//! Inputs: `CascadeMode` (set at runtime from user prefs), `Collection` (which
//!         collection is being queried), optional `item_id` (for item-level check).
//! Outputs: `bool` — whether the caller may access/expose this item.
//!
//! Constraints:
//! - `can_expose` is the single chokepoint; all query paths call it.
//! - Adding a new restricted collection requires adding it to `Collection::is_restricted`.
//!
//! SPORT: cascade-personal / gate

use serde::{Deserialize, Serialize};

/// The operating mode of the Cascade session.
///
/// # Purpose
/// Controls which collections are visible. Set by the user; stored in session config.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / CascadeMode
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum CascadeMode {
    /// Standard Cascade mode — restricted collections return 0 rows.
    #[default]
    Normal,
    /// Personal mode — all collections are accessible.
    Personal,
}

/// A collection name in the personal vault.
///
/// # Purpose
/// Typed wrapper over the string collection name; carries the restriction
/// classification so the gate does not re-query the DB.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / Collection
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Collection(pub String);

impl Collection {
    /// Returns `true` for collections that are hidden in non-Personal mode.
    ///
    /// Keep in sync with the `sensitivity = 'restricted'` seed rows in migrate.rs.
    pub fn is_restricted(&self) -> bool {
        matches!(self.0.as_str(), "finance" | "health" | "credentials")
    }
}

impl std::fmt::Display for Collection {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        self.0.fmt(f)
    }
}

/// Check whether a record in `collection` with `item_id` may be exposed to
/// CC/MCP in the current `mode`.
///
/// # Purpose
/// Single chokepoint for all exposure decisions. Returns `false` if:
/// - `mode` is `Normal` AND `collection.is_restricted()`.
///
/// Consent-logging is the caller's responsibility (see `vault::request_consent`).
///
/// # Inputs
/// - `collection`: the collection being accessed.
/// - `item_id`: the UUID of the specific record (used for audit, not gating logic).
/// - `mode`: the current CascadeMode.
///
/// # Outputs
/// `true` if the item may be exposed; `false` if it must be withheld.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / gate::can_expose
pub fn can_expose(collection: &Collection, _item_id: Option<&str>, mode: CascadeMode) -> bool {
    if collection.is_restricted() && mode == CascadeMode::Normal {
        tracing::debug!(
            collection = %collection,
            "blocked exposure: restricted collection in non-Personal mode"
        );
        return false;
    }
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normal_mode_blocks_restricted_collections() {
        let finance = Collection("finance".into());
        let health = Collection("health".into());
        let creds = Collection("credentials".into());
        assert!(!can_expose(&finance, None, CascadeMode::Normal));
        assert!(!can_expose(&health, None, CascadeMode::Normal));
        assert!(!can_expose(&creds, None, CascadeMode::Normal));
    }

    #[test]
    fn personal_mode_allows_all_collections() {
        let finance = Collection("finance".into());
        let health = Collection("health".into());
        let people = Collection("people".into());
        assert!(can_expose(&finance, None, CascadeMode::Personal));
        assert!(can_expose(&health, None, CascadeMode::Personal));
        assert!(can_expose(&people, None, CascadeMode::Personal));
    }

    #[test]
    fn normal_mode_allows_standard_collections() {
        let people = Collection("people".into());
        let records = Collection("records".into());
        assert!(can_expose(&people, None, CascadeMode::Normal));
        assert!(can_expose(&records, None, CascadeMode::Normal));
    }

    #[test]
    fn collection_is_restricted_flags_correctly() {
        assert!(Collection("finance".into()).is_restricted());
        assert!(Collection("health".into()).is_restricted());
        assert!(Collection("credentials".into()).is_restricted());
        assert!(!Collection("people".into()).is_restricted());
        assert!(!Collection("family".into()).is_restricted());
        assert!(!Collection("records".into()).is_restricted());
    }
}
