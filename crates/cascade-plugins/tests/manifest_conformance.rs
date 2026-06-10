//! Manifest conformance test — loads every plugin.json in plugins/ and
//! examples/plugins/ through the real PluginJsonManifest validator.
//!
//! Purpose: guarantee that all shipped plugin.json files satisfy the canonical
//!   schema (PluginJsonManifest::load) so schema drift is caught at CI time.
//! Inputs: plugin.json files discovered under the two canonical plugin roots.
//! Outputs: test pass / fail per file; panics with an actionable message listing
//!   every failing path + error.
//! Constraints: walks only two well-known directory trees; does not recurse into
//!   arbitrary paths. Test is host-only (not wasm32 target).
//! SPORT: cascade-plugins / tests / manifest_conformance

use std::path::PathBuf;

use cascade_plugins::manifest::PluginJsonManifest;

/// Return the repo root (two levels up from `crates/cascade-plugins/`).
fn repo_root() -> PathBuf {
    // file!() = "crates/cascade-plugins/tests/manifest_conformance.rs"
    // CARGO_MANIFEST_DIR = ".../crates/cascade-plugins"
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    manifest_dir
        .parent() // crates/
        .expect("crates/ parent must exist")
        .parent() // repo root
        .expect("repo root must exist")
        .to_owned()
}

/// Discover every `plugin.json` file under `base_dir` recursively.
fn find_plugin_jsons(base_dir: &std::path::Path) -> Vec<PathBuf> {
    let mut results = Vec::new();
    if !base_dir.exists() {
        return results;
    }
    let walker = walkdir::WalkDir::new(base_dir)
        .follow_links(false)
        .into_iter()
        .filter_map(|e| e.ok())
        .filter(|e| e.file_name() == "plugin.json");
    for entry in walker {
        results.push(entry.path().to_owned());
    }
    results.sort();
    results
}

#[test]
fn manifest_conformance_all_plugin_jsons_pass_validator() {
    let root = repo_root();

    let mut search_roots = vec![
        root.join("plugins"),
        root.join("examples").join("plugins"),
    ];

    // Collect all plugin.json files across both trees
    let mut all_files: Vec<PathBuf> = Vec::new();
    for dir in &mut search_roots {
        all_files.extend(find_plugin_jsons(dir));
    }

    assert!(
        !all_files.is_empty(),
        "Expected to find plugin.json files under plugins/ and examples/plugins/ — \
         check that the repo root is being resolved correctly (got: {root:?})"
    );

    let mut failures: Vec<(PathBuf, String)> = Vec::new();

    for path in &all_files {
        match PluginJsonManifest::load(path) {
            Ok(_) => {}
            Err(e) => failures.push((path.clone(), e.to_string())),
        }
    }

    if !failures.is_empty() {
        let msg = failures
            .iter()
            .map(|(p, e)| format!("  {p:?}: {e}"))
            .collect::<Vec<_>>()
            .join("\n");
        panic!(
            "{} plugin.json file(s) failed validation:\n{msg}\n\n\
             Checked {} file(s) total.",
            failures.len(),
            all_files.len()
        );
    }

    println!(
        "manifest_conformance: all {} plugin.json file(s) passed validation",
        all_files.len()
    );
}
