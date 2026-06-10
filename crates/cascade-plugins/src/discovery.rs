//! Plugin discovery — scan global and local plugin directories.
//!
//! Purpose: find installed plugins in `~/.cascade/plugins/` (global, user-installed)
//!   and `.cascade/plugins/` (per-cascade, project-scoped). Each directory entry
//!   is expected to contain `plugin.wasm` + `cascade-plugin.toml`.
//! Inputs: optional home dir and optional per-cascade root (defaults to cwd).
//! Outputs: `Vec<DiscoveredPlugin>` with the path, manifest, and wasm bytes.
//! Constraints: scan is non-recursive (one directory level); symlinks are followed
//!   at the directory level but capability checks happen in the sandbox layer.
//! SPORT: cascade-plugins / discovery layer

use std::path::{Path, PathBuf};

use thiserror::Error;

use crate::manifest::{ManifestError, PluginManifest};

/// Errors produced during discovery.
#[derive(Debug, Error)]
pub enum DiscoveryError {
    #[error("failed to read plugin directory '{path}': {source}")]
    ReadDir {
        path: PathBuf,
        source: std::io::Error,
    },

    #[error("plugin entry '{path}' has no cascade-plugin.toml; skipping")]
    MissingManifest { path: PathBuf },

    #[error("plugin entry '{path}' has no plugin.wasm; skipping")]
    MissingWasm { path: PathBuf },

    #[error("manifest error in '{path}': {source}")]
    Manifest {
        path: PathBuf,
        source: ManifestError,
    },

    #[error("I/O error reading '{path}': {source}")]
    Io {
        path: PathBuf,
        source: std::io::Error,
    },
}

/// A plugin discovered on disk, ready to be validated and loaded.
#[derive(Debug)]
pub struct DiscoveredPlugin {
    /// Directory containing this plugin.
    pub plugin_dir: PathBuf,
    /// Parsed manifest.
    pub manifest: PluginManifest,
    /// Raw WASM bytes (not yet compiled or instantiated).
    pub wasm_bytes: Vec<u8>,
    /// Origin of discovery: global home dir or per-cascade dir.
    pub origin: PluginOrigin,
}

/// Where a plugin was found.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PluginOrigin {
    /// `~/.cascade/plugins/<name>/`
    Global,
    /// `.cascade/plugins/<name>/` (relative to per-cascade root)
    Local,
}

/// Discover all installed plugins from both directories.
///
/// Errors for individual plugins are collected and returned alongside successes
/// rather than aborting the whole scan. The daemon logs errors and skips the
/// offending plugin, keeping others functional.
pub fn discover_all(cascade_root: Option<&Path>) -> (Vec<DiscoveredPlugin>, Vec<DiscoveryError>) {
    let mut plugins = Vec::new();
    let mut errors = Vec::new();

    // Global plugins: ~/.cascade/plugins/
    if let Some(home) = dirs::home_dir() {
        let global_dir = home.join(".cascade").join("plugins");
        let (mut found, mut errs) = scan_plugin_dir(&global_dir, PluginOrigin::Global);
        plugins.append(&mut found);
        errors.append(&mut errs);
    }

    // Per-cascade plugins: <cascade_root>/.cascade/plugins/
    let local_root = cascade_root
        .map(|p| p.to_owned())
        .unwrap_or_else(|| std::env::current_dir().unwrap_or_default());
    let local_dir = local_root.join(".cascade").join("plugins");
    let (mut found, mut errs) = scan_plugin_dir(&local_dir, PluginOrigin::Local);
    plugins.append(&mut found);
    errors.append(&mut errs);

    (plugins, errors)
}

/// Scan a single plugin directory, yielding one `DiscoveredPlugin` per entry.
///
/// Non-fatal errors (missing manifest, bad TOML) are collected in the error vec
/// rather than stopping the scan. This matches the daemon's "skip, don't fail" policy.
fn scan_plugin_dir(
    dir: &Path,
    origin: PluginOrigin,
) -> (Vec<DiscoveredPlugin>, Vec<DiscoveryError>) {
    if !dir.exists() {
        return (vec![], vec![]);
    }

    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(source) => {
            return (
                vec![],
                vec![DiscoveryError::ReadDir {
                    path: dir.to_owned(),
                    source,
                }],
            );
        }
    };

    let mut plugins = Vec::new();
    let mut errors = Vec::new();

    for entry in entries.flatten() {
        let plugin_dir = entry.path();
        if !plugin_dir.is_dir() {
            continue;
        }

        let manifest_path = plugin_dir.join("cascade-plugin.toml");
        let wasm_path = plugin_dir.join("plugin.wasm");

        if !manifest_path.exists() {
            errors.push(DiscoveryError::MissingManifest { path: plugin_dir });
            continue;
        }
        if !wasm_path.exists() {
            errors.push(DiscoveryError::MissingWasm { path: plugin_dir });
            continue;
        }

        let manifest_bytes = match std::fs::read(&manifest_path) {
            Ok(b) => b,
            Err(source) => {
                errors.push(DiscoveryError::Io {
                    path: manifest_path,
                    source,
                });
                continue;
            }
        };

        let manifest = match PluginManifest::parse(&manifest_bytes) {
            Ok(m) => m,
            Err(source) => {
                errors.push(DiscoveryError::Manifest {
                    path: plugin_dir,
                    source,
                });
                continue;
            }
        };

        let wasm_bytes = match std::fs::read(&wasm_path) {
            Ok(b) => b,
            Err(source) => {
                errors.push(DiscoveryError::Io {
                    path: wasm_path,
                    source,
                });
                continue;
            }
        };

        plugins.push(DiscoveredPlugin {
            plugin_dir,
            manifest,
            wasm_bytes,
            origin,
        });
    }

    (plugins, errors)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn write_plugin(base: &Path, name: &str, manifest_toml: &str, wasm: &[u8]) {
        let dir = base.join(name);
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join("cascade-plugin.toml"), manifest_toml).unwrap();
        fs::write(dir.join("plugin.wasm"), wasm).unwrap();
    }

    const VALID_MANIFEST: &str = r#"
schema_version = 1
name = "test-chunker"
version = "1.0.0"
description = "test"
plugin_type = "chunker"
cascade_version = ">=0.1.0"
"#;

    #[test]
    fn discovers_valid_plugin() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join(".cascade").join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        // Write fake WASM bytes (real compilation not needed for discovery tests).
        write_plugin(
            &plugins_dir,
            "test-chunker",
            VALID_MANIFEST,
            b"\0asm\x01\0\0\0",
        );

        let (found, errors) = scan_plugin_dir(&plugins_dir, PluginOrigin::Local);
        assert_eq!(errors.len(), 0, "unexpected errors: {errors:?}");
        assert_eq!(found.len(), 1);
        assert_eq!(found[0].manifest.name, "test-chunker");
    }

    #[test]
    fn skips_entry_missing_manifest() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join(".cascade").join("plugins");
        let dir = plugins_dir.join("no-manifest-plugin");
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join("plugin.wasm"), b"\0asm").unwrap();
        // No cascade-plugin.toml written.

        let (found, errors) = scan_plugin_dir(&plugins_dir, PluginOrigin::Local);
        assert_eq!(found.len(), 0);
        assert_eq!(errors.len(), 1);
        assert!(matches!(errors[0], DiscoveryError::MissingManifest { .. }));
    }
}
