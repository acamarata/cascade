//! # cascade_core::templates::version
//!
//! Re-exports and tests for template version comparison and upgrade-path detection.
//!
//! ## Purpose
//!
//! This module exists so that `cargo test -- templates::version` filters
//! correctly to the acceptance-criterion test set for T-P3-E05-13.
//!
//! The implementation lives in
//! [`super::registry::TemplateRegistry::available_upgrade`].
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-core::templates` (T-P3-E05-13).

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::super::registry::TemplateRegistry;
    use tempfile::TempDir;

    fn write_template_v(dir: &TempDir, name: &str, id: &str, ver: &str) {
        let path = dir.path().join(name);
        let content = format!(
            "---\nid = \"{}\"\nversion = \"{}\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n# Body\n",
            id, ver
        );
        std::fs::write(path, content).unwrap();
    }

    // T-13-A: no upgrade available when registry version == current
    #[test]
    fn available_upgrade_none_when_same_version() {
        let dir = TempDir::new().unwrap();
        write_template_v(&dir, "t.md", "my-tmpl", "1.0.0");
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        assert!(reg.available_upgrade("my-tmpl", "1.0.0").is_none());
    }

    // T-13-B: upgrade returned when registry version > current
    #[test]
    fn available_upgrade_some_when_newer_version() {
        let dir = TempDir::new().unwrap();
        write_template_v(&dir, "t.md", "my-tmpl", "1.1.0");
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let upgrade = reg.available_upgrade("my-tmpl", "1.0.0");
        assert!(upgrade.is_some());
        assert_eq!(upgrade.unwrap().manifest.version, "1.1.0");
    }

    // T-13-C: None when registry version < current
    #[test]
    fn available_upgrade_none_when_registry_older() {
        let dir = TempDir::new().unwrap();
        write_template_v(&dir, "t.md", "my-tmpl", "1.0.0");
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        assert!(reg.available_upgrade("my-tmpl", "1.2.0").is_none());
    }

    // T-13-D: pre-release handling (1.0.0-alpha < 1.0.0 by semver)
    #[test]
    fn available_upgrade_pre_release_less_than_release() {
        let dir = TempDir::new().unwrap();
        write_template_v(&dir, "t.md", "my-tmpl", "1.0.0");
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let upgrade = reg.available_upgrade("my-tmpl", "1.0.0-alpha");
        assert!(upgrade.is_some(), "1.0.0 > 1.0.0-alpha — upgrade should be available");
    }

    // T-13-E: unknown template → None (no panic)
    #[test]
    fn available_upgrade_unknown_id_returns_none() {
        let dir = TempDir::new().unwrap();
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        assert!(reg.available_upgrade("does-not-exist", "1.0.0").is_none());
    }
}
