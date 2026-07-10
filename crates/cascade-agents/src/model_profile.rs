//! Profile-aware model selection helpers for cascade-agents.
//!
//! # Purpose
//!
//! Bridges [`cascade_types::model_registry`] profile routing into the
//! cascade-agents layer without touching the hot `AgentExecutor` paths.
//!
//! Agent spawn sites that know their output requirements can call
//! [`resolve_model`] with a [`TaskShape`] instead of calling
//! `ModelRegistry::resolve(tier)` directly. Callers that don't need
//! profile-aware selection continue to call `resolve(tier)` — full back-compat.
//!
//! # Inputs / Outputs
//!
//! - **Input:** a [`ModelRegistry`] reference + a [`TaskShape`] (tier + needs).
//! - **Output:** a [`ModelEntry`] reference pointing to the best candidate.
//!
//! # Constraints
//!
//! - No allocation beyond what `TaskShape` already carries.
//! - Deterministic: same registry + same shape always returns the same entry.
//! - Imports only `cascade_types` — no new workspace crate dependencies.
//!
//! SPORT: cascade-agents / model_profile — E-P12-01

use cascade_types::agent::Tier;
use cascade_types::model_registry::{ModelEntry, ModelRegistry, OutputFormat, TaskShape};

// ── Public helpers ────────────────────────────────────────────────────────────

/// Resolve the best [`ModelEntry`] for a spawned agent given a [`TaskShape`].
///
/// This is the profile-aware counterpart to `ModelRegistry::resolve(tier)`.
/// When the registry has no profile data for the requested tier, the call is
/// transparent — the tier-default entry is returned, preserving pre-P12 behavior.
///
/// # Why a free function instead of a trait method?
///
/// `ModelRegistry` lives in `cascade-types`, which must remain dependency-free
/// of `cascade-agents`. The agent layer owns the glue; the type layer stays
/// portable. This function is the glue.
///
/// # Arguments
///
/// * `registry` - The configured model registry (built from TOML or defaults).
/// * `shape` - Description of the spawned task's requirements.
///
/// # Returns
///
/// A reference to the selected `ModelEntry`. Lifetime is tied to `registry`.
///
/// # Example
///
/// ```rust
/// use cascade_agents::model_profile::resolve_model;
/// use cascade_types::model_registry::{
///     ModelRegistry, ModelOverrides, ModelEntry, ModelProfile,
///     TaskShape, OutputFormat,
/// };
/// use cascade_types::agent::Tier;
///
/// let mut reg = ModelRegistry::default();
///
/// // Pure tier path — no shape preferences → falls through to tier default.
/// let shape = TaskShape { tier: Tier::T2, output_format: None, needs: vec![] };
/// let entry = resolve_model(&reg, &shape);
/// assert_eq!(entry.model_id, "tier-t2");
/// ```
pub fn resolve_model<'r>(registry: &'r ModelRegistry, shape: &TaskShape) -> &'r ModelEntry {
    registry.best_for(shape)
}

/// Convenience: resolve a model entry for a tier with no profile preferences.
///
/// Equivalent to `registry.resolve(tier)` but goes through the same
/// `best_for` code path for consistency. Callers that don't have task-shape
/// information use this to make the intent explicit without passing an empty
/// `TaskShape` manually.
///
/// # Example
///
/// ```rust
/// use cascade_agents::model_profile::resolve_by_tier;
/// use cascade_types::model_registry::ModelRegistry;
/// use cascade_types::agent::Tier;
///
/// let reg = ModelRegistry::default();
/// let entry = resolve_by_tier(&reg, Tier::T3);
/// assert_eq!(entry.model_id, "tier-t3");
/// ```
pub fn resolve_by_tier(registry: &ModelRegistry, tier: Tier) -> &ModelEntry {
    let shape = TaskShape {
        tier,
        output_format: None,
        needs: vec![],
    };
    registry.best_for(&shape)
}

/// Build a [`TaskShape`] for a code-writing task at the given tier.
///
/// Convenience constructor used by `cascade-agents` dispatch sites that want
/// to prefer models with `"code"` in their `best_for` list and `Markdown`
/// default output, without importing `cascade_types` internals directly.
///
/// # Example
///
/// ```rust
/// use cascade_agents::model_profile::code_task_shape;
/// use cascade_types::agent::Tier;
///
/// let shape = code_task_shape(Tier::T2);
/// assert_eq!(shape.tier, Tier::T2);
/// assert!(shape.needs.contains(&"code".to_string()));
/// ```
pub fn code_task_shape(tier: Tier) -> TaskShape {
    TaskShape {
        tier,
        output_format: Some(OutputFormat::Markdown),
        needs: vec!["code".into(), "reasoning".into()],
    }
}

/// Build a [`TaskShape`] for a structured-output extraction task at the given tier.
///
/// Convenience constructor used by dispatch sites that need JSON output
/// and structured extraction (classification, triage, labeling, etc.).
///
/// # Example
///
/// ```rust
/// use cascade_agents::model_profile::structured_task_shape;
/// use cascade_types::agent::Tier;
///
/// let shape = structured_task_shape(Tier::T3);
/// assert_eq!(shape.tier, Tier::T3);
/// assert!(shape.needs.contains(&"structured-output".to_string()));
/// ```
pub fn structured_task_shape(tier: Tier) -> TaskShape {
    TaskShape {
        tier,
        output_format: Some(OutputFormat::Json),
        needs: vec!["structured-output".into(), "extraction".into()],
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::model_registry::{
        ModelEntry, ModelOverrides, ModelProfile, ModelRegistry, OutputFormat, RefusalSensitivity,
        ToolUseTrigger,
    };

    fn registry_with_profiled_t2() -> ModelRegistry {
        let mut reg = ModelRegistry::default();
        reg.apply_overrides(&ModelOverrides {
            t2: Some(ModelEntry::with_profile(
                "acme",
                "acme-code-v1",
                ModelProfile {
                    default_format: OutputFormat::Markdown,
                    tool_use_trigger: ToolUseTrigger::Eager,
                    refusal_sensitivity: RefusalSensitivity::Low,
                    best_for: vec!["code".into(), "reasoning".into()],
                },
            )),
            ..Default::default()
        });
        reg
    }

    fn registry_with_profiled_t3() -> ModelRegistry {
        let mut reg = ModelRegistry::default();
        reg.apply_overrides(&ModelOverrides {
            t3: Some(ModelEntry::with_profile(
                "acme",
                "acme-json-v1",
                ModelProfile {
                    default_format: OutputFormat::Json,
                    tool_use_trigger: ToolUseTrigger::Explicit,
                    refusal_sensitivity: RefusalSensitivity::Moderate,
                    best_for: vec!["structured-output".into(), "extraction".into()],
                },
            )),
            ..Default::default()
        });
        reg
    }

    // ── test 1: resolve_model with matching code shape ────────────────────────

    /// resolve_model returns the profiled entry when a needs tag matches.
    #[test]
    fn resolve_model_code_shape_hits_profiled_entry() {
        let reg = registry_with_profiled_t2();
        let shape = code_task_shape(Tier::T2);
        let entry = resolve_model(&reg, &shape);
        assert_eq!(entry.model_id, "acme-code-v1");
        assert_eq!(entry.provider_id, "acme");
    }

    // ── test 2: resolve_model with structured shape ───────────────────────────

    /// resolve_model returns the json-profiled entry for structured tasks.
    #[test]
    fn resolve_model_structured_shape_hits_json_entry() {
        let reg = registry_with_profiled_t3();
        let shape = structured_task_shape(Tier::T3);
        let entry = resolve_model(&reg, &shape);
        assert_eq!(entry.model_id, "acme-json-v1");
    }

    // ── test 3: resolve_by_tier is back-compat ────────────────────────────────

    /// resolve_by_tier returns the default tier entry from a default registry.
    #[test]
    fn resolve_by_tier_default_registry_back_compat() {
        let reg = ModelRegistry::default();
        let entry = resolve_by_tier(&reg, Tier::T1);
        assert_eq!(entry.model_id, "tier-t1");
        assert_eq!(entry.provider_id, "cascade-default");
    }

    // ── test 4: resolve_by_tier works after profile override ─────────────────

    /// resolve_by_tier still returns the right tier entry after overrides.
    #[test]
    fn resolve_by_tier_after_profile_override() {
        let reg = registry_with_profiled_t2();
        let entry = resolve_by_tier(&reg, Tier::T2);
        // Tier T2 was overridden — must return the overridden entry.
        assert_eq!(entry.model_id, "acme-code-v1");
        // Other tiers untouched.
        let t1 = resolve_by_tier(&reg, Tier::T1);
        assert_eq!(t1.provider_id, "cascade-default");
    }

    // ── test 5: code_task_shape shape construction ────────────────────────────

    /// code_task_shape returns the correct tier + needs + format.
    #[test]
    fn code_task_shape_fields() {
        let shape = code_task_shape(Tier::T2);
        assert_eq!(shape.tier, Tier::T2);
        assert_eq!(shape.output_format, Some(OutputFormat::Markdown));
        assert!(shape.needs.contains(&"code".to_string()));
        assert!(shape.needs.contains(&"reasoning".to_string()));
    }

    // ── test 6: structured_task_shape shape construction ─────────────────────

    /// structured_task_shape returns the correct tier + needs + format.
    #[test]
    fn structured_task_shape_fields() {
        let shape = structured_task_shape(Tier::T3);
        assert_eq!(shape.tier, Tier::T3);
        assert_eq!(shape.output_format, Some(OutputFormat::Json));
        assert!(shape.needs.contains(&"structured-output".to_string()));
        assert!(shape.needs.contains(&"extraction".to_string()));
    }

    // ── test 7: empty shape falls through to tier default ────────────────────

    /// resolve_model with empty needs + no format is equivalent to resolve(tier).
    #[test]
    fn resolve_model_empty_shape_tier_fallback() {
        let reg = ModelRegistry::default();
        let shape = TaskShape {
            tier: Tier::T0,
            output_format: None,
            needs: vec![],
        };
        let entry = resolve_model(&reg, &shape);
        assert_eq!(entry.model_id, "tier-t0");
    }
}
