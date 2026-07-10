//! Soul — personality and output-style layer for Cascade agents.
//!
//! Purpose: define the style contract for each agent's generated text. A "soul"
//!   controls verbosity, inter-agent terse mode, style tags, and output policy.
//!   The rendered `system_block` is injected as a `{{soul_block}}` template slot
//!   into agent system prompts.
//!
//! Inputs:
//!   - An optional soul name (matches a YAML file in `data/souls/`).
//!   - A `SoulOverride` that selectively overrides fields from the named soul.
//!
//! Outputs:
//!   - `ResolvedSoul` with all fields merged and a rendered `system_block`.
//!
//! Constraints:
//!   - Soul YAMLs are embedded at compile time via `include_str!`.
//!   - Falls back to `"professional-minimal"` when `soul_ref` is `None`.
//!   - `verbosity` is clamped to `1..=10`.
//!   - No async; pure functions only.
//!
//! SPORT: cascade-agents / soul — soul-01

use serde::Deserialize;

// ── Embedded soul YAML data ───────────────────────────────────────────────────

const SOUL_PROFESSIONAL_MINIMAL: &str = include_str!("../data/souls/professional-minimal.yaml");
const SOUL_INTER_AGENT: &str = include_str!("../data/souls/inter-agent.yaml");
const SOUL_VERBOSE_TEACHER: &str = include_str!("../data/souls/verbose-teacher.yaml");

// ── Raw YAML schema ───────────────────────────────────────────────────────────

/// On-disk representation of a soul YAML file.
#[derive(Debug, Deserialize)]
struct RawSoul {
    #[allow(dead_code)]
    name: String,
    #[allow(dead_code)]
    description: String,
    verbosity_default: u8,
    inter_agent: bool,
    #[serde(default)]
    style_tags: Vec<String>,
    output_policy: Option<String>,
}

// ── Public types ──────────────────────────────────────────────────────────────

/// A fully resolved soul with all overrides applied and a rendered system block.
#[derive(Debug, Clone)]
pub struct ResolvedSoul {
    /// Output verbosity on a 1–10 scale (see [`verbosity_instruction`]).
    pub verbosity: u8,
    /// When `true`, output uses field-labelled terse format (agent-to-agent).
    pub inter_agent: bool,
    /// Descriptive style tags (e.g. `"concise"`, `"educational"`).
    pub style_tags: Vec<String>,
    /// Free-text output policy directive, if any.
    pub output_policy: Option<String>,
    /// The rendered style instructions injected as `{{soul_block}}`.
    pub system_block: String,
}

/// Selective overrides applied on top of a named soul's defaults.
///
/// `None` fields are left unchanged; `Some` fields replace the soul's value.
#[derive(Debug, Default, Clone)]
pub struct SoulOverride {
    /// Override verbosity (clamped to 1–10).
    pub verbosity: Option<u8>,
    /// Override inter-agent terse mode.
    pub inter_agent: Option<bool>,
    /// Override style tags (replaces the entire vec).
    pub style_tags: Option<Vec<String>>,
    /// Override output policy.
    pub output_policy: Option<String>,
}

// ── Verbosity scale ───────────────────────────────────────────────────────────

/// Maps a verbosity level to a human-readable instruction string.
///
/// The scale runs from 1 (ultra-terse, caveman) to 10 (exhaustive professor).
/// Level 3 (`"professional-minimal"`) is the default.
pub fn verbosity_instruction(v: u8) -> &'static str {
    match v.clamp(1, 10) {
        1 => "Caveman: one line only. No explanation. Subject + verb + object.",
        2 => "Ultra-terse: one sentence maximum. Core fact only.",
        3 => "Professional-minimal: concise and direct. No filler phrases. State the result.",
        4 => "Concise-professional: brief paragraphs. Essential context only.",
        5 => "Standard: complete sentences, moderate detail, normal prose.",
        6 => "Detailed: include supporting detail and relevant context.",
        7 => "Thorough: cover edge cases, tradeoffs, and alternatives briefly.",
        8 => "Comprehensive: full coverage with examples and rationale.",
        9 => "Verbose: extensive prose, multiple examples, all caveats noted.",
        10 => "Professor: exhaustive explanations, derivations, and full context.",
        _ => unreachable!(),
    }
}

// ── Soul resolution ───────────────────────────────────────────────────────────

/// Look up a named soul YAML string.
///
/// Returns `None` if the name is unknown; callers fall back to the default.
fn soul_yaml_for_name(name: &str) -> Option<&'static str> {
    match name {
        "professional-minimal" => Some(SOUL_PROFESSIONAL_MINIMAL),
        "inter-agent" => Some(SOUL_INTER_AGENT),
        "verbose-teacher" => Some(SOUL_VERBOSE_TEACHER),
        _ => None,
    }
}

/// Parse a raw soul YAML. Panics at startup if the embedded YAML is malformed
/// (compile-time data; a panic here is a programmer error, not a runtime one).
fn parse_soul(yaml: &str) -> RawSoul {
    serde_yaml::from_str(yaml).expect("embedded soul YAML must be valid")
}

/// Resolve a soul by name, apply overrides, and return a [`ResolvedSoul`].
///
/// - If `soul_ref` is `None` or names an unknown soul, falls back to
///   `"professional-minimal"`.
/// - Override fields that are `Some` replace the named soul's value; `None`
///   fields keep the soul's default.
pub fn resolve_soul(soul_ref: Option<&str>, overrides: SoulOverride) -> ResolvedSoul {
    let yaml = soul_ref
        .and_then(soul_yaml_for_name)
        .unwrap_or(SOUL_PROFESSIONAL_MINIMAL);

    let raw = parse_soul(yaml);

    let verbosity = overrides
        .verbosity
        .unwrap_or(raw.verbosity_default)
        .clamp(1, 10);
    let inter_agent = overrides.inter_agent.unwrap_or(raw.inter_agent);
    let style_tags = overrides.style_tags.unwrap_or(raw.style_tags);
    let output_policy = overrides.output_policy.or(raw.output_policy);

    let soul = ResolvedSoul {
        verbosity,
        inter_agent,
        style_tags: style_tags.clone(),
        output_policy: output_policy.clone(),
        system_block: String::new(), // placeholder — populated below
    };

    let block = render_soul_block(&ResolvedSoul {
        verbosity,
        inter_agent,
        style_tags,
        output_policy,
        system_block: String::new(),
    });

    ResolvedSoul {
        system_block: block,
        ..soul
    }
}

/// Render the `{{soul_block}}` string injected into agent system prompts.
///
/// The block instructs the LLM on verbosity, format, and output policy.
pub fn render_soul_block(soul: &ResolvedSoul) -> String {
    let mut lines: Vec<String> = Vec::new();

    lines.push("## Output style".into());
    lines.push(format!(
        "Verbosity level {}/10 — {}",
        soul.verbosity,
        verbosity_instruction(soul.verbosity)
    ));

    if soul.inter_agent {
        lines.push(
            "Format: field-labelled output only. \
             Use `field: value` pairs. No prose preamble. No explanation unless explicitly requested."
                .into(),
        );
    }

    if !soul.style_tags.is_empty() {
        lines.push(format!("Style: {}", soul.style_tags.join(", ")));
    }

    if let Some(ref policy) = soul.output_policy {
        lines.push(format!("Policy: {policy}"));
    }

    lines.join("\n")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// verbosity_instruction(1) and verbosity_instruction(10) must be distinct.
    #[test]
    fn verbosity_1_vs_10_produce_different_strings() {
        assert_ne!(
            verbosity_instruction(1),
            verbosity_instruction(10),
            "level 1 and level 10 must produce different instruction strings"
        );
    }

    /// Default soul (no ref, no overrides) must have verbosity == 3.
    #[test]
    fn default_verbosity_is_3() {
        let soul = resolve_soul(None, SoulOverride::default());
        assert_eq!(
            soul.verbosity, 3,
            "default soul verbosity must be 3 (professional-minimal)"
        );
    }

    /// An override verbosity of 7 must produce a resolved soul with verbosity 7.
    #[test]
    fn resolve_soul_merges_override() {
        let soul = resolve_soul(
            None,
            SoulOverride {
                verbosity: Some(7),
                ..Default::default()
            },
        );
        assert_eq!(soul.verbosity, 7, "override verbosity must be applied");
    }

    /// inter_agent: true must change the rendered system_block.
    #[test]
    fn inter_agent_changes_style_block() {
        let non_inter = resolve_soul(None, SoulOverride::default());
        let inter = resolve_soul(Some("inter-agent"), SoulOverride::default());
        assert!(
            inter.system_block.contains("field: value"),
            "inter-agent soul must mention field-labelled format"
        );
        assert!(
            inter.inter_agent,
            "inter_agent flag must be true for inter-agent soul"
        );
        assert_ne!(
            non_inter.system_block, inter.system_block,
            "inter-agent system_block must differ from non-inter"
        );
    }
}
