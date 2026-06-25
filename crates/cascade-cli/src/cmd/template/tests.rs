//! Tests for `cascade template` subcommands.

#[cfg(test)]
mod tests {
    use crate::cmd::template::{
        helpers::{
            load_registry, parse_tier, parse_vars, read_stamps_from_content, substitute_vars,
        },
        list::{truncate, ListArgs},
        manage::{is_valid_semver, validate_template_file, CreateArgs, ExportArgs},
        ApplyArgs, DiffArgs, UpgradeArgs,
    };
    use crate::cmd::Command;
    use cascade_core::templates::registry::TemplateFilter;
    use cascade_types::{TemplateManifest, TemplateTier, TemplateRecord};
    use serial_test::serial;
    use std::fs;
    use std::io::Write;
    use std::path::Path;
    use tempfile::TempDir;

    // ── Fixtures ──────────────────────────────────────────────────────────────

    fn write_template_file(dir: &TempDir, name: &str, id: &str, tier: &str, body: &str) {
        let path = dir.path().join(name);
        let mut f = fs::File::create(&path).unwrap();
        writeln!(
            f,
            "---\nid = \"{id}\"\nversion = \"1.0.0\"\ntier = \"{tier}\"\nstacks = []\nproject_shapes = []\ndescription = \"Test {id}\"\n---\n{body}"
        )
        .unwrap();
    }

    fn make_record_with_body(id: &str, body: &str) -> TemplateRecord {
        TemplateRecord {
            manifest: TemplateManifest {
                id: id.to_string(),
                version: "1.0.0".to_string(),
                tier: TemplateTier::Gci,
                stacks: vec![],
                project_shapes: vec![],
                description: format!("Test {id}"),
                extends: None,
                min_cascade_version: None,
            },
            body: body.to_string(),
        }
    }

    // ── Arg parsing ───────────────────────────────────────────────────────────

    #[test]
    fn parse_list_args_defaults() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ListArgs,
        }

        let cli = Cli::try_parse_from(["list"]).unwrap();
        assert!(cli.args.tier.is_none());
        assert!(cli.args.stack.is_none());
        assert!(cli.args.shape.is_none());
        assert!(!cli.args.json);
    }

    #[test]
    fn parse_list_args_all_flags() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ListArgs,
        }

        let cli = Cli::try_parse_from([
            "list", "--tier", "gci", "--stack", "rust", "--shape", "lib", "--json",
        ])
        .unwrap();
        assert_eq!(cli.args.tier.as_deref(), Some("gci"));
        assert_eq!(cli.args.stack.as_deref(), Some("rust"));
        assert_eq!(cli.args.shape.as_deref(), Some("lib"));
        assert!(cli.args.json);
    }

    #[test]
    fn parse_apply_args() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ApplyArgs,
        }

        let cli = Cli::try_parse_from([
            "apply",
            "--id",
            "gci-default",
            "--target",
            "/tmp/test.md",
            "--var",
            "project=myproject",
            "--var",
            "author=Alice",
            "--dry-run",
            "--force",
        ])
        .unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert_eq!(cli.args.target.as_deref(), Some(Path::new("/tmp/test.md")));
        assert_eq!(cli.args.vars, vec!["project=myproject", "author=Alice"]);
        assert!(cli.args.dry_run);
        assert!(cli.args.force);
    }

    #[test]
    fn parse_diff_args() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: DiffArgs,
        }

        let cli = Cli::try_parse_from(["diff", "--id", "gci-default"]).unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert!(cli.args.target.is_none());
    }

    #[test]
    fn parse_upgrade_args() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: UpgradeArgs,
        }

        let cli = Cli::try_parse_from(["upgrade", "--id", "gci-default", "--dry-run"]).unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert!(cli.args.dry_run);
        assert!(!cli.args.force, "--force should default to false");
    }

    #[test]
    fn parse_upgrade_args_with_force() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: UpgradeArgs,
        }

        let cli = Cli::try_parse_from(["upgrade", "--id", "gci-default", "--force"]).unwrap();
        assert_eq!(cli.args.id, "gci-default");
        assert!(cli.args.force);
        assert!(!cli.args.dry_run);
    }

    #[test]
    fn parse_list_args_upgradeable_flag() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: ListArgs,
        }

        let cli = Cli::try_parse_from(["list", "--upgradeable"]).unwrap();
        assert!(cli.args.upgradeable);
        assert!(cli.args.target.is_none());

        let cli2 =
            Cli::try_parse_from(["list", "--upgradeable", "--target", "/tmp/target.md"]).unwrap();
        assert!(cli2.args.upgradeable);
        assert_eq!(
            cli2.args.target.as_deref(),
            Some(Path::new("/tmp/target.md"))
        );
    }

    // ── list: fixture registry ────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn list_against_fixture_registry() {
        let tmp = TempDir::new().unwrap();
        write_template_file(&tmp, "gci.md", "gci-default", "gci", "## Overview\nBody.\n");
        write_template_file(
            &tmp,
            "prc-rust.md",
            "prc-rust",
            "prc",
            "## Rust Rules\nBody.\n",
        );
        write_template_file(&tmp, "any-base.md", "any-base", "any", "## Base\nBody.\n");
        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", tmp.path()) };
        unsafe { std::env::remove_var("HOME") };

        let reg = load_registry().unwrap();
        assert_eq!(reg.len(), 3);

        let all = reg.list(&TemplateFilter::default());
        assert_eq!(all.len(), 3);

        let filter = TemplateFilter {
            tier: Some(TemplateTier::Gci),
            ..Default::default()
        };
        let gci_matches = reg.list(&filter);
        assert_eq!(gci_matches.len(), 2);
    }

    // ── list: --upgradeable filter ────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn list_upgradeable_returns_only_templates_with_newer_registry_version() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("gci.md");
        fs::write(&path, "---\nid = \"gci-default\"\nversion = \"1.1.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Overview\nNew body.\n").unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", tmp.path()) };
        unsafe { std::env::remove_var("HOME") };

        let target_dir = TempDir::new().unwrap();
        let target = target_dir.path().join("CASCADE.md");
        let stamp = r#"<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->"#.to_string();
        fs::write(&target, format!("## Overview\nOld body.\n\n{}\n", stamp)).unwrap();

        let reg = load_registry().unwrap();
        let filter = TemplateFilter::default();
        let all = reg.list(&filter);
        assert_eq!(all.len(), 1, "registry should have 1 template");

        let content = fs::read_to_string(&target).unwrap();
        let stamps: std::collections::HashMap<String, String> =
            read_stamps_from_content(&content).into_iter().collect();

        let mut records = all;
        records.retain(|r| {
            if let Some(cv) = stamps.get(&r.manifest.id) {
                reg.available_upgrade(&r.manifest.id, cv).is_some()
            } else {
                false
            }
        });

        assert_eq!(records.len(), 1, "gci-default should be upgradeable");
        assert_eq!(records[0].manifest.version, "1.1.0");
    }

    #[test]
    #[serial(global_env)]
    fn list_upgradeable_returns_empty_when_already_up_to_date() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("gci.md");
        fs::write(&path, "---\nid = \"gci-default\"\nversion = \"1.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Overview\nBody.\n").unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", tmp.path()) };
        unsafe { std::env::remove_var("HOME") };

        let target_dir = TempDir::new().unwrap();
        let target = target_dir.path().join("CASCADE.md");
        let stamp = r#"<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->"#;
        fs::write(&target, format!("## Overview\nBody.\n\n{}\n", stamp)).unwrap();

        let reg = load_registry().unwrap();
        let content = fs::read_to_string(&target).unwrap();
        let stamps: std::collections::HashMap<String, String> =
            read_stamps_from_content(&content).into_iter().collect();

        let mut records = reg.list(&TemplateFilter::default());
        records.retain(|r| {
            if let Some(cv) = stamps.get(&r.manifest.id) {
                reg.available_upgrade(&r.manifest.id, cv).is_some()
            } else {
                false
            }
        });

        assert!(
            records.is_empty(),
            "no template should be upgradeable when already up to date"
        );
    }

    // ── upgrade: three-way engine wired via upgrade_by_id ────────────────────

    #[test]
    #[serial(global_env)]
    fn upgrade_wires_to_upgrade_by_id_stamp_updated() {
        use cascade_core::templates::apply::TemplateEngine;

        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };

        let engine = TemplateEngine::new();

        let stamp = r#"<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->"#;
        let target = tmp.path().join("CASCADE.md");
        fs::write(&target, format!("## Rules\nuser content\n\n{}\n", stamp)).unwrap();

        let reg_dir = TempDir::new().unwrap();
        fs::write(
            reg_dir.path().join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.1.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Rules\nnew content\n",
        ).unwrap();
        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", reg_dir.path()) };
        let reg = load_registry().unwrap();
        let new_record = reg.get("gci-default").unwrap().clone();

        let result = engine
            .upgrade_by_id(
                &reg,
                "gci-default",
                &new_record,
                &target,
                false,
                Some(tmp.path().to_path_buf()),
            )
            .unwrap();

        let content = fs::read_to_string(&target).unwrap();
        assert!(
            content.contains(r#"version="1.1.0""#),
            "stamp must be updated to 1.1.0: {content}"
        );
        assert!(
            content.contains("user content"),
            "user content should be preserved"
        );
        let _ = result;
    }

    #[test]
    #[serial(global_env)]
    fn upgrade_force_overwrites_all_conflicting_sections() {
        use cascade_core::templates::apply::{ApplyOptions, TemplateEngine};

        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };

        let engine = TemplateEngine::new();

        let target = tmp.path().join("CASCADE.md");
        fs::write(&target, "## Rules\nuser content\n").unwrap();

        let reg_dir = TempDir::new().unwrap();
        fs::write(
            reg_dir.path().join("gci.md"),
            "---\nid = \"gci-default\"\nversion = \"1.1.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"GCI default\"\n---\n## Rules\nnew content\n",
        ).unwrap();
        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", reg_dir.path()) };
        let reg = load_registry().unwrap();
        let new_record = reg.get("gci-default").unwrap().clone();

        let opts = ApplyOptions {
            dry_run: false,
            force: true,
            root: Some(tmp.path().to_path_buf()),
        };
        let result = engine.apply(&new_record, &target, &opts).unwrap();

        assert!(
            result.applied.contains(&"## Rules".to_string()),
            "force should overwrite conflicting section: {:?}",
            result
        );
        let content = fs::read_to_string(&target).unwrap();
        assert!(
            content.contains("new content"),
            "force should write new template content"
        );
        assert!(
            !content.contains("user content"),
            "old user content should be replaced"
        );
    }

    #[test]
    #[serial(global_env)]
    fn read_stamps_from_content_extracts_id_and_version() {
        let content = r#"## Rules
some content

<!-- cascade:applied { id="gci-default", version="1.0.0", applied_at="2026-01-01T00:00:00Z" } -->
<!-- cascade:applied { id="prc-rust", version="2.3.1", applied_at="2026-02-01T00:00:00Z" } -->
"#;
        let stamps = read_stamps_from_content(content);
        assert_eq!(stamps.len(), 2);
        let map: std::collections::HashMap<_, _> = stamps.into_iter().collect();
        assert_eq!(map["gci-default"], "1.0.0");
        assert_eq!(map["prc-rust"], "2.3.1");
    }

    #[test]
    fn read_stamps_from_content_empty_file() {
        let stamps = read_stamps_from_content("");
        assert!(stamps.is_empty());
    }

    // ── apply: dry-run returns plan without writing ────────────────────────────

    #[test]
    #[serial(global_env)]
    fn apply_dry_run_does_not_write() {
        use cascade_core::templates::apply::{ApplyOptions, TemplateEngine};

        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };

        let record = make_record_with_body("gci-default", "## Overview\nNew content.\n");
        let target = tmp.path().join("CASCADE.md");

        let engine = TemplateEngine::new();
        let opts = ApplyOptions {
            dry_run: true,
            root: Some(tmp.path().to_path_buf()),
            ..Default::default()
        };
        let result = engine.apply(&record, &target, &opts).unwrap();

        assert!(result.applied.contains(&"## Overview".to_string()));
        assert!(!target.exists(), "dry-run must not create the file");
    }

    // ── apply: variable substitution ─────────────────────────────────────────

    #[test]
    fn substitute_vars_basic() {
        let mut vars = std::collections::HashMap::new();
        vars.insert("project".to_string(), "cascade".to_string());
        vars.insert("author".to_string(), "Aric".to_string());

        let body = "# {{project}}\nMaintained by {{author}}.\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, "# cascade\nMaintained by Aric.\n");
    }

    #[test]
    fn substitute_vars_with_default() {
        let vars = std::collections::HashMap::new();
        let body = "License: {{license:MIT}}\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, "License: MIT\n");
    }

    #[test]
    fn substitute_vars_with_default_override() {
        let mut vars = std::collections::HashMap::new();
        vars.insert("license".to_string(), "Apache-2.0".to_string());
        let body = "License: {{license:MIT}}\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, "License: Apache-2.0\n");
    }

    #[test]
    fn substitute_vars_missing_var_returns_error() {
        let vars = std::collections::HashMap::new();
        let body = "# {{project_name}}\n";
        let result = substitute_vars(body, &vars);
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("project_name"),
            "error should name the missing var: {msg}"
        );
        assert!(
            msg.contains("--var"),
            "error should hint at --var flag: {msg}"
        );
    }

    #[test]
    fn substitute_vars_no_placeholders() {
        let vars = std::collections::HashMap::new();
        let body = "# No placeholders here.\n";
        let result = substitute_vars(body, &vars).unwrap();
        assert_eq!(result, body);
    }

    // ── apply: missing-var error ──────────────────────────────────────────────

    #[test]
    fn parse_vars_invalid_format() {
        let bad = vec!["no-equals-sign".to_string()];
        let result = parse_vars(&bad);
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("key=value"),
            "error should mention format: {msg}"
        );
    }

    #[test]
    fn parse_vars_valid() {
        let entries = vec!["key=val".to_string(), "foo=bar=baz".to_string()];
        let map = parse_vars(&entries).unwrap();
        assert_eq!(map["key"], "val");
        assert_eq!(map["foo"], "bar=baz");
    }

    // ── parse_tier ────────────────────────────────────────────────────────────

    #[test]
    fn parse_tier_valid_values() {
        assert_eq!(parse_tier("gci").unwrap(), TemplateTier::Gci);
        assert_eq!(parse_tier("prc").unwrap(), TemplateTier::Prc);
        assert_eq!(parse_tier("any").unwrap(), TemplateTier::Any);
        assert_eq!(parse_tier("GCI").unwrap(), TemplateTier::Gci);
    }

    #[test]
    fn parse_tier_invalid_value() {
        let result = parse_tier("bogus");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("bogus"),
            "error should mention the invalid value: {msg}"
        );
    }

    // ── truncate helper ───────────────────────────────────────────────────────

    #[test]
    fn truncate_short_string_unchanged() {
        assert_eq!(truncate("hello", 10), "hello");
    }

    #[test]
    fn truncate_long_string_shortened() {
        let long = "a".repeat(50);
        let result = truncate(&long, 10);
        assert!(
            result.len() <= 12,
            "truncated result should be short: {result}"
        );
        assert!(result.ends_with('…') || result.len() <= 10);
    }

    // ── create: scaffolds valid template ─────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn create_writes_scaffold_with_output_override() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("my-tmpl.md");

        let args = CreateArgs {
            id: "my-tmpl".to_string(),
            tier: "prc".to_string(),
            extends: None,
            output: Some(out.clone()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("create should succeed");

        assert!(out.exists(), "scaffold file should be created");
        let content = fs::read_to_string(&out).unwrap();
        assert!(content.contains("id = \"my-tmpl\""), "id: {content}");
        assert!(content.contains("tier = \"prc\""), "tier: {content}");
        assert!(
            content.contains("version = \"1.0.0\""),
            "version: {content}"
        );
        assert!(content.contains("## Purpose"), "Purpose: {content}");
        assert!(
            content.contains("## Key Conventions"),
            "Key Conventions: {content}"
        );
        assert!(content.contains("## File Layout"), "File Layout: {content}");
    }

    #[test]
    #[serial(global_env)]
    fn create_refuses_to_overwrite_existing_file() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("existing.md");
        fs::write(&out, "already here").unwrap();

        let args = CreateArgs {
            id: "existing".to_string(),
            tier: "any".to_string(),
            extends: None,
            output: Some(out.clone()),
        };
        let result = tokio::runtime::Runtime::new().unwrap().block_on(args.run());
        assert!(result.is_err(), "should error on existing file");
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("already exists"), "error: {msg}");
    }

    #[test]
    #[serial(global_env)]
    fn create_includes_extends_when_set() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("child.md");

        let args = CreateArgs {
            id: "child".to_string(),
            tier: "prc".to_string(),
            extends: Some("gci-default".to_string()),
            output: Some(out.clone()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("create with extends should succeed");

        let content = fs::read_to_string(&out).unwrap();
        assert!(
            content.contains("extends = \"gci-default\""),
            "extends: {content}"
        );
    }

    // ── validate: passes on well-formed scaffold ──────────────────────────────

    #[test]
    #[serial(global_env)]
    fn validate_passes_on_well_formed_scaffold() {
        let tmp = TempDir::new().unwrap();
        let out = tmp.path().join("well-formed.md");

        let args = CreateArgs {
            id: "well-formed".to_string(),
            tier: "prc".to_string(),
            extends: None,
            output: Some(out.clone()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("create should succeed");

        let content = fs::read_to_string(&out).unwrap();
        let fixed = content.replace("description = \"\"", "description = \"My test template\"");
        fs::write(&out, fixed).unwrap();

        let errors = validate_template_file(&out).expect("validate should not I/O error");
        assert!(errors.is_empty(), "scaffold should be valid: {errors:?}");
    }

    #[test]
    fn validate_errors_on_empty_description() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("test-desc.md");
        fs::write(&path,
            "---\nid = \"test-desc\"\nversion = \"1.0.0\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"\"\n---\n             ## Purpose\n\nText.\n\n## Key Conventions\n\nMore text.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(!errors.is_empty(), "should have errors");
        assert!(
            errors.iter().any(|e| e.contains("description")),
            "desc: {errors:?}"
        );
    }

    #[test]
    fn validate_errors_on_id_mismatch() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("filename-slug.md");
        fs::write(&path,
            "---\nid = \"wrong-id\"\nversion = \"1.0.0\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"desc\"\n---\n             ## Section One\n\nText.\n\n## Section Two\n\nMore.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(
            errors
                .iter()
                .any(|e| e.contains("id field") && e.contains("filename slug")),
            "id mismatch: {errors:?}"
        );
    }

    #[test]
    fn validate_errors_on_too_few_sections() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("one-section.md");
        fs::write(&path,
            "---\nid = \"one-section\"\nversion = \"1.0.0\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"desc\"\n---\n             ## Only Section\n\nJust one.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(
            errors.iter().any(|e| e.contains("section")),
            "sections: {errors:?}"
        );
    }

    #[test]
    fn validate_errors_on_bad_semver() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("bad-version.md");
        fs::write(&path,
            "---\nid = \"bad-version\"\nversion = \"not-semver\"\ntier = \"any\"\n             stacks = []\nproject_shapes = []\ndescription = \"desc\"\n---\n             ## Section One\n\nText.\n\n## Section Two\n\nMore.\n").unwrap();

        let errors = validate_template_file(&path).expect("parse should succeed");
        assert!(
            errors.iter().any(|e| e.contains("semver")),
            "semver: {errors:?}"
        );
    }

    // ── is_valid_semver helper ────────────────────────────────────────────────

    #[test]
    fn semver_valid_versions() {
        assert!(is_valid_semver("1.0.0"));
        assert!(is_valid_semver("0.3.0"));
        assert!(is_valid_semver("10.20.30"));
        assert!(is_valid_semver("1.0.0-alpha.1"));
        assert!(is_valid_semver("1.0.0+build.1"));
    }

    #[test]
    fn semver_invalid_versions() {
        assert!(!is_valid_semver("1.0"));
        assert!(!is_valid_semver("1"));
        assert!(!is_valid_semver("not-semver"));
        assert!(!is_valid_semver("1.0.0.0"));
        assert!(!is_valid_semver(""));
    }

    // ── export: writes standalone .md ────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn export_writes_template_to_dir() {
        let bundled_dir = TempDir::new().unwrap();
        write_template_file(
            &bundled_dir,
            "gci-default.md",
            "gci-default",
            "gci",
            "## Overview\nBody content.\n",
        );
        let out_dir = TempDir::new().unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", bundled_dir.path()) };
        unsafe { std::env::remove_var("HOME") };

        let args = ExportArgs {
            id: "gci-default".to_string(),
            output: Some(out_dir.path().to_path_buf()),
        };
        tokio::runtime::Runtime::new()
            .unwrap()
            .block_on(args.run())
            .expect("export should succeed");

        let exported = out_dir.path().join("gci-default.md");
        assert!(exported.exists(), "exported file should exist");
        let content = fs::read_to_string(&exported).unwrap();
        assert!(content.contains("id = \"gci-default\""), "id: {content}");
        assert!(content.contains("## Overview"), "body: {content}");
        assert!(content.starts_with("---"), "frontmatter: {content}");
    }

    #[test]
    #[serial(global_env)]
    fn export_errors_on_unknown_id() {
        let bundled_dir = TempDir::new().unwrap();
        let out_dir = TempDir::new().unwrap();

        unsafe { std::env::set_var("CASCADE_BUNDLED_TEMPLATES", bundled_dir.path()) };
        unsafe { std::env::remove_var("HOME") };

        let args = ExportArgs {
            id: "nonexistent".to_string(),
            output: Some(out_dir.path().to_path_buf()),
        };
        let result = tokio::runtime::Runtime::new().unwrap().block_on(args.run());
        assert!(result.is_err(), "should error on unknown id");
    }
}
