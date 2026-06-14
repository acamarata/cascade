//! Model-ID registry mapping agent [`Tier`]s to concrete provider model IDs.
//!
//! # Purpose
//!
//! Provides a vendor-neutral bridge between Cascade's internal tier concept
//! (T0–T3) and the model identifiers expected by external AI providers.
//! Model IDs can be:
//!
//! 1. **Built-in defaults** — generic placeholder strings that are safe to
//!    store in version control without embedding vendor marketing names.
//! 2. **TOML overrides** — a `[models]` table in any tier's `config.toml`
//!    replaces the defaults for that deployment.
//!
//! # TOML shape
//!
//! ```toml
//! [models]
//! t0 = { provider_id = "anthropic", model_id = "claude-opus-4-5" }
//! t1 = { provider_id = "anthropic", model_id = "claude-opus-4-5" }
//! t2 = { provider_id = "anthropic", model_id = "claude-sonnet-4-5" }
//! t3 = { provider_id = "anthropic", model_id = "claude-haiku-4-5" }
//! ```
//!
//! Any omitted tier keeps the built-in default. Unknown keys are ignored.
//!
//! # Inputs / Outputs
//!
//! - **Input:** an optional `[models]` TOML table (as [`ModelOverrides`])
//! - **Output:** [`ModelRegistry::resolve`] returns a `&ModelEntry` for any
//!   [`Tier`], never fails (falls back to defaults)
//!
//! # Constraints
//!
//! - No vendor names are hardcoded in the built-in defaults.
//! - The registry is clone-cheap (four entries, all small strings).
//! - All public types are `Serialize + Deserialize` for IPC exposure.

use crate::agent::Tier;
use serde::{Deserialize, Serialize};

// ── ModelEntry ────────────────────────────────────────────────────────────────

/// A concrete (provider, model) pair for a single tier.
///
/// Both fields use generic placeholder strings in the built-in defaults so
/// that `model_registry.rs` can be committed to version control without
/// embedding any particular vendor's marketing names.
///
/// # Example
///
/// ```
/// use cascade_types::model_registry::ModelEntry;
///
/// let entry = ModelEntry {
///     provider_id: "my-provider".into(),
///     model_id: "my-model-v1".into(),
/// };
/// assert_eq!(entry.provider_id, "my-provider");
/// ```
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelEntry {
    /// Provider identifier (e.g. `"anthropic"`, `"openai"`, `"local"`).
    ///
    /// Used by `cascade-providers` to select the correct SDK adapter.
    pub provider_id: String,

    /// The model identifier as expected by the provider's API.
    ///
    /// Examples: `"claude-sonnet-4-5"`, `"gpt-4o"`, `"llama3"`.
    pub model_id: String,
}

impl ModelEntry {
    /// Construct a new entry from static string literals.
    pub fn new(provider_id: impl Into<String>, model_id: impl Into<String>) -> Self {
        Self {
            provider_id: provider_id.into(),
            model_id: model_id.into(),
        }
    }
}

// ── ModelOverrides (TOML shape) ───────────────────────────────────────────────

/// Optional per-tier overrides loaded from a `[models]` table in `config.toml`.
///
/// All fields are optional. An absent field keeps the built-in default.
///
/// # Example TOML
///
/// ```toml
/// [models]
/// t2 = { provider_id = "openai", model_id = "gpt-4o" }
/// ```
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "lowercase")]
pub struct ModelOverrides {
    /// Override for T0 (observer/orchestrator).
    pub t0: Option<ModelEntry>,
    /// Override for T1 (planner/decision-maker).
    pub t1: Option<ModelEntry>,
    /// Override for T2 (executor / bulk workhorse).
    pub t2: Option<ModelEntry>,
    /// Override for T3 (cheap triage / structured output).
    pub t3: Option<ModelEntry>,
}

// ── ModelRegistry ─────────────────────────────────────────────────────────────

/// Registry mapping each [`Tier`] to a [`ModelEntry`].
///
/// # Construction
///
/// Start with [`ModelRegistry::default`] (built-in defaults), then optionally
/// call [`ModelRegistry::apply_overrides`] with values loaded from TOML.
///
/// ```
/// use cascade_types::model_registry::{ModelRegistry, ModelOverrides, ModelEntry};
///
/// let mut registry = ModelRegistry::default();
///
/// // Apply TOML-loaded overrides:
/// let overrides = ModelOverrides {
///     t2: Some(ModelEntry::new("openai", "gpt-4o")),
///     ..Default::default()
/// };
/// registry.apply_overrides(&overrides);
///
/// // Resolve any tier:
/// let entry = registry.resolve(cascade_types::agent::Tier::T2);
/// assert_eq!(entry.provider_id, "openai");
/// assert_eq!(entry.model_id, "gpt-4o");
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelRegistry {
    t0: ModelEntry,
    t1: ModelEntry,
    t2: ModelEntry,
    t3: ModelEntry,
}

impl Default for ModelRegistry {
    /// Built-in defaults — generic placeholder strings, not vendor-specific.
    ///
    /// These allow the registry to function in any environment without
    /// configuration, and prevent vendor lock-in at the type level.
    fn default() -> Self {
        Self {
            t0: ModelEntry::new("cascade-default", "tier-t0"),
            t1: ModelEntry::new("cascade-default", "tier-t1"),
            t2: ModelEntry::new("cascade-default", "tier-t2"),
            t3: ModelEntry::new("cascade-default", "tier-t3"),
        }
    }
}

impl ModelRegistry {
    /// Apply overrides from a TOML `[models]` table.
    ///
    /// Only fields present in `overrides` are changed; absent fields keep
    /// their current value (built-in default or a prior override).
    ///
    /// # Example
    ///
    /// ```
    /// use cascade_types::model_registry::{ModelRegistry, ModelOverrides, ModelEntry};
    ///
    /// let mut reg = ModelRegistry::default();
    /// reg.apply_overrides(&ModelOverrides {
    ///     t3: Some(ModelEntry::new("local", "mistral-7b")),
    ///     ..Default::default()
    /// });
    /// assert_eq!(reg.resolve(cascade_types::agent::Tier::T3).provider_id, "local");
    /// // Other tiers unchanged:
    /// assert_eq!(reg.resolve(cascade_types::agent::Tier::T0).provider_id, "cascade-default");
    /// ```
    pub fn apply_overrides(&mut self, overrides: &ModelOverrides) {
        if let Some(e) = &overrides.t0 {
            self.t0 = e.clone();
        }
        if let Some(e) = &overrides.t1 {
            self.t1 = e.clone();
        }
        if let Some(e) = &overrides.t2 {
            self.t2 = e.clone();
        }
        if let Some(e) = &overrides.t3 {
            self.t3 = e.clone();
        }
    }

    /// Resolve the [`ModelEntry`] for the given [`Tier`].
    ///
    /// Always returns a valid entry (never panics, never returns `None`).
    ///
    /// ```
    /// use cascade_types::model_registry::ModelRegistry;
    /// use cascade_types::agent::Tier;
    ///
    /// let reg = ModelRegistry::default();
    /// assert_eq!(reg.resolve(Tier::T2).model_id, "tier-t2");
    /// ```
    pub fn resolve(&self, tier: Tier) -> &ModelEntry {
        match tier {
            Tier::T0 => &self.t0,
            Tier::T1 => &self.t1,
            Tier::T2 => &self.t2,
            Tier::T3 => &self.t3,
        }
    }

    /// Construct a registry from a complete set of four entries.
    ///
    /// Useful for testing and for deserialisation from a full `[models]` table.
    pub fn from_entries(t0: ModelEntry, t1: ModelEntry, t2: ModelEntry, t3: ModelEntry) -> Self {
        Self { t0, t1, t2, t3 }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::agent::Tier;

    // ── test 1: all four tiers resolve from defaults ──────────────────────────

    /// Default registry resolves all four tiers without panic.
    #[test]
    fn default_registry_resolves_all_tiers() {
        let reg = ModelRegistry::default();
        let t0 = reg.resolve(Tier::T0);
        let t1 = reg.resolve(Tier::T1);
        let t2 = reg.resolve(Tier::T2);
        let t3 = reg.resolve(Tier::T3);

        // Default provider is "cascade-default" for all tiers.
        assert_eq!(t0.provider_id, "cascade-default");
        assert_eq!(t1.provider_id, "cascade-default");
        assert_eq!(t2.provider_id, "cascade-default");
        assert_eq!(t3.provider_id, "cascade-default");

        // Default model IDs follow the tier-tN pattern.
        assert_eq!(t0.model_id, "tier-t0");
        assert_eq!(t1.model_id, "tier-t1");
        assert_eq!(t2.model_id, "tier-t2");
        assert_eq!(t3.model_id, "tier-t3");
    }

    // ── test 2: TOML override replaces only the specified tier ────────────────

    /// apply_overrides replaces the specified tier and leaves others unchanged.
    #[test]
    fn toml_override_replaces_single_tier() {
        let mut reg = ModelRegistry::default();
        reg.apply_overrides(&ModelOverrides {
            t2: Some(ModelEntry::new("openai", "gpt-4o")),
            ..Default::default()
        });

        let t2 = reg.resolve(Tier::T2);
        assert_eq!(t2.provider_id, "openai");
        assert_eq!(t2.model_id, "gpt-4o");

        // Other tiers must be unchanged.
        assert_eq!(reg.resolve(Tier::T0).provider_id, "cascade-default");
        assert_eq!(reg.resolve(Tier::T1).provider_id, "cascade-default");
        assert_eq!(reg.resolve(Tier::T3).provider_id, "cascade-default");
    }

    // ── test 3: TOML round-trip for ModelOverrides ────────────────────────────

    /// ModelOverrides deserialises correctly from TOML.
    #[test]
    fn model_overrides_toml_round_trip() {
        let toml_str = r#"
[models]
t1 = { provider_id = "anthropic", model_id = "claude-opus-4" }
t3 = { provider_id = "local", model_id = "mistral-7b" }
"#;

        #[derive(serde::Deserialize)]
        struct Wrapper {
            models: ModelOverrides,
        }

        let parsed: Wrapper = toml::from_str(toml_str).expect("TOML must parse");
        let ov = parsed.models;

        assert!(ov.t0.is_none(), "t0 must be absent");
        assert!(ov.t2.is_none(), "t2 must be absent");

        let t1 = ov.t1.expect("t1 must be present");
        assert_eq!(t1.provider_id, "anthropic");
        assert_eq!(t1.model_id, "claude-opus-4");

        let t3 = ov.t3.expect("t3 must be present");
        assert_eq!(t3.provider_id, "local");
        assert_eq!(t3.model_id, "mistral-7b");
    }

    // ── test 4: all four overrides applied at once ────────────────────────────

    /// apply_overrides with all four tiers replaces all defaults.
    #[test]
    fn all_four_overrides_applied() {
        let mut reg = ModelRegistry::default();
        reg.apply_overrides(&ModelOverrides {
            t0: Some(ModelEntry::new("p", "m0")),
            t1: Some(ModelEntry::new("p", "m1")),
            t2: Some(ModelEntry::new("p", "m2")),
            t3: Some(ModelEntry::new("p", "m3")),
        });

        assert_eq!(reg.resolve(Tier::T0).model_id, "m0");
        assert_eq!(reg.resolve(Tier::T1).model_id, "m1");
        assert_eq!(reg.resolve(Tier::T2).model_id, "m2");
        assert_eq!(reg.resolve(Tier::T3).model_id, "m3");
    }

    // ── test 5: registry JSON serialisation round-trip ────────────────────────

    /// ModelRegistry survives a serde_json round-trip.
    #[test]
    fn registry_json_round_trip() {
        let reg = ModelRegistry::default();
        let json = serde_json::to_string(&reg).expect("serialize");
        let parsed: ModelRegistry = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(parsed.resolve(Tier::T0).model_id, "tier-t0");
        assert_eq!(parsed.resolve(Tier::T3).model_id, "tier-t3");
    }

    // ── test 6: empty TOML models table leaves defaults intact ────────────────

    /// An empty [models] table applies no overrides.
    #[test]
    fn empty_models_table_keeps_defaults() {
        let toml_str = "[models]\n";

        #[derive(serde::Deserialize)]
        struct Wrapper {
            models: ModelOverrides,
        }

        let parsed: Wrapper = toml::from_str(toml_str).expect("TOML must parse");
        let ov = parsed.models;

        let mut reg = ModelRegistry::default();
        reg.apply_overrides(&ov);

        assert_eq!(reg.resolve(Tier::T2).provider_id, "cascade-default");
    }
}
