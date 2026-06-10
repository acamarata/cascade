//! # cascade_core::templates::upgrade
//!
//! Re-exports and tests for template upgrade + diff operations.
//!
//! ## Purpose
//!
//! This module exists so that `cargo test -- templates::upgrade` filters
//! correctly to the acceptance-criterion test set for T-P3-E05-10.
//!
//! The implementation lives in [`super::apply`].
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-core::templates` (T-P3-E05-10).

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::super::apply::{TemplateEngine, make_stamp};
    use cascade_types::{TemplateManifest, TemplateRecord, TemplateTier};
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

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

    // T-10-A: diff_sections on empty target → all sections added, zero conflicts
    #[test]
    fn diff_sections_empty_target_all_added() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_versioned_record("gci-default", "1.0.0", "## Alpha\nalpha\n## Beta\nbeta\n");
        let target = tmp.path().join("CASCADE.md");
        let dr = engine.diff_sections(&record, &target).unwrap();
        assert_eq!(dr.added, vec!["## Alpha", "## Beta"]);
        assert!(dr.conflicts.is_empty());
        assert!(dr.matching.is_empty());
    }

    // T-10-B: diff_sections partial match
    #[test]
    fn diff_sections_partial_match() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_versioned_record("gci-default", "1.0.0", "## Alpha\nalpha body\n## Beta\nnew beta\n");
        let target = tmp.path().join("CASCADE.md");
        fs::write(&target, "## Alpha\nalpha body\n## Beta\nold beta\n").unwrap();

        let dr = engine.diff_sections(&record, &target).unwrap();
        assert!(dr.added.is_empty());
        assert!(dr.matching.contains(&"## Alpha".to_string()));
        assert!(dr.conflicts.contains(&"## Beta".to_string()));
    }

    // T-10-C: upgrade no-op (same version stamp → all-empty UpgradeResult)
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

    // T-10-D: upgrade changed section → overwritten, listed in upgraded
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
        assert!(result.upgraded.contains(&"## Rules".to_string()), "Rules must be in upgraded");
        assert!(result.added.is_empty());

        let content = fs::read_to_string(&target).unwrap();
        assert!(content.contains("new rules"), "target must have new content");
        assert!(content.contains(r#"version="1.1.0""#), "stamp must be updated");
    }

    // T-10-E: upgrade removed section → deprecation comment added to target
    #[test]
    #[serial(global_env)]
    fn upgrade_removed_section_gets_deprecation_comment() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();

        let old = make_versioned_record(
            "gci-default", "1.0.0",
            "## Rules\nrules\n## OldSection\nold stuff\n",
        );
        let new_r = make_versioned_record(
            "gci-default", "1.1.0",
            "## Rules\nrules\n",
        );
        let target = tmp.path().join("CASCADE.md");
        let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
        fs::write(
            &target,
            format!("## Rules\nrules\n## OldSection\nold stuff\n\n{}\n", stamp),
        ).unwrap();

        let result = engine
            .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
            .unwrap();
        assert!(result.deprecated.contains(&"## OldSection".to_string()));

        let content = fs::read_to_string(&target).unwrap();
        assert!(content.contains("cascade:deprecated"), "deprecation comment must appear");
        assert!(content.contains("old stuff"), "old content must be preserved");
    }

    // T-10-F: upgrade dry-run does not modify file
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
