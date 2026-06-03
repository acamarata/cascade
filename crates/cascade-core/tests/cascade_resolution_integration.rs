//! Integration tests for `cascade_core::cascade_resolve` and `cascade_core::compat_gen`
//! using real temporary directories.
//!
//! # Purpose
//!
//! Verifies that the 6-tier cascade resolution engine correctly merges configuration
//! from all six tiers (GCI, PCI, APC, PPC, PRC, PAC) with proper precedence,
//! and that the compat-file generator produces CLAUDE.md + AGENTS.md files
//! correctly at each tier directory.

use cascade_core::cascade_resolve::resolve_cascade;
use cascade_core::compat_gen::generate_all;
use std::fs;
use std::path::Path;
use tempfile::TempDir;

/// Helper: creates a tier fixture at the given path with config.toml and optional CASCADE.md.
fn create_tier_fixture(dir: &Path, tier_name: &str, instructions: &str, rules: &[&str]) {
    let cascade_dir = dir.join(".cascade");
    fs::create_dir_all(&cascade_dir).expect("failed to create .cascade dir");

    // Write config.toml with instructions, rules, and tier-specific marker
    let mut toml_content = format!(
        "instructions = \"{}\\n(Tier: {})\"",
        instructions, tier_name
    );
    if !rules.is_empty() {
        toml_content.push_str("\nrules = [\n");
        for rule in rules {
            toml_content.push_str(&format!("  \"{}\",\n", rule));
        }
        toml_content.push_str("]\n");
    }
    fs::write(cascade_dir.join("config.toml"), &toml_content).expect("failed to write config.toml");

    // Write CASCADE.md as fallback
    fs::write(
        cascade_dir.join("CASCADE.md"),
        format!("# {} Tier\n\n{}\n", tier_name, instructions),
    )
    .expect("failed to write CASCADE.md");
}

/// Test 1: Six-tier fixture verifies GCI instructions override all lower tiers.
#[test]
fn test_six_tier_priority() {
    let root = TempDir::new().unwrap();
    let root_path = root.path();

    // Simulate each tier by creating nested directories
    let gci_path = root_path;
    let aci_path = root_path.join("Downloads");
    let apc_path = root_path.join("Sites");
    let ppc_path = root_path.join("myproject");
    let prc_path = ppc_path.join("repo");

    fs::create_dir_all(&aci_path).unwrap();
    fs::create_dir_all(&apc_path).unwrap();
    fs::create_dir_all(&ppc_path).unwrap();
    fs::create_dir_all(&prc_path).unwrap();

    // Create fixtures for each tier with unique instructions
    create_tier_fixture(gci_path, "GCI", "Global instructions", &["GCI rule 1"]);
    create_tier_fixture(&aci_path, "PCI", "Personal instructions", &["PCI rule 1"]);
    create_tier_fixture(
        &apc_path,
        "APC",
        "All-Projects instructions",
        &["APC rule 1"],
    );
    create_tier_fixture(&ppc_path, "PPC", "Project instructions", &["PPC rule 1"]);
    create_tier_fixture(&prc_path, "PRC", "Repo instructions", &["PRC rule 1"]);

    // Set HOME to the temp root so cascade_resolve finds GCI
    let old_home = std::env::var("HOME").ok();
    std::env::set_var("HOME", root_path);

    // Override CASCADE_APC_PATH to point to our temp APC
    let old_apc = std::env::var("CASCADE_APC_PATH").ok();
    std::env::set_var("CASCADE_APC_PATH", apc_path.to_string_lossy().to_string());

    let resolved = resolve_cascade(&prc_path).expect("resolve_cascade failed");

    // GCI instructions should win (highest tier)
    assert!(
        resolved.instructions.contains("Global instructions"),
        "Expected GCI instructions to override lower tiers; got: {}",
        resolved.instructions
    );

    // Restore environment
    if let Some(home) = old_home {
        std::env::set_var("HOME", home);
    }
    if let Some(apc) = old_apc {
        std::env::set_var("CASCADE_APC_PATH", apc);
    } else {
        std::env::remove_var("CASCADE_APC_PATH");
    }
}

/// Test 2: Rules accumulation with GCI entries appearing first.
#[test]
fn test_rules_accumulation() {
    let root = TempDir::new().unwrap();
    let root_path = root.path();

    let gci_path = root_path;
    let ppc_path = root_path.join("myproject");

    fs::create_dir_all(&ppc_path).unwrap();

    // Create GCI and PPC tiers with rules
    create_tier_fixture(gci_path, "GCI", "Global", &["GCI rule A", "GCI rule B"]);
    create_tier_fixture(&ppc_path, "PPC", "Project", &["PPC rule X", "PPC rule Y"]);

    let old_home = std::env::var("HOME").ok();
    std::env::set_var("HOME", root_path);

    let resolved = resolve_cascade(&ppc_path).expect("resolve_cascade failed");

    // GCI rules should appear first, then PPC rules
    assert!(
        resolved.rules.len() >= 4,
        "Expected at least 4 rules; got: {}",
        resolved.rules.len()
    );

    // Find indices of GCI and PPC rules
    let gci_rule_idx = resolved
        .rules
        .iter()
        .position(|r| r.contains("GCI rule A"))
        .expect("GCI rule A not found");
    let ppc_rule_idx = resolved
        .rules
        .iter()
        .position(|r| r.contains("PPC rule X"))
        .expect("PPC rule X not found");

    assert!(
        gci_rule_idx < ppc_rule_idx,
        "GCI rules should appear before PPC rules; got GCI at {}, PPC at {}",
        gci_rule_idx,
        ppc_rule_idx
    );

    if let Some(home) = old_home {
        std::env::set_var("HOME", home);
    }
}

/// Test 3: Missing tiers are silently skipped without panic.
#[test]
fn test_missing_tiers_skipped() {
    let root = TempDir::new().unwrap();
    let root_path = root.path();

    let gci_path = root_path;
    let ppc_path = root_path.join("myproject");

    fs::create_dir_all(&ppc_path).unwrap();

    // Create only GCI and PPC; skip PCI, APC, PRC
    create_tier_fixture(gci_path, "GCI", "Global only", &["only GCI"]);

    let old_home = std::env::var("HOME").ok();
    std::env::set_var("HOME", root_path);

    // Should not panic, and should resolve successfully with only found tiers
    let resolved = resolve_cascade(&ppc_path).expect("resolve_cascade should not panic");
    assert!(
        !resolved.instructions.is_empty(),
        "Expected instructions to be resolved"
    );

    if let Some(home) = old_home {
        std::env::set_var("HOME", home);
    }
}

/// Test 4: compat_gen writes CLAUDE.md and AGENTS.md files at each tier.
#[test]
fn test_compat_files_written() {
    let root = TempDir::new().unwrap();
    let root_path = root.path();

    let gci_path = root_path;
    let ppc_path = root_path.join("myproject");

    fs::create_dir_all(&ppc_path).unwrap();

    create_tier_fixture(gci_path, "GCI", "Global", &[]);
    create_tier_fixture(&ppc_path, "PPC", "Project", &[]);

    let old_home = std::env::var("HOME").ok();
    std::env::set_var("HOME", root_path);

    let generated = generate_all(&ppc_path).expect("generate_all failed");

    // Should have written CLAUDE.md and AGENTS.md for at least one tier
    assert!(
        generated.len() >= 2,
        "Expected at least 2 generated files (CLAUDE.md + AGENTS.md); got: {}",
        generated.len()
    );

    // Verify files actually exist on disk
    for compat_file in &generated {
        assert!(
            compat_file.path.exists(),
            "Generated file does not exist: {}",
            compat_file.path.display()
        );
    }

    if let Some(home) = old_home {
        std::env::set_var("HOME", home);
    }
}

/// Test 5: compat_gen is idempotent (calling twice succeeds without errors).
#[test]
fn test_compat_idempotent() {
    let root = TempDir::new().unwrap();
    let root_path = root.path();

    let gci_path = root_path;
    let ppc_path = root_path.join("myproject");

    fs::create_dir_all(&ppc_path).unwrap();

    create_tier_fixture(gci_path, "GCI", "Global", &[]);
    create_tier_fixture(&ppc_path, "PPC", "Project", &[]);

    let old_home = std::env::var("HOME").ok();
    std::env::set_var("HOME", root_path);

    // Generate once — should succeed
    let generated1 = generate_all(&ppc_path).expect("first generate_all failed");
    assert!(
        !generated1.is_empty(),
        "Expected at least one file generated"
    );

    // Record the paths of generated CLAUDE.md files
    let claude_paths: Vec<_> = generated1
        .iter()
        .filter(|f| f.path.file_name().is_some_and(|n| n == "CLAUDE.md"))
        .map(|f| f.path.clone())
        .collect();

    // Generate again — should also succeed (idempotent)
    let generated2 = generate_all(&ppc_path).expect("second generate_all failed");
    assert!(
        !generated2.is_empty(),
        "Expected files on second generation"
    );

    // Verify that CLAUDE.md files from first generation still exist and are readable
    for claude_path in &claude_paths {
        assert!(
            claude_path.exists(),
            "CLAUDE.md from first generation no longer exists: {}",
            claude_path.display()
        );
        let content = fs::read_to_string(claude_path)
            .expect("failed to read CLAUDE.md file on idempotency check");
        assert!(
            content.contains("cascade:generated"),
            "CLAUDE.md does not contain generated marker: {}",
            claude_path.display()
        );
    }

    if let Some(home) = old_home {
        std::env::set_var("HOME", home);
    }
}
