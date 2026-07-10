//! Integration tests for `cascade_types::model_registry` and
//! `cascade_types::model_profile_types` (P12).
//!
//! Covers: profile deserialization + back-compat (no-profile config),
//! `best_for` selection (same-tier match, format match, empty fallback to tier).

use cascade_types::{
    agent::Tier,
    model_registry::{
        ModelEntry, ModelOverrides, ModelProfile, ModelRegistry, OutputFormat, RefusalSensitivity,
        TaskShape, ToolUseTrigger,
    },
};

// ── helpers ───────────────────────────────────────────────────────────────────

fn code_profile() -> ModelProfile {
    ModelProfile {
        default_format: OutputFormat::Markdown,
        tool_use_trigger: ToolUseTrigger::Eager,
        refusal_sensitivity: RefusalSensitivity::Low,
        best_for: vec!["code".into(), "reasoning".into()],
    }
}

fn json_profile() -> ModelProfile {
    ModelProfile {
        default_format: OutputFormat::Json,
        tool_use_trigger: ToolUseTrigger::Explicit,
        refusal_sensitivity: RefusalSensitivity::Moderate,
        best_for: vec!["structured-output".into(), "extraction".into()],
    }
}

// ── test 1: all four tiers resolve from defaults ──────────────────────────────

#[test]
fn default_registry_resolves_all_tiers() {
    let reg = ModelRegistry::default();
    assert_eq!(reg.resolve(Tier::T0).provider_id, "cascade-default");
    assert_eq!(reg.resolve(Tier::T1).provider_id, "cascade-default");
    assert_eq!(reg.resolve(Tier::T2).provider_id, "cascade-default");
    assert_eq!(reg.resolve(Tier::T3).provider_id, "cascade-default");
    assert_eq!(reg.resolve(Tier::T0).model_id, "tier-t0");
    assert_eq!(reg.resolve(Tier::T3).model_id, "tier-t3");
    assert!(reg.resolve(Tier::T0).profile.is_none());
}

// ── test 2: TOML override replaces only the specified tier ────────────────────

#[test]
fn toml_override_replaces_single_tier() {
    let mut reg = ModelRegistry::default();
    reg.apply_overrides(&ModelOverrides {
        t2: Some(ModelEntry::new("openai", "gpt-4o")),
        ..Default::default()
    });
    assert_eq!(reg.resolve(Tier::T2).provider_id, "openai");
    assert_eq!(reg.resolve(Tier::T0).provider_id, "cascade-default");
    assert_eq!(reg.resolve(Tier::T1).provider_id, "cascade-default");
    assert_eq!(reg.resolve(Tier::T3).provider_id, "cascade-default");
}

// ── test 3: TOML round-trip for ModelOverrides ────────────────────────────────

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
    assert!(ov.t0.is_none());
    assert!(ov.t2.is_none());
    let t1 = ov.t1.expect("t1 must be present");
    assert_eq!(t1.provider_id, "anthropic");
    let t3 = ov.t3.expect("t3 must be present");
    assert_eq!(t3.provider_id, "local");
}

// ── test 4: all four overrides applied at once ────────────────────────────────

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

// ── test 5: registry JSON serialisation round-trip ────────────────────────────

#[test]
fn registry_json_round_trip() {
    let reg = ModelRegistry::default();
    let json = serde_json::to_string(&reg).expect("serialize");
    let parsed: ModelRegistry = serde_json::from_str(&json).expect("deserialize");
    assert_eq!(parsed.resolve(Tier::T0).model_id, "tier-t0");
    assert_eq!(parsed.resolve(Tier::T3).model_id, "tier-t3");
}

// ── test 6: empty TOML models table leaves defaults intact ────────────────────

#[test]
fn empty_models_table_keeps_defaults() {
    let toml_str = "[models]\n";
    #[derive(serde::Deserialize)]
    struct Wrapper {
        models: ModelOverrides,
    }
    let parsed: Wrapper = toml::from_str(toml_str).expect("TOML must parse");
    let mut reg = ModelRegistry::default();
    reg.apply_overrides(&parsed.models);
    assert_eq!(reg.resolve(Tier::T2).provider_id, "cascade-default");
}

// ── test 7: profile deserialization from TOML (P12 back-compat) ──────────────

#[test]
fn profile_toml_round_trip() {
    let toml_str = r#"
[models]
[models.t2]
provider_id = "acme"
model_id    = "acme-fast-v2"

[models.t2.profile]
default_format      = "json"
tool_use_trigger    = "eager"
refusal_sensitivity = "low"
best_for            = ["code", "structured-output"]
"#;
    #[derive(serde::Deserialize)]
    struct Wrapper {
        models: ModelOverrides,
    }
    let parsed: Wrapper = toml::from_str(toml_str).expect("TOML must parse");
    let entry = parsed.models.t2.expect("t2 must be present");
    assert_eq!(entry.provider_id, "acme");
    let profile = entry.profile.expect("profile must be present");
    assert_eq!(profile.default_format, OutputFormat::Json);
    assert_eq!(profile.tool_use_trigger, ToolUseTrigger::Eager);
    assert_eq!(profile.refusal_sensitivity, RefusalSensitivity::Low);
    assert_eq!(profile.best_for, vec!["code", "structured-output"]);
}

// ── test 8: back-compat — no-profile entry deserialises as None ──────────────

#[test]
fn no_profile_entry_deserialises_as_none() {
    let toml_str = r#"
[models]
t1 = { provider_id = "legacy", model_id = "legacy-v1" }
"#;
    #[derive(serde::Deserialize)]
    struct Wrapper {
        models: ModelOverrides,
    }
    let parsed: Wrapper = toml::from_str(toml_str).expect("TOML must parse");
    let entry = parsed.models.t1.expect("t1 must be present");
    assert!(entry.profile.is_none(), "absent profile must be None");
}

// ── test 9: best_for — same-tier match on best_for tag ───────────────────────

#[test]
fn best_for_same_tier_tag_match() {
    let mut reg = ModelRegistry::default();
    reg.apply_overrides(&ModelOverrides {
        t2: Some(ModelEntry::with_profile(
            "acme",
            "acme-code",
            code_profile(),
        )),
        ..Default::default()
    });
    let shape = TaskShape {
        tier: Tier::T2,
        output_format: None,
        needs: vec!["code".into()],
    };
    let entry = reg.best_for(&shape);
    assert_eq!(entry.model_id, "acme-code");
}

// ── test 10: best_for — format match ─────────────────────────────────────────

#[test]
fn best_for_format_match() {
    let mut reg = ModelRegistry::default();
    reg.apply_overrides(&ModelOverrides {
        t3: Some(ModelEntry::with_profile(
            "acme",
            "acme-json",
            json_profile(),
        )),
        ..Default::default()
    });
    let shape = TaskShape {
        tier: Tier::T3,
        output_format: Some(OutputFormat::Json),
        needs: vec![],
    };
    let entry = reg.best_for(&shape);
    assert_eq!(entry.model_id, "acme-json");
}

// ── test 11: best_for — no-profile falls back to tier ────────────────────────

#[test]
fn best_for_no_profile_falls_back_to_tier() {
    let reg = ModelRegistry::default();
    let shape = TaskShape {
        tier: Tier::T1,
        output_format: Some(OutputFormat::Plain),
        needs: vec!["reasoning".into()],
    };
    let entry = reg.best_for(&shape);
    assert_eq!(entry.model_id, "tier-t1");
    assert_eq!(entry.provider_id, "cascade-default");
}

// ── test 12: best_for — empty shape returns tier entry ───────────────────────

#[test]
fn best_for_empty_shape_returns_tier() {
    let mut reg = ModelRegistry::default();
    reg.apply_overrides(&ModelOverrides {
        t2: Some(ModelEntry::with_profile(
            "acme",
            "acme-fast",
            code_profile(),
        )),
        ..Default::default()
    });
    let shape = TaskShape {
        tier: Tier::T2,
        output_format: None,
        needs: vec![],
    };
    let entry = reg.best_for(&shape);
    assert_eq!(entry.model_id, "acme-fast");
}

// ── test 13: with_profile constructor ────────────────────────────────────────

#[test]
fn model_entry_with_profile_constructor() {
    let entry = ModelEntry::with_profile("prov", "mdl", json_profile());
    assert_eq!(entry.provider_id, "prov");
    let p = entry.profile.expect("profile must be Some");
    assert_eq!(p.default_format, OutputFormat::Json);
}

// ── test 14: entry with profile JSON round-trip ───────────────────────────────

#[test]
fn entry_with_profile_json_round_trip() {
    let entry = ModelEntry::with_profile("acme", "acme-v1", code_profile());
    let json = serde_json::to_string(&entry).expect("serialize");
    let parsed: ModelEntry = serde_json::from_str(&json).expect("deserialize");
    let p = parsed.profile.expect("profile must survive round-trip");
    assert_eq!(p.default_format, OutputFormat::Markdown);
    assert_eq!(p.tool_use_trigger, ToolUseTrigger::Eager);
    assert_eq!(p.best_for, vec!["code", "reasoning"]);
}
