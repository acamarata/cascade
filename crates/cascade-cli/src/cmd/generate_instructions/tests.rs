//! Tests for `cascade generate-instructions`.

use std::fs;

use cascade_core::cascade_resolution::TierResult;
use cascade_types::cascade_tier::CascadeTier;
use serde_json::Value;
use serial_test::serial;
use std::path::PathBuf;
use tempfile::TempDir;

use super::cc::{generate_cc, update_cc_settings_json, CASCADE_HEADER_MARKER};
use super::oc::generate_oc;

fn fake_tier_result(
    tier: CascadeTier,
    cascade_dir: &std::path::Path,
    instructions: &str,
) -> TierResult {
    TierResult {
        tier,
        found: true,
        path_searched: cascade_dir.to_path_buf(),
        instructions: instructions.to_string(),
    }
}

// ── test 1: CC CLAUDE.md written with cascade header ─────────────────────

/// CC generation writes CLAUDE.md with cascade.search instruction.
///
/// WHY: acceptance criterion — CLAUDE.md must contain `cascade.search`.
#[test]
#[serial(global_env)]
fn cc_claude_md_written() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    // Create a fake .cascade dir
    let cascade_dir = tmp.path().join("myproject").join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();
    fs::write(cascade_dir.join("CASCADE.md"), "project instructions").unwrap();

    let tier = fake_tier_result(CascadeTier::Ppc, &cascade_dir, "project instructions");
    let tier_root = cascade_dir.parent().unwrap();

    generate_cc(&tier, tier_root, "unix://~/.cascade/cascade.sock", false)
        .expect("generate_cc should succeed");

    let claude_md = tier_root.join(".claude").join("CLAUDE.md");
    assert!(claude_md.exists(), "CLAUDE.md must be created");
    let content = fs::read_to_string(&claude_md).unwrap();
    assert!(
        content.contains("cascade.search"),
        "CLAUDE.md must contain cascade.search"
    );
    assert!(
        content.contains(CASCADE_HEADER_MARKER),
        "CLAUDE.md must contain cascade header marker"
    );
    assert!(
        content.contains("project instructions"),
        "CLAUDE.md must contain tier instructions"
    );
}

// ── test 2: CC settings.json contains cascade MCP entry ──────────────────

/// Written settings.json must include cascade mcpServers entry.
///
/// WHY: acceptance criterion from spec.
#[test]
#[serial(global_env)]
fn cc_settings_json_mcp_entry() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let claude_dir = tmp.path().join(".claude");
    fs::create_dir_all(&claude_dir).unwrap();
    let settings_path = claude_dir.join("settings.json");

    update_cc_settings_json(&settings_path, false).expect("should succeed");

    let content = fs::read_to_string(&settings_path).unwrap();
    let json: Value = serde_json::from_str(&content).unwrap();

    let cascade_entry = json.pointer("/mcpServers/cascade");
    assert!(
        cascade_entry.is_some(),
        "settings.json must have mcpServers.cascade"
    );
    assert_eq!(
        cascade_entry.unwrap()["command"].as_str(),
        Some("cascade"),
        "command must be 'cascade'"
    );
    assert_eq!(
        cascade_entry.unwrap()["args"],
        serde_json::json!(["mcp", "stdio"]),
        "args must be ['mcp', 'stdio']"
    );
}

// ── test 3: AGENTS.md is a symlink to CLAUDE.md ───────────────────────────

/// AGENTS.md is created as a symlink, not a copy.
///
/// WHY: OC/CC compat rule and acceptance criterion.
#[test]
#[serial(global_env)]
#[cfg(unix)]
fn agents_md_is_symlink() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();
    fs::write(cascade_dir.join("CASCADE.md"), "gci instructions").unwrap();

    let tier = fake_tier_result(CascadeTier::Gci, &cascade_dir, "gci instructions");

    generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", false)
        .expect("generate_cc should succeed");

    let agents_md = tmp.path().join(".claude").join("AGENTS.md");
    assert!(
        agents_md.exists() || agents_md.is_symlink(),
        "AGENTS.md must exist"
    );
    assert!(
        agents_md.is_symlink(),
        "AGENTS.md must be a symlink (not a plain file)"
    );
    let target = fs::read_link(&agents_md).unwrap();
    assert_eq!(
        target,
        PathBuf::from("CLAUDE.md"),
        "AGENTS.md must symlink to CLAUDE.md"
    );
}

// ── test 4: idempotency — second run does not double-append ──────────────

/// Running generate_cc twice does not double-append the cascade header.
///
/// WHY: acceptance criterion from spec.
#[test]
#[serial(global_env)]
fn cc_idempotent() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();
    fs::write(cascade_dir.join("CASCADE.md"), "tier text").unwrap();

    let tier = fake_tier_result(CascadeTier::Gci, &cascade_dir, "tier text");

    generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", false).unwrap();
    generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", false).unwrap();

    let claude_md = tmp.path().join(".claude").join("CLAUDE.md");
    let content = fs::read_to_string(&claude_md).unwrap();

    let marker_count = content.matches(CASCADE_HEADER_MARKER).count();
    assert_eq!(
        marker_count, 1,
        "cascade header must appear exactly once (idempotent)"
    );
}

// ── test 5: dry-run writes nothing ────────────────────────────────────────

/// Dry-run mode prints diff but does not write any files.
///
/// WHY: acceptance criterion — `--dry-run` must not modify files.
#[test]
#[serial(global_env)]
fn dry_run_writes_nothing() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();
    fs::write(cascade_dir.join("CASCADE.md"), "dry run text").unwrap();

    let tier = fake_tier_result(CascadeTier::Gci, &cascade_dir, "dry run text");

    generate_cc(
        &tier,
        tmp.path(),
        "unix://~/.cascade/cascade.sock",
        /* dry_run= */ true,
    )
    .unwrap();

    let claude_md = tmp.path().join(".claude").join("CLAUDE.md");
    assert!(
        !claude_md.exists(),
        "CLAUDE.md must NOT be created in dry-run mode"
    );
}

// ── test 6: settings.json additive (does not remove existing entries) ─────

/// Existing mcpServers entries are preserved when adding cascade.
///
/// WHY: spec requires additive, not destructive merge.
#[test]
#[serial(global_env)]
fn settings_json_additive() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    let settings_path = tmp.path().join("settings.json");

    // Write existing settings with another MCP server
    let initial = serde_json::json!({
        "mcpServers": {
            "other-server": {
                "command": "other",
                "args": []
            }
        }
    });
    fs::write(
        &settings_path,
        serde_json::to_string_pretty(&initial).unwrap(),
    )
    .unwrap();

    update_cc_settings_json(&settings_path, false).unwrap();

    let content = fs::read_to_string(&settings_path).unwrap();
    let json: Value = serde_json::from_str(&content).unwrap();

    assert!(
        json.pointer("/mcpServers/other-server").is_some(),
        "existing mcpServers entry must be preserved"
    );
    assert!(
        json.pointer("/mcpServers/cascade").is_some(),
        "cascade entry must be added"
    );
}

// ── test 7: OC instructions field set in per-project opencode.json ────────

/// `generate_oc` sets `instructions` field in per-project opencode.json.
///
/// WHY: T-P4-E02-29 acceptance — OC generate-instructions writes instructions field.
#[test]
#[serial(global_env)]
fn oc_generate_sets_instructions_field() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();

    let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "repo instructions");
    let tier_root = cascade_dir.parent().unwrap();

    generate_oc(&tier, tier_root, false).expect("generate_oc should succeed");

    let project_oc_json = tier_root.join("opencode.json");
    assert!(
        project_oc_json.exists(),
        "per-project opencode.json must be created"
    );

    let content = fs::read_to_string(&project_oc_json).unwrap();
    let json: Value = serde_json::from_str(&content).unwrap();
    assert_eq!(
        json["instructions"].as_str(),
        Some(".cascade/opencode-instructions.md"),
        "instructions field must point to generated file"
    );
}

/// `generate_oc` opencode-instructions.md contains `cascade.search`.
///
/// WHY: T-P4-E02-29 acceptance criterion — instructions file mentions cascade.search.
#[test]
#[serial(global_env)]
fn oc_generate_instructions_md_contains_cascade_search() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();

    let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "");
    let tier_root = cascade_dir.parent().unwrap();

    generate_oc(&tier, tier_root, false).expect("generate_oc should succeed");

    let instr_path = cascade_dir.join("opencode-instructions.md");
    assert!(
        instr_path.exists(),
        "opencode-instructions.md must be created"
    );

    let content = fs::read_to_string(&instr_path).unwrap();
    assert!(
        content.contains("cascade.search"),
        "opencode-instructions.md must contain cascade.search: {content}"
    );
}

/// Per-project opencode.json instructions field is idempotent.
///
/// WHY: running twice must not corrupt the file.
#[test]
#[serial(global_env)]
fn oc_project_instructions_idempotent() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();

    let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "");
    let tier_root = cascade_dir.parent().unwrap();
    let project_oc_json = tier_root.join("opencode.json");

    generate_oc(&tier, tier_root, false).unwrap();
    generate_oc(&tier, tier_root, false).unwrap();

    let content = fs::read_to_string(&project_oc_json).unwrap();
    let json: Value = serde_json::from_str(&content).unwrap();
    // "instructions" key should appear exactly once (JSON object semantics)
    assert_eq!(
        json["instructions"].as_str(),
        Some(".cascade/opencode-instructions.md"),
    );
}

/// Per-project opencode.json instructions field dry-run writes nothing.
///
/// WHY: --dry-run must not create files.
#[test]
#[serial(global_env)]
fn oc_project_instructions_dry_run_no_write() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = TempDir::new().unwrap();
    std::env::set_var("HOME", tmp.path().to_str().unwrap());

    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();

    let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "");
    let tier_root = cascade_dir.parent().unwrap();
    let project_oc_json = tier_root.join("opencode.json");

    generate_oc(&tier, tier_root, /* dry_run= */ true).unwrap();

    assert!(
        !project_oc_json.exists(),
        "dry-run must not write project opencode.json"
    );
}
