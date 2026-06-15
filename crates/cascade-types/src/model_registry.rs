//! Model-ID registry mapping agent [`Tier`]s to concrete provider model IDs,
//! with optional per-model **behavioral profiles** and profile-aware routing.
//!
//! # Purpose
//!
//! Provides a vendor-neutral bridge between Cascade's internal tier concept
//! (T0–T3) and the model identifiers expected by external AI providers.
//! Model IDs can be:
//!
//! 1. **Built-in defaults** — generic placeholder strings safe to commit
//!    without embedding vendor marketing names.
//! 2. **TOML overrides** — a `[models]` table in `config.toml` replaces
//!    defaults for that deployment.
//!
//! # Behavioral profiles (P12)
//!
//! Same tier ≠ same defaults across providers. A [`ModelProfile`] captures
//! per-model behavioral characteristics — preferred output format, tool-use
//! eagerness, refusal sensitivity, and task-suitability tags — so that the
//! dispatcher can pick the best entry for a given [`TaskShape`] rather than
//! using tier alone. Profile types live in [`crate::model_profile_types`].
//!
//! Profile fields all use `#[serde(default)]` so existing TOML without profile
//! data continues to deserialise correctly; no profiles = pure tier routing.
//!
//! # TOML shape
//!
//! ```toml
//! [models]
//! t0 = { provider_id = "acme", model_id = "acme-tier0" }
//! t2 = { provider_id = "acme", model_id = "acme-tier2-fast",
//!         profile = { default_format = "markdown", tool_use_trigger = "eager",
//!                     refusal_sensitivity = "low",
//!                     best_for = ["code", "structured-output"] } }
//! ```
//!
//! Any omitted tier keeps the built-in default. An absent `profile` key keeps
//! the `None` default — `resolve(tier)` still works without any profile data.
//!
//! # Inputs / Outputs
//!
//! - **Input:** an optional `[models]` TOML table (as [`ModelOverrides`])
//! - **Output:** [`ModelRegistry::resolve`] → `&ModelEntry` by tier;
//!   [`ModelRegistry::best_for`] → `&ModelEntry` by [`TaskShape`]
//!
//! # Constraints
//!
//! - No vendor names hardcoded in built-in defaults.
//! - Registry is clone-cheap (four entries, all small strings).
//! - All public types are `Serialize + Deserialize` for IPC exposure.
//! - `best_for` is deterministic: same inputs always produce the same choice.

use crate::agent::Tier;
use serde::{Deserialize, Serialize};

// Re-export profile types so callers use a single import path.
pub use crate::model_profile_types::{
    ModelProfile, OutputFormat, RefusalSensitivity, TaskShape, ToolUseTrigger,
};

// ── ModelEntry ────────────────────────────────────────────────────────────────

/// A concrete (provider, model) pair for a single tier, with an optional
/// behavioral profile.
///
/// Both `provider_id` / `model_id` use generic placeholder strings in the
/// built-in defaults so that `model_registry.rs` can be committed to version
/// control without embedding any particular vendor's marketing names.
///
/// # Example
///
/// ```
/// use cascade_types::model_registry::{ModelEntry, ModelProfile, OutputFormat};
///
/// let entry = ModelEntry {
///     provider_id: "my-provider".into(),
///     model_id: "my-model-v1".into(),
///     profile: Some(ModelProfile {
///         default_format: OutputFormat::Json,
///         best_for: vec!["structured-output".into()],
///         ..Default::default()
///     }),
/// };
/// assert_eq!(entry.provider_id, "my-provider");
/// assert!(entry.profile.is_some());
/// ```
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelEntry {
    /// Provider identifier (e.g. `"acme"`, `"local"`).
    ///
    /// Used by `cascade-providers` to select the correct SDK adapter.
    pub provider_id: String,

    /// The model identifier as expected by the provider's API.
    pub model_id: String,

    /// Optional behavioral profile for this entry.
    ///
    /// When `None` the entry still participates in tier-based routing via
    /// `resolve(tier)`. It scores zero in `best_for` overlap matching but
    /// remains the tier fallback.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub profile: Option<ModelProfile>,
}

impl ModelEntry {
    /// Construct a new entry from static string literals (no profile).
    pub fn new(provider_id: impl Into<String>, model_id: impl Into<String>) -> Self {
        Self {
            provider_id: provider_id.into(),
            model_id: model_id.into(),
            profile: None,
        }
    }

    /// Construct a new entry with a behavioral profile.
    pub fn with_profile(
        provider_id: impl Into<String>,
        model_id: impl Into<String>,
        profile: ModelProfile,
    ) -> Self {
        Self {
            provider_id: provider_id.into(),
            model_id: model_id.into(),
            profile: Some(profile),
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
/// t2 = { provider_id = "acme", model_id = "acme-fast",
///         profile = { default_format = "json", best_for = ["structured-output"] } }
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

/// Registry mapping each [`Tier`] to a [`ModelEntry`], with optional
/// profile-aware routing via [`ModelRegistry::best_for`].
///
/// # Construction
///
/// Start with [`ModelRegistry::default`] (built-in defaults — no profiles),
/// then optionally call [`ModelRegistry::apply_overrides`] with values loaded
/// from TOML.
///
/// ```
/// use cascade_types::model_registry::{
///     ModelRegistry, ModelOverrides, ModelEntry, ModelProfile, TaskShape, OutputFormat,
/// };
/// use cascade_types::agent::Tier;
///
/// let mut registry = ModelRegistry::default();
///
/// // Apply TOML-loaded overrides with a profile:
/// let overrides = ModelOverrides {
///     t2: Some(ModelEntry::with_profile(
///         "acme", "acme-fast",
///         ModelProfile {
///             default_format: OutputFormat::Json,
///             best_for: vec!["structured-output".into()],
///             ..Default::default()
///         },
///     )),
///     ..Default::default()
/// };
/// registry.apply_overrides(&overrides);
///
/// // Tier-only path still works:
/// let entry = registry.resolve(Tier::T2);
/// assert_eq!(entry.provider_id, "acme");
///
/// // Profile-aware path:
/// let shape = TaskShape {
///     tier: Tier::T2,
///     output_format: Some(OutputFormat::Json),
///     needs: vec!["structured-output".into()],
/// };
/// let best = registry.best_for(&shape);
/// assert_eq!(best.model_id, "acme-fast");
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
    /// Default entries carry no profile (`profile: None`).
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
    /// Profiles in the overrides are adopted verbatim; passing an entry with
    /// `profile: None` clears any previously-set profile for that tier.
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
    /// This is the pure tier-based path; profile data in the entry is
    /// available to callers but not used for selection here.
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

    /// Select the [`ModelEntry`] that best matches the given [`TaskShape`].
    ///
    /// # Selection algorithm (deterministic)
    ///
    /// 1. Resolve the single entry for `shape.tier`.
    /// 2. Score the entry's profile against the shape:
    ///    - +1 for each tag in `shape.needs` found in `profile.best_for`.
    ///    - +1 if `shape.output_format` matches `profile.default_format`.
    /// 3. Return the entry — the caller always gets a valid reference.
    ///    When score is zero (no profile or no matching signals) the returned
    ///    entry is the same one `resolve(tier)` would return (tier default).
    ///
    /// Empty `shape.needs` + `shape.output_format == None` short-circuits
    /// immediately to `resolve(tier)` without scoring.
    ///
    /// ```
    /// use cascade_types::model_registry::{
    ///     ModelRegistry, ModelOverrides, ModelEntry, ModelProfile,
    ///     TaskShape, OutputFormat,
    /// };
    /// use cascade_types::agent::Tier;
    ///
    /// let mut reg = ModelRegistry::default();
    /// reg.apply_overrides(&ModelOverrides {
    ///     t2: Some(ModelEntry::with_profile(
    ///         "acme", "acme-json-v1",
    ///         ModelProfile {
    ///             default_format: OutputFormat::Json,
    ///             best_for: vec!["structured-output".into()],
    ///             ..Default::default()
    ///         },
    ///     )),
    ///     ..Default::default()
    /// });
    ///
    /// let shape = TaskShape {
    ///     tier: Tier::T2,
    ///     output_format: Some(OutputFormat::Json),
    ///     needs: vec!["structured-output".into()],
    /// };
    /// let entry = reg.best_for(&shape);
    /// assert_eq!(entry.model_id, "acme-json-v1");
    /// ```
    pub fn best_for(&self, shape: &TaskShape) -> &ModelEntry {
        let candidate = self.resolve(shape.tier);

        // Short-circuit: no discriminating information in the shape.
        if shape.needs.is_empty() && shape.output_format.is_none() {
            return candidate;
        }

        // Score the single-tier candidate against the shape.
        //
        // WHY single candidate: the current registry stores exactly one entry
        // per tier, so "best from same-tier pool" reduces to scoring the one
        // entry. The returned reference is the same entry regardless of score;
        // the score is used to decide whether the entry qualifies as a
        // "profile match" vs. a pure tier fallback. Both paths return the same
        // reference here (one-entry pool), but the separation is explicit for
        // when multi-entry pools are added in a future iteration.
        let score = Self::score(candidate, shape);
        let _ = score; // Score drives future multi-candidate selection.
        candidate
    }

    /// Score a `ModelEntry` against a `TaskShape`.
    ///
    /// Returns the number of matching signals (0 = no profile or no overlap).
    /// Deterministic and allocation-free.
    fn score(entry: &ModelEntry, shape: &TaskShape) -> u32 {
        let Some(profile) = &entry.profile else {
            return 0;
        };

        let mut s: u32 = 0;

        for tag in &shape.needs {
            if profile.best_for.iter().any(|b| b == tag) {
                s += 1;
            }
        }

        if let Some(fmt) = shape.output_format {
            if fmt == profile.default_format {
                s += 1;
            }
        }

        s
    }

    /// Construct a registry from a complete set of four entries.
    ///
    /// Useful for testing and for deserialisation from a full `[models]` table.
    pub fn from_entries(t0: ModelEntry, t1: ModelEntry, t2: ModelEntry, t3: ModelEntry) -> Self {
        Self { t0, t1, t2, t3 }
    }
}

// Tests live in tests/model_registry.rs (integration tests) to keep this
// file within the 300-line implementation cap.
