//! `cascade template create`, `validate`, and `export` subcommands.
//!
//! Purpose: Template lifecycle management — scaffold new templates, validate
//! existing ones, and export templates to standalone files.
//! Inputs: id, tier, optional extends/output/path args.
//! Outputs: Created/exported files; validation errors to stderr.
//! Constraints: No logic changes — CLI plumbing only.

use super::helpers::{load_registry, not_found_error, parse_tier};
use super::Command;
use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::Args;
use std::path::{Path, PathBuf};

// ── cascade template create ────────────────────────────────────────────────────

/// Arguments for `cascade template create`.
///
/// Scaffolds a new user template at `~/.cascade/templates/<id>.md` (or `--output`).
/// The scaffold contains pre-filled TOML frontmatter and placeholder Markdown body.
/// Exits with an error if the file already exists (non-destructive).
#[derive(Debug, Args)]
pub struct CreateArgs {
    /// Unique id for the new template (e.g. `my-team-prc`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Tier to pre-fill in the manifest (gci, pci, apc, ppc, prc, pac, any).
    /// Defaults to `any`.
    #[arg(long, value_name = "TIER", default_value = "any")]
    pub tier: String,

    /// Optional parent template id for the `extends` field.
    #[arg(long, value_name = "PARENT")]
    pub extends: Option<String>,

    /// Output path for the scaffold file.
    /// Defaults to `~/.cascade/templates/<id>.md`.
    #[arg(long, value_name = "PATH")]
    pub output: Option<PathBuf>,
}

#[async_trait]
impl Command for CreateArgs {
    async fn run(&self) -> Result<()> {
        parse_tier(&self.tier)?;

        let out_path = match &self.output {
            Some(p) => p.clone(),
            None => {
                let home = std::env::var_os("HOME").ok_or_else(|| {
                    CascadeError::Other("HOME is not set; use --output to specify a path".into())
                })?;
                PathBuf::from(home)
                    .join(".cascade")
                    .join("templates")
                    .join(format!("{}.md", self.id))
            }
        };

        if out_path.exists() {
            return Err(CascadeError::Other(format!(
                "Template file already exists: {}  — delete it first or choose a different id.",
                out_path.display()
            )));
        }

        if let Some(parent) = out_path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| {
                CascadeError::Other(format!("cannot create directory {}: {e}", parent.display()))
            })?;
        }

        let extends_line = match &self.extends {
            Some(p) => format!("extends = \"{p}\"\n"),
            None => String::new(),
        };
        let scaffold = format!(
            "---\n\
             id = \"{id}\"\n\
             version = \"1.0.0\"\n\
             tier = \"{tier}\"\n\
             stacks = []\n\
             project_shapes = []\n\
             description = \"\"\n\
             {extends}---\n\
             \n\
             ## Purpose\n\
             \n\
             <!-- Describe what this template is for and when to use it. -->\n\
             \n\
             ## Key Conventions\n\
             \n\
             <!-- List the key conventions this template enforces. -->\n\
             \n\
             ## File Layout\n\
             \n\
             <!-- Describe the expected directory/file structure for projects using this template. -->\n",
            id = self.id,
            tier = self.tier,
            extends = extends_line,
        );

        std::fs::write(&out_path, &scaffold)
            .map_err(|e| CascadeError::Other(format!("write {}: {e}", out_path.display())))?;

        println!("Created template scaffold: {}", out_path.display());
        println!(
            "Edit the file, then run: cascade template validate {}",
            out_path.display()
        );
        Ok(())
    }
}

// ── cascade template validate ──────────────────────────────────────────────────

/// Arguments for `cascade template validate`.
///
/// Parses a template file and validates:
/// - Frontmatter parses as `TemplateManifest` (TOML schema).
/// - `id` field matches the filename slug (sans `.md` extension).
/// - `version` is a valid semver string.
/// - `description` is non-empty.
/// - Body contains at least 2 Markdown `##` sections.
///
/// Prints `PASS` on success (exit 0) or a numbered list of errors (exit 1).
#[derive(Debug, Args)]
pub struct ValidateArgs {
    /// Path to the `.md` template file to validate.
    #[arg(value_name = "PATH")]
    pub path: PathBuf,
}

#[async_trait]
impl Command for ValidateArgs {
    async fn run(&self) -> Result<()> {
        let errors = validate_template_file(&self.path)?;
        if errors.is_empty() {
            println!("PASS  {}", self.path.display());
            Ok(())
        } else {
            eprintln!("FAIL  {}", self.path.display());
            for (i, e) in errors.iter().enumerate() {
                eprintln!("  {}. {e}", i + 1);
            }
            std::process::exit(1);
        }
    }
}

/// Validate a template file path; returns a list of actionable error strings.
/// Returns `Err` only for I/O or parse errors that prevent further checks.
pub(super) fn validate_template_file(path: &Path) -> Result<Vec<String>> {
    use cascade_core::templates::parser::parse_template_file;

    let record = parse_template_file(path)
        .map_err(|e| CascadeError::Other(format!("cannot parse {}: {e}", path.display())))?;

    let mut errors: Vec<String> = Vec::new();

    let file_stem = path.file_stem().and_then(|s| s.to_str()).unwrap_or("");
    if record.manifest.id != file_stem {
        errors.push(format!(
            "id field '{}' does not match filename slug '{}' — rename the file or update the id field",
            record.manifest.id, file_stem
        ));
    }

    if !is_valid_semver(&record.manifest.version) {
        errors.push(format!(
            "version '{}' is not valid semver — use the form MAJOR.MINOR.PATCH (e.g. 1.0.0)",
            record.manifest.version
        ));
    }

    if record.manifest.description.trim().is_empty() {
        errors.push(
            "description is empty — add a short human-readable description of this template".into(),
        );
    }

    let section_count = record.body.lines().filter(|l| l.starts_with("## ")).count();
    if section_count < 2 {
        errors.push(format!(
            "body has {section_count} `##` section(s) — at least 2 are required \
             (e.g. ## Purpose and ## Key Conventions)"
        ));
    }

    Ok(errors)
}

/// Check that a version string is valid semver (MAJOR.MINOR.PATCH).
///
/// Accepts optional pre-release (`-alpha.1`) and build metadata (`+build.1`)
/// per semver 2.0.0, but the three numeric dot-separated components are required.
pub(super) fn is_valid_semver(v: &str) -> bool {
    let v = v.split('+').next().unwrap_or(v);
    let v = v.split('-').next().unwrap_or(v);
    let parts: Vec<&str> = v.split('.').collect();
    if parts.len() != 3 {
        return false;
    }
    parts.iter().all(|p| p.parse::<u64>().is_ok())
}

// ── cascade template export ────────────────────────────────────────────────────

/// Arguments for `cascade template export`.
///
/// Exports a template (bundled or user) to a directory as a standalone `.md`
/// file that preserves frontmatter + body, ready for sharing.
#[derive(Debug, Args)]
pub struct ExportArgs {
    /// Template id to export (e.g. `gci-default`).
    #[arg(long, value_name = "ID")]
    pub id: String,

    /// Directory to write the exported file into.
    /// Defaults to the current working directory.
    #[arg(long, value_name = "DIR")]
    pub output: Option<PathBuf>,
}

#[async_trait]
impl Command for ExportArgs {
    async fn run(&self) -> Result<()> {
        let reg = load_registry()?;
        let record = reg.get(&self.id).ok_or_else(|| not_found_error(&self.id))?;

        let out_dir = match &self.output {
            Some(d) => d.clone(),
            None => std::env::current_dir()
                .map_err(|e| CascadeError::Other(format!("cannot determine CWD: {e}")))?,
        };

        std::fs::create_dir_all(&out_dir).map_err(|e| {
            CascadeError::Other(format!(
                "cannot create directory {}: {e}",
                out_dir.display()
            ))
        })?;

        let out_file = out_dir.join(format!("{}.md", self.id));

        let fm = toml::to_string(&record.manifest)
            .map_err(|e| CascadeError::Other(format!("serialize manifest: {e}")))?;
        let content = format!("---\n{fm}---\n{}", record.body);

        std::fs::write(&out_file, &content)
            .map_err(|e| CascadeError::Other(format!("write {}: {e}", out_file.display())))?;

        println!("Exported template '{}' → {}", self.id, out_file.display());
        Ok(())
    }
}
