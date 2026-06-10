//! # cascade_core::templates::inheritance
//!
//! Re-exports and tests for template inheritance (`extends` resolution).
//!
//! ## Purpose
//!
//! This module exists so that `cargo test -- templates::inheritance` filters
//! correctly to the acceptance-criterion test set for T-P3-E05-12.
//!
//! The implementation lives in [`super::registry::TemplateRegistry::resolve`].
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-core::templates` (T-P3-E05-12).

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::super::registry::TemplateRegistry;
    use tempfile::TempDir;

    fn write_template(dir: &TempDir, name: &str, id: &str, tier: &str) {
        let path = dir.path().join(name);
        let content = format!(
            "---\nid = \"{}\"\nversion = \"1.0.0\"\ntier = \"{}\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n# Body\n",
            id, tier
        );
        std::fs::write(path, content).unwrap();
    }

    fn write_template_with_extends(
        dir: &TempDir,
        name: &str,
        id: &str,
        tier: &str,
        extends: Option<&str>,
        body: &str,
    ) {
        let path = dir.path().join(name);
        let extends_line = match extends {
            Some(e) => format!("extends = \"{}\"\n", e),
            None => String::new(),
        };
        let content = format!(
            "---\nid = \"{}\"\nversion = \"1.0.0\"\ntier = \"{}\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n{}---\n{}",
            id, tier, extends_line, body
        );
        std::fs::write(path, content).unwrap();
    }

    // T-12-A: no extends → resolve returns a clone of the record
    #[test]
    fn resolve_no_extends_returns_self() {
        let dir = TempDir::new().unwrap();
        write_template(&dir, "gci.md", "gci-default", "gci");

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let resolved = reg.resolve("gci-default").unwrap();
        assert_eq!(resolved.manifest.id, "gci-default");
        assert!(resolved.body.contains("# Body"));
    }

    // T-12-B: single extends — child merges parent + child sections (child wins)
    #[test]
    fn resolve_single_extends_merges_parent_and_child() {
        let dir = TempDir::new().unwrap();

        write_template_with_extends(
            &dir, "parent.md", "gci-default", "gci", None,
            "## ParentSection\nparent content\n## Shared\nparent shared\n",
        );
        write_template_with_extends(
            &dir, "child.md", "child-tmpl", "gci", Some("gci-default"),
            "## Shared\nchild override\n## ChildOnly\nchild only\n",
        );

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let resolved = reg.resolve("child-tmpl").unwrap();

        assert!(resolved.body.contains("## ParentSection"), "parent section must be present");
        assert!(resolved.body.contains("parent content"));
        assert!(resolved.body.contains("child override"), "child must override shared");
        assert!(!resolved.body.contains("parent shared"), "parent shared must not appear");
        assert!(resolved.body.contains("## ChildOnly"), "child-only section must appear");
        assert_eq!(resolved.manifest.id, "child-tmpl");
    }

    // T-12-C: chain (grandparent → parent → child) resolves all three correctly
    #[test]
    fn resolve_three_level_chain() {
        let dir = TempDir::new().unwrap();

        write_template_with_extends(
            &dir, "gp.md", "grandparent", "gci", None,
            "## GpSection\ngp body\n",
        );
        write_template_with_extends(
            &dir, "parent.md", "parent", "gci", Some("grandparent"),
            "## ParentSection\nparent body\n",
        );
        write_template_with_extends(
            &dir, "child.md", "child", "gci", Some("parent"),
            "## ChildSection\nchild body\n",
        );

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let resolved = reg.resolve("child").unwrap();

        assert!(resolved.body.contains("## GpSection"), "grandparent section must resolve");
        assert!(resolved.body.contains("## ParentSection"), "parent section must resolve");
        assert!(resolved.body.contains("## ChildSection"), "child section must resolve");
    }

    // T-12-D: cycle detection — A extends B extends A → Err, no panic
    #[test]
    fn resolve_cycle_returns_error_not_panic() {
        let dir = TempDir::new().unwrap();

        write_template_with_extends(
            &dir, "a.md", "tmpl-a", "gci", Some("tmpl-b"),
            "## A\na body\n",
        );
        write_template_with_extends(
            &dir, "b.md", "tmpl-b", "gci", Some("tmpl-a"),
            "## B\nb body\n",
        );

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let result = reg.resolve("tmpl-a");
        assert!(result.is_err(), "cycle must return an error");
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("cycle") || msg.contains("depth") || msg.contains("tmpl"),
            "error message must describe the cycle: {}", msg
        );
    }
}
