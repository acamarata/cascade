//! PluginLoader — discovers and instantiates WASM plugins from `~/.cascade/plugins/`.
//!
//! Purpose: scan a plugins directory (one level deep), load the `plugin.json`
//!   manifest for each subdirectory, read the WASM bytes, and compile the module
//!   via `PluginSandbox`. Returns a partial-load result so one bad plugin never
//!   aborts the rest.
//! Inputs:  A directory path (canonically `~/.cascade/plugins/`).
//! Outputs: `(Vec<LoadedPlugin>, Vec<PluginLoadError>)` — all successes and all
//!   per-plugin failures, separated, so callers can log failures without panicking.
//! Constraints:
//!   - Non-recursive scan (one directory level only).
//!   - Symlinks inside the plugins dir are NOT followed (prevents path-traversal).
//!   - Tracing spans are emitted per plugin attempt (`plugin_id` field populated).
//!
//! SPORT: cascade-plugins / loader layer (T-P4-E03-07)

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;
use tracing::{error, info, instrument, warn};

use crate::manifest::{ManifestError, PluginJsonManifest, PluginType};
use crate::runtime::{LoadedPlugin, PluginSandbox};

// ── Error type ────────────────────────────────────────────────────────────────

/// Per-plugin load failure returned by `PluginLoader::scan`.
///
/// Each variant is actionable: it tells the operator exactly what went wrong
/// and which plugin directory triggered the failure.
#[derive(Debug, Error, Serialize, Deserialize)]
pub enum PluginLoadError {
    /// No `plugin.json` file found in the plugin directory.
    #[error("plugin.json not found in '{dir}'")]
    ManifestNotFound { dir: PathBuf },

    /// `plugin.json` exists but failed to parse or validate.
    #[error("manifest error in '{dir}': {reason}")]
    ManifestInvalid { dir: PathBuf, reason: String },

    /// The WASM binary declared in `entry_wasm` was not found.
    #[error("WASM file not found: '{path}'")]
    WasmNotFound { path: PathBuf },

    /// The WASM binary could not be read from disk.
    #[error("failed to read WASM file '{path}': {reason}")]
    WasmReadError { path: PathBuf, reason: String },

    /// The WASM sandbox failed to compile or initialise the module.
    #[error("sandbox error for plugin '{id}': {reason}")]
    SandboxError { id: String, reason: String },

    /// A plugin whose `disable` marker file is present is skipped.
    #[error("plugin '{id}' is disabled (found .disabled marker)")]
    Disabled { id: String },

    /// The filesystem entry is not a directory (files and symlinks are skipped).
    #[error("skipping non-directory entry '{path}'")]
    NotADirectory { path: PathBuf },
}

// ── LoadedPlugin extension: carry the plugin dir path ────────────────────────

/// A compiled plugin plus the directory it was loaded from.
///
/// The `plugin_dir` field is used by the hot-reload watcher to watch for WASM
/// changes and by the registry to persist enable/disable state.
pub struct DiscoveredPlugin {
    /// Compiled, sandboxed plugin module ready for invocation.
    pub loaded: LoadedPlugin,
    /// The directory under `~/.cascade/plugins/` this plugin was loaded from.
    pub plugin_dir: PathBuf,
    /// Parsed manifest (preserved for registry display and dispatch routing).
    pub manifest: PluginJsonManifest,
}

// ── PluginLoader ──────────────────────────────────────────────────────────────

/// Scans a plugins directory and loads all valid WASM plugins.
///
/// # Design
/// - One sandbox is created per plugin type group (lightweight vs data-heavy)
///   and reused within the scan to amortise `Engine` construction.
/// - Bad plugins are isolated: their error is collected and the scan continues.
/// - Symlinks in the plugins dir are NOT followed.
pub struct PluginLoader;

impl PluginLoader {
    /// Scan `plugins_dir` for plugin subdirectories and load each one.
    ///
    /// Returns a tuple `(loaded, errors)`:
    /// - `loaded`: every plugin that compiled and initialised successfully.
    /// - `errors`: one `PluginLoadError` per failed or skipped plugin directory.
    ///
    /// An empty `plugins_dir` is valid and returns `(vec![], vec![])`.
    #[instrument(name = "plugin_loader_scan", fields(plugins_dir = %plugins_dir.display()))]
    pub fn scan(plugins_dir: &Path) -> (Vec<DiscoveredPlugin>, Vec<PluginLoadError>) {
        let mut loaded = Vec::new();
        let mut errors = Vec::new();

        let read_dir = match std::fs::read_dir(plugins_dir) {
            Ok(rd) => rd,
            Err(e) => {
                // Non-existent or unreadable plugins dir is not an error: the
                // daemon may start before the user has installed any plugins.
                info!(
                    plugins_dir = %plugins_dir.display(),
                    reason = %e,
                    "plugins directory not found or unreadable — no plugins loaded"
                );
                return (loaded, errors);
            }
        };

        for entry_result in read_dir {
            let entry = match entry_result {
                Ok(e) => e,
                Err(e) => {
                    warn!(reason = %e, "failed to read directory entry — skipping");
                    continue;
                }
            };

            let entry_path = entry.path();

            // Only enter real directories — no symlinks (path-traversal guard).
            let file_type = match entry.file_type() {
                Ok(ft) => ft,
                Err(e) => {
                    warn!(path = %entry_path.display(), reason = %e, "cannot read file type — skipping");
                    continue;
                }
            };
            if !file_type.is_dir() {
                errors.push(PluginLoadError::NotADirectory {
                    path: entry_path.clone(),
                });
                continue;
            }

            // Attempt to load this plugin directory.
            match Self::load_one(&entry_path) {
                Ok(discovered) => {
                    info!(
                        plugin_id = %discovered.manifest.id,
                        plugin_dir = %entry_path.display(),
                        "plugin loaded successfully"
                    );
                    loaded.push(discovered);
                }
                Err(e) => {
                    error!(
                        plugin_dir = %entry_path.display(),
                        reason = %e,
                        "plugin load failed — skipping"
                    );
                    errors.push(e);
                }
            }
        }

        (loaded, errors)
    }

    /// Load a single plugin directory.
    ///
    /// Steps:
    /// 1. Check for `.disabled` marker → `PluginLoadError::Disabled`
    /// 2. Load and validate `plugin.json`
    /// 3. Resolve `entry_wasm` path
    /// 4. Read WASM bytes
    /// 5. Build sandbox + compile module
    #[instrument(name = "plugin_load_one", fields(plugin_dir = %plugin_dir.display()))]
    fn load_one(plugin_dir: &Path) -> Result<DiscoveredPlugin, PluginLoadError> {
        // 1. Skip disabled plugins.
        if plugin_dir.join(".disabled").exists() {
            // Read the manifest id for a nicer error if possible; fall back to dir name.
            let id = plugin_dir
                .file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned());
            return Err(PluginLoadError::Disabled { id });
        }

        // 2. Load manifest.
        let manifest_path = plugin_dir.join("plugin.json");
        if !manifest_path.exists() {
            return Err(PluginLoadError::ManifestNotFound {
                dir: plugin_dir.to_owned(),
            });
        }

        let manifest = PluginJsonManifest::load(&manifest_path).map_err(|e: ManifestError| {
            PluginLoadError::ManifestInvalid {
                dir: plugin_dir.to_owned(),
                reason: e.to_string(),
            }
        })?;

        // 3. Resolve WASM path (entry_wasm must be relative per manifest validation).
        let wasm_path = plugin_dir.join(&manifest.entry_wasm);
        if !wasm_path.exists() {
            return Err(PluginLoadError::WasmNotFound { path: wasm_path });
        }

        // 4. Read WASM bytes.
        let wasm_bytes = std::fs::read(&wasm_path).map_err(|e| PluginLoadError::WasmReadError {
            path: wasm_path.clone(),
            reason: e.to_string(),
        })?;

        // 5. Compile via sandbox.  Map PluginJsonManifest.kind -> PluginType for resource limits.
        let plugin_type = kind_to_type(manifest.kind);
        let sandbox =
            PluginSandbox::new(plugin_type).map_err(|e| PluginLoadError::SandboxError {
                id: manifest.id.clone(),
                reason: e.to_string(),
            })?;

        let loaded = sandbox
            .load_with_permissions_and_data_dir(
                &wasm_bytes,
                &manifest.id,
                manifest.permissions.clone(),
                None,
                &plugin_dir.join("data"),
            )
            .map_err(|e| PluginLoadError::SandboxError {
                id: manifest.id.clone(),
                reason: e.to_string(),
            })?;

        Ok(DiscoveredPlugin {
            loaded,
            plugin_dir: plugin_dir.to_owned(),
            manifest,
        })
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Map a `PluginKind` (from `plugin.json`) to the `PluginType` enum used for
/// sandbox resource-limit selection.
///
/// WHY: `PluginKind` is the public-contract enum; `PluginType` is the internal
/// sandbox enum.  They have different variants but overlap on the resource
/// profiles that matter (small vs large memory budget).
fn kind_to_type(kind: crate::manifest::PluginKind) -> PluginType {
    use crate::manifest::PluginKind;
    match kind {
        PluginKind::Agent => PluginType::Agent,
        PluginKind::Provider => PluginType::EmbeddingProvider,
        PluginKind::DataSource => PluginType::Retriever,
        PluginKind::ChatTool => PluginType::ToolIntegration,
        PluginKind::Widget => PluginType::OutputRenderer,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    /// Minimal valid `plugin.json` for test fixtures.
    fn valid_manifest_json(id: &str) -> String {
        format!(
            r#"{{
                "id": "{id}",
                "name": "Test Plugin",
                "version": "1.0.0",
                "description": "A test plugin",
                "author": "test",
                "license": "MIT",
                "entry_wasm": "{id}.wasm",
                "kind": "ChatTool",
                "min_cascade_version": ">=0.1.0"
            }}"#
        )
    }

    /// Write a minimal valid WAT module as WASM bytes to `path`.
    fn write_minimal_wasm(path: &Path) {
        // A trivial WAT module that exports nothing but compiles cleanly.
        let wat = r#"(module)"#;
        let wasm = wat::parse_str(wat).expect("wat::parse_str");
        fs::write(path, wasm).expect("write wasm");
    }

    /// Create a valid plugin directory with `plugin.json` + WASM binary.
    fn make_valid_plugin(dir: &Path, id: &str) {
        let plugin_dir = dir.join(id);
        fs::create_dir_all(&plugin_dir).unwrap();
        fs::write(plugin_dir.join("plugin.json"), valid_manifest_json(id)).unwrap();
        write_minimal_wasm(&plugin_dir.join(format!("{id}.wasm")));
    }

    #[test]
    fn scan_empty_dir_returns_no_errors() {
        let tmp = TempDir::new().unwrap();
        let (loaded, errors) = PluginLoader::scan(tmp.path());
        assert!(loaded.is_empty(), "expected no loaded plugins");
        assert!(errors.is_empty(), "expected no errors");
    }

    #[test]
    fn scan_nonexistent_dir_returns_empty() {
        let (loaded, errors) = PluginLoader::scan(Path::new("/tmp/__cascade_no_such_dir_xyz__"));
        assert!(loaded.is_empty());
        assert!(
            errors.is_empty(),
            "non-existent dir should not produce errors"
        );
    }

    #[test]
    fn scan_partial_load_two_valid_one_invalid() {
        let tmp = TempDir::new().unwrap();

        // Two valid plugins.
        make_valid_plugin(tmp.path(), "com.example.plugin-a");
        make_valid_plugin(tmp.path(), "com.example.plugin-b");

        // One invalid: directory present but no plugin.json.
        fs::create_dir_all(tmp.path().join("com.example.bad")).unwrap();

        let (loaded, errors) = PluginLoader::scan(tmp.path());

        assert_eq!(
            loaded.len(),
            2,
            "should load 2 valid plugins (errors: {})",
            errors
                .iter()
                .map(|e| e.to_string())
                .collect::<Vec<_>>()
                .join(", ")
        );
        assert_eq!(errors.len(), 1, "should report 1 error; errors: {errors:?}");

        // The error for the missing manifest must be ManifestNotFound.
        assert!(
            matches!(errors[0], PluginLoadError::ManifestNotFound { .. }),
            "expected ManifestNotFound, got {:?}",
            errors[0]
        );
    }

    #[test]
    fn scan_missing_manifest_produces_manifest_not_found() {
        let tmp = TempDir::new().unwrap();
        // Directory with no plugin.json.
        fs::create_dir_all(tmp.path().join("com.example.no-manifest")).unwrap();

        let (loaded, errors) = PluginLoader::scan(tmp.path());

        assert!(loaded.is_empty());
        assert_eq!(errors.len(), 1);
        assert!(matches!(
            errors[0],
            PluginLoadError::ManifestNotFound { .. }
        ));
    }

    #[test]
    fn scan_disabled_plugin_is_skipped_not_panicked() {
        let tmp = TempDir::new().unwrap();
        let plugin_dir = tmp.path().join("com.example.disabled-plugin");
        fs::create_dir_all(&plugin_dir).unwrap();
        fs::write(
            plugin_dir.join("plugin.json"),
            valid_manifest_json("com.example.disabled-plugin"),
        )
        .unwrap();
        write_minimal_wasm(&plugin_dir.join("com.example.disabled-plugin.wasm"));
        // Place the disable marker.
        fs::write(plugin_dir.join(".disabled"), "").unwrap();

        let (loaded, errors) = PluginLoader::scan(tmp.path());

        assert!(loaded.is_empty(), "disabled plugin should not be loaded");
        assert_eq!(errors.len(), 1);
        assert!(matches!(errors[0], PluginLoadError::Disabled { .. }));
    }
}
