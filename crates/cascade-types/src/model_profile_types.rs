//! Behavioral-profile types for the model registry (P12).
//!
//! # Purpose
//!
//! Defines the enums and structs that describe per-model behavioral
//! characteristics and the `TaskShape` used to select among models.
//! Kept in a dedicated file so `model_registry.rs` stays within the 300-line cap.
//!
//! All types use `#[serde(default)]` so TOML/JSON without profile fields
//! continues to deserialise correctly — back-compat with pre-P12 configs.
//!
//! # Inputs / Outputs
//!
//! - **Input:** TOML `[models.tN.profile]` inline table (via `ModelEntry`).
//! - **Output:** scored in [`crate::model_registry::ModelRegistry::best_for`].
//!
//! # Constraints
//!
//! - No runtime-dep beyond serde + std. Zero I/O.
//! - All types are `Clone + PartialEq + Serialize + Deserialize`.

use crate::agent::Tier;
use serde::{Deserialize, Serialize};

// ── OutputFormat ──────────────────────────────────────────────────────────────

/// The preferred output format a model produces unprompted.
///
/// Used in both `ModelProfile` (model capability) and `TaskShape` (task need).
/// When both sides specify a format and they match, that entry gains a
/// tie-breaking point in `best_for` selection.
///
/// # TOML values: `"markdown"` | `"plain"` | `"json"`
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum OutputFormat {
    /// Rich markdown with headings, lists, and code fences (most models default).
    #[default]
    Markdown,
    /// Plain prose with no markup — best for short answers or log lines.
    Plain,
    /// Machine-parseable JSON objects — best for structured extraction tasks.
    Json,
}

// ── ToolUseTrigger ────────────────────────────────────────────────────────────

/// How eagerly a model calls tools without being explicitly prompted.
///
/// `Eager` models call tools at the first opportunity; `Explicit` models wait
/// until the task description names a specific tool. Matching the trigger to
/// the task prevents runaway tool chaining or missed tool use.
///
/// # TOML values: `"eager"` | `"explicit"`
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ToolUseTrigger {
    /// Model calls tools when it judges them relevant, without explicit prompting.
    Eager,
    /// Model waits for explicit mention of a tool before calling it.
    #[default]
    Explicit,
}

// ── RefusalSensitivity ────────────────────────────────────────────────────────

/// How likely a model is to refuse or add unsolicited safety caveats.
///
/// Higher sensitivity models are more conservative on ambiguous tasks.
/// Lower sensitivity models are more likely to attempt the task directly.
/// This is a behavioral observation, not a capability claim.
///
/// # TOML values: `"low"` | `"moderate"` | `"high"`
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum RefusalSensitivity {
    /// Rarely refuses ambiguous tasks; minimal unsolicited caveats.
    Low,
    /// Balanced — may add caveats on clearly sensitive tasks.
    #[default]
    Moderate,
    /// Conservative; may refuse or caveat borderline tasks.
    High,
}

// ── ModelProfile ──────────────────────────────────────────────────────────────

/// Behavioral characteristics of a specific model entry.
///
/// All fields use `#[serde(default)]` — a TOML entry without a `profile`
/// block deserialises to `ModelProfile::default()`, preserving back-compat.
///
/// # TOML example
///
/// ```toml
/// [models.t2.profile]
/// default_format      = "markdown"
/// tool_use_trigger    = "eager"
/// refusal_sensitivity = "low"
/// best_for            = ["code", "reasoning"]
/// ```
///
/// # Notes
///
/// These fields reflect observed behavioral tendencies, not guarantees.
/// Operators should update profiles when they switch model versions.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ModelProfile {
    /// The format this model produces in its default response style.
    pub default_format: OutputFormat,

    /// How eagerly this model invokes tools without explicit instruction.
    pub tool_use_trigger: ToolUseTrigger,

    /// How likely this model is to refuse or caveat ambiguous tasks.
    pub refusal_sensitivity: RefusalSensitivity,

    /// Task-type tags this model excels at (e.g. `"reasoning"`, `"code"`,
    /// `"structured-output"`, `"summarization"`). Used for overlap scoring in
    /// [`crate::model_registry::ModelRegistry::best_for`].
    #[serde(default)]
    pub best_for: Vec<String>,
}

// ── TaskShape ─────────────────────────────────────────────────────────────────

/// A description of what an agent spawn needs from a model.
///
/// Passed to [`crate::model_registry::ModelRegistry::best_for`] to select
/// the most suitable [`crate::model_registry::ModelEntry`] for the task
/// among same-tier candidates.
///
/// # Selection algorithm
///
/// 1. Filter to the entry for `shape.tier`.
/// 2. Score the entry's profile against the shape:
///    - +1 for each `needs` tag that appears in `profile.best_for`.
///    - +1 if `output_format` matches `profile.default_format`.
/// 3. Return the entry with the highest score; fall back to `resolve(tier)`
///    when score is zero (no profile or no matching tags).
///
/// # Example
///
/// ```
/// use cascade_types::model_profile_types::{TaskShape, OutputFormat};
/// use cascade_types::agent::Tier;
///
/// let shape = TaskShape {
///     tier: Tier::T2,
///     output_format: Some(OutputFormat::Json),
///     needs: vec!["structured-output".into(), "code".into()],
/// };
/// ```
#[derive(Debug, Clone)]
pub struct TaskShape {
    /// Required tier — only that tier's entry is considered.
    pub tier: Tier,

    /// The output format the calling task expects, if any.
    ///
    /// When `Some`, an entry whose `profile.default_format` matches gains +1.
    pub output_format: Option<OutputFormat>,

    /// Task-type tags the calling task cares about.
    ///
    /// Each tag present in a candidate entry's `profile.best_for` adds +1 to
    /// that entry's score.
    pub needs: Vec<String>,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn output_format_default_is_markdown() {
        assert_eq!(OutputFormat::default(), OutputFormat::Markdown);
    }

    #[test]
    fn tool_use_trigger_default_is_explicit() {
        assert_eq!(ToolUseTrigger::default(), ToolUseTrigger::Explicit);
    }

    #[test]
    fn refusal_sensitivity_default_is_moderate() {
        assert_eq!(RefusalSensitivity::default(), RefusalSensitivity::Moderate);
    }

    #[test]
    fn model_profile_default_fields() {
        let p = ModelProfile::default();
        assert_eq!(p.default_format, OutputFormat::Markdown);
        assert_eq!(p.tool_use_trigger, ToolUseTrigger::Explicit);
        assert_eq!(p.refusal_sensitivity, RefusalSensitivity::Moderate);
        assert!(p.best_for.is_empty());
    }

    #[test]
    fn model_profile_json_round_trip() {
        let p = ModelProfile {
            default_format: OutputFormat::Json,
            tool_use_trigger: ToolUseTrigger::Eager,
            refusal_sensitivity: RefusalSensitivity::Low,
            best_for: vec!["code".into(), "reasoning".into()],
        };
        let json = serde_json::to_string(&p).expect("serialize");
        let parsed: ModelProfile = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(parsed, p);
    }

    #[test]
    fn task_shape_fields() {
        let shape = TaskShape {
            tier: Tier::T2,
            output_format: Some(OutputFormat::Json),
            needs: vec!["code".into()],
        };
        assert_eq!(shape.tier, Tier::T2);
        assert_eq!(shape.output_format, Some(OutputFormat::Json));
        assert_eq!(shape.needs.len(), 1);
    }

    #[test]
    fn output_format_serde_lowercase() {
        let json = serde_json::to_string(&OutputFormat::Json).unwrap();
        assert_eq!(json, "\"json\"");
        let parsed: OutputFormat = serde_json::from_str("\"markdown\"").unwrap();
        assert_eq!(parsed, OutputFormat::Markdown);
    }

    #[test]
    fn tool_use_trigger_serde_lowercase() {
        let json = serde_json::to_string(&ToolUseTrigger::Eager).unwrap();
        assert_eq!(json, "\"eager\"");
    }

    #[test]
    fn refusal_sensitivity_serde_lowercase() {
        let json = serde_json::to_string(&RefusalSensitivity::High).unwrap();
        assert_eq!(json, "\"high\"");
    }
}
