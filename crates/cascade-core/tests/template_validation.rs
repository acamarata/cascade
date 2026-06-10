//! # Template validation integration test
//!
//! Validates that all bundled templates in `data/templates/` parse without error,
//! and that every file entry in `index.json` exists on disk.
//!
//! This test is invoked by `.github/workflows/template-ci.yml` via:
//!   `cargo test -p cascade-core --test template_validation`
//!
//! With zero templates in `data/templates/` (W-01 state), the registry is empty
//! and the test passes. As W-02 adds template files, this test grows automatically.

use cascade_core::templates::registry::{TemplateFilter, TemplateRegistry};
use std::path::{Path, PathBuf};

/// Resolve `data/templates/` relative to this crate's manifest directory.
///
/// `CARGO_MANIFEST_DIR` is set by cargo at test compile time and points to
/// `crates/cascade-core/`. The bundled data directory is at the repo root:
/// `../../data/templates/` relative to the crate manifest.
fn bundled_templates_dir() -> PathBuf {
    let manifest = env!("CARGO_MANIFEST_DIR");
    Path::new(manifest)
        .join("../../data/templates")
        .canonicalize()
        .unwrap_or_else(|_| Path::new(manifest).join("../../data/templates").to_path_buf())
}

/// Parse `data/templates/index.json` and return the list of `file` entries.
fn index_file_entries(templates_dir: &Path) -> Vec<String> {
    let index_path = templates_dir.join("index.json");
    if !index_path.exists() {
        return vec![];
    }
    let raw = std::fs::read_to_string(&index_path)
        .expect("index.json should be readable");
    let v: serde_json::Value =
        serde_json::from_str(&raw).expect("index.json should be valid JSON");
    v["templates"]
        .as_array()
        .map(|arr| {
            arr.iter()
                .filter_map(|entry| entry["file"].as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

/// All `.md` files under `data/templates/` must parse cleanly.
///
/// With an empty template directory this is vacuously true. As templates are
/// added in W-02 they are automatically validated.
#[test]
fn all_bundled_templates_parse_without_error() {
    let dir = bundled_templates_dir();

    // Use a two-pass approach: load the registry and compare to raw file count.
    let reg = TemplateRegistry::load_dirs(&[&dir]);

    // Count .md files manually to detect skipped-due-to-parse-errors.
    let mut md_count = 0usize;
    count_md_files(&dir, &mut md_count);

    assert_eq!(
        reg.len(),
        md_count,
        "registry loaded {} templates but found {} .md files — \
         {} file(s) failed to parse (check tracing output for details)",
        reg.len(),
        md_count,
        md_count.saturating_sub(reg.len()),
    );
}

/// The registry loads without error even when the templates directory is empty.
#[test]
fn empty_bundled_dir_returns_empty_registry() {
    // data/templates/ may contain subdirs with .gitkeep only.
    // The registry must tolerate that gracefully.
    let dir = bundled_templates_dir();
    let reg = TemplateRegistry::load_dirs(&[&dir]);
    // Just assert it doesn't panic. The count may be 0 or >0 depending on phase.
    let _ = reg.len();
}

/// Every `file` entry in `index.json` must exist on disk.
#[test]
fn index_json_all_files_exist() {
    let dir = bundled_templates_dir();
    let entries = index_file_entries(&dir);

    let mut missing = Vec::new();
    for file in &entries {
        let full_path = dir.join(file);
        if !full_path.exists() {
            missing.push(file.clone());
        }
    }

    assert!(
        missing.is_empty(),
        "index.json references files that don't exist on disk: {:?}",
        missing
    );
}

/// `list()` with no filter returns all loaded templates.
#[test]
fn list_no_filter_returns_all_templates() {
    let dir = bundled_templates_dir();
    let reg = TemplateRegistry::load_dirs(&[&dir]);
    let all = reg.list(&TemplateFilter::default());
    assert_eq!(
        all.len(),
        reg.len(),
        "list() with empty filter should return all templates"
    );
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Recursively count `.md` files under `dir`.
fn count_md_files(dir: &Path, count: &mut usize) {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            count_md_files(&path, count);
        } else if path.extension().and_then(|e| e.to_str()) == Some("md") {
            *count += 1;
        }
    }
}
