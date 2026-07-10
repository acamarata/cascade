//! Tests for the template apply engine.

use super::engine::TemplateEngine;
use super::options::ApplyOptions;
use super::stamp::{extract_stamps, make_stamp};
use cascade_types::{TemplateManifest, TemplateRecord, TemplateTier};
use serial_test::serial;
use std::fs;
use tempfile::TempDir;

// ── Fixtures ──────────────────────────────────────────────────────────────

fn make_record(body: &str) -> TemplateRecord {
    TemplateRecord {
        manifest: TemplateManifest {
            id: "gci-default".to_string(),
            version: "1.0.0".to_string(),
            tier: TemplateTier::Gci,
            stacks: vec![],
            project_shapes: vec![],
            description: "Test template".to_string(),
            extends: None,
            min_cascade_version: None,
        },
        body: body.to_string(),
    }
}

fn opts_with_root(tmp: &TempDir) -> ApplyOptions {
    ApplyOptions {
        root: Some(tmp.path().to_path_buf()),
        ..Default::default()
    }
}

fn dry_run_opts_with_root(tmp: &TempDir) -> ApplyOptions {
    ApplyOptions {
        dry_run: true,
        root: Some(tmp.path().to_path_buf()),
        ..Default::default()
    }
}

fn force_opts_with_root(tmp: &TempDir) -> ApplyOptions {
    ApplyOptions {
        force: true,
        root: Some(tmp.path().to_path_buf()),
        ..Default::default()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn template_body_preserved_with_placeholders() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Overview\n{{project_name}} rocks.\n");
    let target = tmp.path().join("CASCADE.md");
    let opts = opts_with_root(&tmp);

    let result = engine.apply(&record, &target, &opts).unwrap();
    assert!(result.applied.contains(&"## Overview".to_string()));

    let written = fs::read_to_string(&target).unwrap();
    assert!(written.contains("{{project_name}} rocks."));
}

#[test]
#[serial(global_env)]
fn apply_to_empty_target_appends_all_sections_and_stamp() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Alpha\nalpha body\n## Beta\nbeta body\n");
    let target = tmp.path().join("CASCADE.md");
    let opts = opts_with_root(&tmp);

    let result = engine.apply(&record, &target, &opts).unwrap();

    assert_eq!(result.applied, vec!["## Alpha", "## Beta"]);
    assert!(result.conflicts.is_empty());
    assert!(result.skipped.is_empty());

    let written = fs::read_to_string(&target).unwrap();
    assert!(written.contains("## Alpha"));
    assert!(written.contains("## Beta"));
    assert!(written.contains("alpha body"));
    assert!(written.contains("beta body"));
    assert!(written.contains("cascade:applied"));
    assert!(written.contains(r#"id="gci-default""#));
    assert!(written.contains(r#"version="1.0.0""#));
}

#[test]
#[serial(global_env)]
fn apply_twice_is_idempotent() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Overview\nContent.\n");
    let target = tmp.path().join("CASCADE.md");
    let opts = opts_with_root(&tmp);

    let r1 = engine.apply(&record, &target, &opts).unwrap();
    assert!(!r1.applied.is_empty());

    let r2 = engine.apply(&record, &target, &opts).unwrap();
    assert!(
        r2.applied.is_empty(),
        "second apply should be no-op: {:?}",
        r2.applied
    );
    assert!(r2.conflicts.is_empty());
    assert!(r2.skipped.is_empty());
}

#[test]
#[serial(global_env)]
fn conflict_recorded_when_section_exists_with_different_content() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Rules\nNew rules content.\n");
    let target = tmp.path().join("CASCADE.md");

    fs::write(&target, "## Rules\nOld rules content.\n").unwrap();

    let opts = opts_with_root(&tmp);
    let result = engine.apply(&record, &target, &opts).unwrap();

    assert!(result.conflicts.contains(&"## Rules".to_string()));
    assert!(result.applied.is_empty());

    let after = fs::read_to_string(&target).unwrap();
    assert!(after.contains("Old rules content."));
    assert!(!after.contains("New rules content."));
}

#[test]
#[serial(global_env)]
fn force_mode_overwrites_conflicting_section() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Rules\nNew rules content.\n");
    let target = tmp.path().join("CASCADE.md");

    fs::write(&target, "## Rules\nOld rules content.\n").unwrap();

    let opts = force_opts_with_root(&tmp);
    let result = engine.apply(&record, &target, &opts).unwrap();

    assert!(
        result.applied.contains(&"## Rules".to_string()),
        "force should mark as applied"
    );
    assert!(result.conflicts.is_empty());

    let after = fs::read_to_string(&target).unwrap();
    assert!(
        after.contains("New rules content."),
        "forced content should appear"
    );
}

#[test]
#[serial(global_env)]
fn dry_run_does_not_write_file() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Overview\nNew content.\n");
    let target = tmp.path().join("CASCADE.md");

    fs::write(&target, "## Old\nOriginal.\n").unwrap();

    let opts = dry_run_opts_with_root(&tmp);
    let result = engine.apply(&record, &target, &opts).unwrap();

    assert!(result.applied.contains(&"## Overview".to_string()));

    let after = fs::read_to_string(&target).unwrap();
    assert!(
        !after.contains("New content."),
        "dry-run must not write file"
    );
    assert!(
        after.contains("Original."),
        "original content preserved in dry-run"
    );
}

#[test]
#[serial(global_env)]
fn dry_run_returns_correct_plan() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## New Section\nbody\n## Existing Section\ndifferent\n");
    let target = tmp.path().join("CASCADE.md");

    fs::write(&target, "## Existing Section\noriginal\n").unwrap();

    let opts = dry_run_opts_with_root(&tmp);
    let result = engine.apply(&record, &target, &opts).unwrap();

    assert!(result.applied.contains(&"## New Section".to_string()));
    assert!(result
        .conflicts
        .contains(&"## Existing Section".to_string()));
}

#[test]
#[serial(global_env)]
fn provenance_stamp_written_with_id_and_version() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Section A\nbody\n");
    let target = tmp.path().join("CASCADE.md");
    let opts = opts_with_root(&tmp);

    engine.apply(&record, &target, &opts).unwrap();

    let written = fs::read_to_string(&target).unwrap();
    let stamps = extract_stamps(&written);
    assert_eq!(stamps.len(), 1);
    assert_eq!(stamps[0].0, "gci-default");
    assert_eq!(stamps[0].1, "1.0.0");
}

#[test]
#[serial(global_env)]
fn traversal_outside_root_is_rejected() {
    use std::path::PathBuf;
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Section\nbody\n");

    let outside = PathBuf::from("/tmp/cascade-traversal-test.md");
    let opts = ApplyOptions {
        root: Some(tmp.path().to_path_buf()),
        ..Default::default()
    };

    let result = engine.apply(&record, &outside, &opts);
    assert!(result.is_err(), "traversal should be rejected");
    let err = result.unwrap_err().to_string();
    assert!(
        err.contains("confinement"),
        "error should mention confinement, got: {}",
        err
    );
}

#[test]
#[serial(global_env)]
fn existing_sections_not_in_template_are_preserved() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## New Section\nnew content\n");
    let target = tmp.path().join("CASCADE.md");

    fs::write(&target, "## Existing\nkeep this\n").unwrap();

    let opts = opts_with_root(&tmp);
    engine.apply(&record, &target, &opts).unwrap();

    let after = fs::read_to_string(&target).unwrap();
    assert!(
        after.contains("keep this"),
        "existing section must be preserved"
    );
    assert!(
        after.contains("new content"),
        "new section must be appended"
    );
}

#[test]
#[serial(global_env)]
fn diff_alias_matches_dry_run_apply() {
    let tmp = TempDir::new().unwrap();
    let engine = TemplateEngine::new();
    let record = make_record("## Section\nbody\n");
    let target = tmp.path().join("CASCADE.md");

    let opts = dry_run_opts_with_root(&tmp);
    let diff_result = engine.apply(&record, &target, &opts).unwrap();
    assert!(diff_result.applied.contains(&"## Section".to_string()));
    assert!(!target.exists(), "diff must not create the file");
}

// ── Upgrade tests ─────────────────────────────────────────────────────────

mod upgrade {
    use super::*;

    fn make_versioned_record(id: &str, version: &str, body: &str) -> TemplateRecord {
        TemplateRecord {
            manifest: TemplateManifest {
                id: id.to_string(),
                version: version.to_string(),
                tier: TemplateTier::Gci,
                stacks: vec![],
                project_shapes: vec![],
                description: "Test".to_string(),
                extends: None,
                min_cascade_version: None,
            },
            body: body.to_string(),
        }
    }

    #[test]
    fn diff_sections_empty_target_all_added() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Alpha\nalpha\n## Beta\nbeta\n");
        let target = tmp.path().join("CASCADE.md");
        let dr = engine.diff_sections(&record, &target).unwrap();
        assert_eq!(dr.added, vec!["## Alpha", "## Beta"]);
        assert!(dr.conflicts.is_empty());
        assert!(dr.matching.is_empty());
    }

    #[test]
    fn diff_sections_partial_match() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Alpha\nalpha body\n## Beta\nnew beta\n");
        let target = tmp.path().join("CASCADE.md");
        fs::write(&target, "## Alpha\nalpha body\n## Beta\nold beta\n").unwrap();

        let dr = engine.diff_sections(&record, &target).unwrap();
        assert!(dr.added.is_empty());
        assert!(dr.matching.contains(&"## Alpha".to_string()));
        assert!(dr.conflicts.contains(&"## Beta".to_string()));
    }

    #[test]
    #[serial(global_env)]
    fn upgrade_noop_same_version() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let old = make_versioned_record("gci-default", "1.0.0", "## Intro\noriginal\n");
        let new_r = make_versioned_record("gci-default", "1.0.0", "## Intro\noriginal\n");
        let target = tmp.path().join("CASCADE.md");

        let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
        fs::write(&target, format!("## Intro\noriginal\n\n{}\n", stamp)).unwrap();

        let result = engine
            .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
            .unwrap();
        assert!(result.upgraded.is_empty());
        assert!(result.added.is_empty());
        assert!(result.deprecated.is_empty());
    }

    #[test]
    #[serial(global_env)]
    fn upgrade_changed_section_overwritten() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();

        let old = make_versioned_record("gci-default", "1.0.0", "## Rules\nold rules\n");
        let new_r = make_versioned_record("gci-default", "1.1.0", "## Rules\nnew rules\n");
        let target = tmp.path().join("CASCADE.md");

        let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
        fs::write(&target, format!("## Rules\nold rules\n\n{}\n", stamp)).unwrap();

        let result = engine
            .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
            .unwrap();
        assert!(
            result.upgraded.contains(&"## Rules".to_string()),
            "Rules must be in upgraded"
        );
        assert!(result.added.is_empty());

        let content = fs::read_to_string(&target).unwrap();
        assert!(
            content.contains("new rules"),
            "target must have new content"
        );
        assert!(
            content.contains(r#"version="1.1.0""#),
            "stamp must be updated"
        );
    }

    #[test]
    #[serial(global_env)]
    fn upgrade_removed_section_gets_deprecation_comment() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();

        let old = make_versioned_record(
            "gci-default",
            "1.0.0",
            "## Rules\nrules\n## OldSection\nold stuff\n",
        );
        let new_r = make_versioned_record("gci-default", "1.1.0", "## Rules\nrules\n");
        let target = tmp.path().join("CASCADE.md");
        let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
        fs::write(
            &target,
            format!("## Rules\nrules\n## OldSection\nold stuff\n\n{}\n", stamp),
        )
        .unwrap();

        let result = engine
            .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
            .unwrap();
        assert!(result.deprecated.contains(&"## OldSection".to_string()));

        let content = fs::read_to_string(&target).unwrap();
        assert!(
            content.contains("cascade:deprecated"),
            "deprecation comment must appear"
        );
        assert!(
            content.contains("old stuff"),
            "old content must be preserved"
        );
    }

    #[test]
    #[serial(global_env)]
    fn upgrade_dry_run_does_not_write() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();

        let old = make_versioned_record("gci-default", "1.0.0", "## Alpha\nalpha\n");
        let new_r = make_versioned_record("gci-default", "1.1.0", "## Alpha\nnew alpha\n");
        let target = tmp.path().join("CASCADE.md");
        let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
        let original = format!("## Alpha\nalpha\n\n{}\n", stamp);
        fs::write(&target, &original).unwrap();

        engine
            .upgrade(&old, &new_r, &target, true, Some(tmp.path().to_path_buf()))
            .unwrap();

        let after = fs::read_to_string(&target).unwrap();
        assert_eq!(after, original, "dry-run must not modify the file");
    }
}
