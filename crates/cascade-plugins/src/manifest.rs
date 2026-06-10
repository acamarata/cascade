//! Plugin manifest — two formats supported:
//!
//! 1. `cascade-plugin.toml` (legacy / internal) — TOML, schema-versioned.
//! 2. `plugin.json` (public plugin contract) — JSON, serde + schemars, validated by
//!    `PluginJsonManifest::load(path)`.
//!
//! Purpose: parse and validate plugin manifest files; emit actionable errors on any
//!   schema or value violation.
//! Inputs: TOML bytes (legacy) or a `plugin.json` path (public contract).
//! Outputs: `PluginManifest` / `PluginJsonManifest` structs or `ManifestError` with
//!   actionable messages.
//! Constraints: DENY-by-default for all permissions; reverse-domain `id` format
//!   enforced; all paths in `plugin.json` must be relative (no absolute WASM paths).
//! SPORT: cascade-plugins / manifest layer

use std::path::{Path, PathBuf};

use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::capability::DeclaredCapabilities;

// ─── TOML manifest (legacy / internal) ───────────────────────────────────────

/// Maximum schema version this build of Cascade supports (TOML manifest).
pub const HOST_MAX_SCHEMA_VERSION: u32 = 1;

/// Plugin type variants used in the TOML manifest.
///
/// Maps to ABI entry points and sandbox resource profiles.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum PluginType {
    Chunker,
    Retriever,
    Parser,
    EmbeddingProvider,
    Reranker,
    QueryStrategy,
    Agent,
    OutputRenderer,
    ModeCustomizer,
    ToolIntegration,
}

/// Parsed `cascade-plugin.toml` manifest (TOML format).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginManifest {
    /// Schema version for this manifest format.
    ///
    /// # Forward-compat policy
    /// - Adding optional fields: backward-compatible, no version bump needed.
    /// - Removing, renaming, or changing types of fields: requires incrementing this value.
    pub schema_version: u32,

    /// Plugin identifier — must be a valid Rust crate name (lowercase, hyphens).
    pub name: String,

    /// Semantic version string (e.g. "1.0.0").
    pub version: String,

    /// Human-readable description.
    pub description: String,

    /// The plugin type determines ABI entry points and sandbox resource profile.
    pub plugin_type: PluginType,

    /// Semver range of Cascade host versions this plugin is compatible with.
    pub cascade_version: String,

    /// Required capabilities declared by this plugin.
    #[serde(default)]
    pub capabilities: DeclaredCapabilities,

    /// File extensions this plugin handles (Parser plugins only).
    #[serde(default)]
    pub extensions: Vec<String>,

    /// ABI version this plugin was compiled against.
    #[serde(default = "default_abi_version")]
    pub abi_version: u32,
}

fn default_abi_version() -> u32 {
    1
}

impl PluginManifest {
    /// Parse and validate a `cascade-plugin.toml` manifest.
    pub fn parse(toml_bytes: &[u8]) -> Result<Self, ManifestError> {
        let raw: toml::Value = toml::from_str(std::str::from_utf8(toml_bytes).unwrap_or_default())
            .map_err(|e| ManifestError::TomlParse(e.to_string()))?;

        let version_val = raw
            .get("schema_version")
            .ok_or(ManifestError::MissingSchemaVersion)?;

        let schema_version = version_val
            .as_integer()
            .ok_or(ManifestError::MissingSchemaVersion)? as u32;

        if schema_version > HOST_MAX_SCHEMA_VERSION {
            let name = raw
                .get("name")
                .and_then(|v| v.as_str())
                .unwrap_or("<unknown>")
                .to_owned();
            return Err(ManifestError::SchemaVersionTooNew {
                name,
                required: schema_version,
                supported: HOST_MAX_SCHEMA_VERSION,
            });
        }

        let manifest: PluginManifest =
            toml::from_str(std::str::from_utf8(toml_bytes).unwrap_or_default())
                .map_err(|e| ManifestError::TomlParse(e.to_string()))?;

        manifest.validate_toml()?;
        Ok(manifest)
    }

    fn validate_toml(&self) -> Result<(), ManifestError> {
        if self.name.is_empty() {
            return Err(ManifestError::Validation {
                id: "<empty>".to_owned(),
                reason: "plugin name must not be empty".to_owned(),
            });
        }
        if self.name.contains(' ') {
            return Err(ManifestError::Validation {
                id: self.name.clone(),
                reason: "plugin name must not contain spaces; use hyphens".to_owned(),
            });
        }
        semver::Version::parse(&self.version).map_err(|e| ManifestError::Validation {
            id: self.name.clone(),
            reason: format!("invalid semver in `version`: {e}"),
        })?;
        Ok(())
    }
}

// ─── JSON manifest (plugin.json — public plugin contract) ────────────────────

/// Plugin kind variants for `plugin.json`.
///
/// Defines the contract surface and sandbox resource profile.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub enum PluginKind {
    DataSource,
    ChatTool,
    Widget,
    Provider,
    Agent,
}

/// Filesystem permission scope.
///
/// `path` must be a relative path or an explicit token such as `"$HOME"`.
/// Absolute paths are rejected to prevent manifest authors from escaping the
/// plugin sandbox by declaring `/etc` as a read scope.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
pub struct FsPermission {
    /// Relative path scope (e.g. `"data/"` or `"$HOME/.config/cascade"`).
    pub path: String,
    /// Allow reading files inside `path`.
    #[serde(default)]
    pub read: bool,
    /// Allow writing files inside `path`.
    #[serde(default)]
    pub write: bool,
}

/// Network permission scope.
///
/// Declare each host the plugin may reach. Wildcards (`"*"`) are rejected.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
pub struct NetPermission {
    /// Allowed host (e.g. `"api.example.com"`). Wildcards are denied.
    pub host: String,
    /// Allowed ports (e.g. `[443, 8080]`). Empty list means all ports.
    #[serde(default)]
    pub ports: Vec<u16>,
}

/// Environment-variable permission scope.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
pub struct EnvPermission {
    /// Variable name the plugin may read (e.g. `"HOME"`).
    pub name: String,
}

/// Aggregated permissions declared in `plugin.json`.
///
/// All fields default to empty — DENY-by-default unless explicitly listed.
#[derive(Debug, Clone, Default, Serialize, Deserialize, JsonSchema)]
pub struct JsonPermissions {
    #[serde(default)]
    pub fs: Vec<FsPermission>,
    #[serde(default)]
    pub net: Vec<NetPermission>,
    #[serde(default)]
    pub env: Vec<EnvPermission>,
}

/// Parsed and validated `plugin.json` manifest.
///
/// This is the public plugin contract used by the plugin registry and PDK.
/// Every plugin distributed via the Cascade plugin ecosystem must include a
/// `plugin.json` file at the root of its plugin directory.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
pub struct PluginJsonManifest {
    /// Reverse-domain plugin identifier (e.g. `"com.example.my-plugin"`).
    ///
    /// Must match `^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`.
    pub id: String,

    /// Human-readable display name.
    pub name: String,

    /// Plugin version — must be a valid semver string (e.g. `"1.2.3"`).
    pub version: String,

    /// One-line description shown in the plugin browser.
    pub description: String,

    /// Plugin author (name or email).
    pub author: String,

    /// SPDX license identifier (e.g. `"MIT"`, `"Apache-2.0"`).
    pub license: String,

    /// Relative path from the plugin directory to the compiled WASM entry-point
    /// (e.g. `"plugin.wasm"`). Must not be an absolute path.
    pub entry_wasm: String,

    /// Plugin kind — determines the ABI surface and sandbox resource limits.
    pub kind: PluginKind,

    /// Permissions required by this plugin (DENY-by-default; empty = none).
    #[serde(default)]
    pub permissions: JsonPermissions,

    /// Minimum Cascade version required to run this plugin (semver requirement,
    /// e.g. `">=0.2.0"`).
    pub min_cascade_version: String,

    /// Exported WASM function names this plugin exposes (e.g. `["run", "describe"]`).
    #[serde(default)]
    pub exports: Vec<String>,

    /// List of capability tokens this plugin declares (subset of allowed vocabulary).
    ///
    /// Allowed tokens: `"fs_read"`, `"fs_write"`, `"net_outbound"`, `"env_read"`.
    #[serde(default)]
    pub capabilities: Vec<String>,
}

/// Capability tokens that plugins are permitted to declare.
const ALLOWED_CAPABILITIES: &[&str] = &["fs_read", "fs_write", "net_outbound", "env_read"];

/// Regex for reverse-domain plugin IDs.
///
/// Pattern: `^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`
fn is_valid_plugin_id(id: &str) -> bool {
    if id.is_empty() {
        return false;
    }
    let parts: Vec<&str> = id.split('.').collect();
    if parts.len() < 2 {
        return false; // need at least two components
    }
    parts.iter().all(|part| {
        !part.is_empty()
            && part
                .chars()
                .next()
                .map(|c| c.is_ascii_lowercase())
                .unwrap_or(false)
            && part
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_' || c == '-')
    })
}

impl PluginJsonManifest {
    /// Load, deserialise, and validate a `plugin.json` file.
    ///
    /// Returns an actionable `ManifestError` for each invalid field class.
    pub fn load(path: &Path) -> Result<Self, ManifestError> {
        let bytes = std::fs::read(path).map_err(|e| ManifestError::Io {
            path: path.to_owned(),
            reason: e.to_string(),
        })?;

        let manifest: PluginJsonManifest =
            serde_json::from_slice(&bytes).map_err(|e| ManifestError::JsonParse(e.to_string()))?;

        manifest.validate()?;
        Ok(manifest)
    }

    /// Parse from a JSON string (useful in tests and schema generation).
    pub fn from_json_str(json: &str) -> Result<Self, ManifestError> {
        let manifest: PluginJsonManifest =
            serde_json::from_str(json).map_err(|e| ManifestError::JsonParse(e.to_string()))?;
        manifest.validate()?;
        Ok(manifest)
    }

    fn validate(&self) -> Result<(), ManifestError> {
        // 1. id — reverse-domain format
        if !is_valid_plugin_id(&self.id) {
            return Err(ManifestError::InvalidId {
                id: self.id.clone(),
                reason: "id must be a reverse-domain string with at least two components, \
                         all lowercase (e.g. \"com.example.my-plugin\")"
                    .to_owned(),
            });
        }

        // 2. version — must parse as semver
        semver::Version::parse(&self.version).map_err(|e| ManifestError::InvalidVersion {
            id: self.id.clone(),
            reason: format!(
                "version field \"{}\" is not valid semver: {e}. \
                 Use a three-part version like \"1.0.0\".",
                self.version
            ),
        })?;

        // 3. entry_wasm — must not be absolute, and must not contain path-traversal
        //    components (`..`). A relative `../../etc/passwd` would escape the
        //    plugin directory sandbox when joined in the loader.
        let wasm_path = Path::new(&self.entry_wasm);
        if wasm_path.is_absolute() {
            return Err(ManifestError::AbsoluteWasmPath {
                id: self.id.clone(),
                path: self.entry_wasm.clone(),
                reason: "entry_wasm must be a relative path (e.g. \"plugin.wasm\"), \
                         not an absolute filesystem path"
                    .to_owned(),
            });
        }
        if wasm_path
            .components()
            .any(|c| c == std::path::Component::ParentDir)
        {
            return Err(ManifestError::PathTraversal {
                id: self.id.clone(),
                path: self.entry_wasm.clone(),
                reason: "entry_wasm must not contain `..` components — \
                         only a plain filename or sub-path is allowed (e.g. \"plugin.wasm\")"
                    .to_owned(),
            });
        }

        // 4. entry_wasm — must not be empty
        if self.entry_wasm.is_empty() {
            return Err(ManifestError::Validation {
                id: self.id.clone(),
                reason: "entry_wasm must not be empty; specify the relative path to the \
                         compiled WASM file (e.g. \"plugin.wasm\")"
                    .to_owned(),
            });
        }

        // 5. min_cascade_version — must parse as a semver VersionReq
        semver::VersionReq::parse(&self.min_cascade_version).map_err(|e| {
            ManifestError::InvalidMinCascadeVersion {
                id: self.id.clone(),
                reason: format!(
                    "min_cascade_version \"{}\" is not a valid semver requirement: {e}. \
                     Use a requirement like \">=0.2.0\".",
                    self.min_cascade_version
                ),
            }
        })?;

        // 6. capabilities — must be from allowed vocabulary (deny unknown tokens)
        for cap in &self.capabilities {
            if !ALLOWED_CAPABILITIES.contains(&cap.as_str()) {
                return Err(ManifestError::UnknownCapability {
                    id: self.id.clone(),
                    capability: cap.clone(),
                    allowed: ALLOWED_CAPABILITIES
                        .iter()
                        .map(|s| s.to_string())
                        .collect::<Vec<_>>()
                        .join(", "),
                });
            }
        }

        // 7. fs permissions — path must not be absolute and must not traverse with `..`
        for fs_perm in &self.permissions.fs {
            let fp = Path::new(&fs_perm.path);
            if fp.is_absolute() {
                return Err(ManifestError::Validation {
                    id: self.id.clone(),
                    reason: format!(
                        "fs permission path \"{}\" must be relative, not absolute. \
                         Use relative paths or environment variable tokens like \"$HOME\".",
                        fs_perm.path
                    ),
                });
            }
            if fp
                .components()
                .any(|c| c == std::path::Component::ParentDir)
            {
                return Err(ManifestError::PathTraversal {
                    id: self.id.clone(),
                    path: fs_perm.path.clone(),
                    reason: "fs permission path must not contain `..` components — \
                             declare explicit relative sub-paths only (e.g. \"data/\")"
                        .to_owned(),
                });
            }
        }

        // 8. net permissions — wildcards are denied
        for net_perm in &self.permissions.net {
            if net_perm.host.contains('*') {
                return Err(ManifestError::Validation {
                    id: self.id.clone(),
                    reason: format!(
                        "net permission host \"{}\" contains a wildcard which is not allowed. \
                         Declare each host explicitly.",
                        net_perm.host
                    ),
                });
            }
        }

        Ok(())
    }
}

// ─── Shared error type ────────────────────────────────────────────────────────

/// Errors from manifest parsing or validation (both TOML and JSON formats).
#[derive(Debug, Error)]
pub enum ManifestError {
    // TOML manifest errors
    #[error(
        "manifest is missing required field `schema_version`. \
         Add `schema_version = 1` to your cascade-plugin.toml."
    )]
    MissingSchemaVersion,

    #[error(
        "plugin '{name}' requires manifest schema version {required} but this \
         Cascade build supports up to version {supported}. \
         Update Cascade to load this plugin."
    )]
    SchemaVersionTooNew {
        name: String,
        required: u32,
        supported: u32,
    },

    #[error("TOML parse error in cascade-plugin.toml: {0}")]
    TomlParse(String),

    // JSON manifest errors
    #[error("JSON parse error in plugin.json: {0}")]
    JsonParse(String),

    #[error("plugin id \"{id}\" is not a valid reverse-domain identifier: {reason}")]
    InvalidId { id: String, reason: String },

    #[error("plugin \"{id}\" has an invalid version: {reason}")]
    InvalidVersion { id: String, reason: String },

    #[error("plugin \"{id}\" declares an absolute entry_wasm path \"{path}\": {reason}")]
    AbsoluteWasmPath {
        id: String,
        path: String,
        reason: String,
    },

    #[error("plugin \"{id}\" declares a path-traversal path \"{path}\": {reason}")]
    PathTraversal {
        id: String,
        path: String,
        reason: String,
    },

    #[error("plugin \"{id}\" has an invalid min_cascade_version: {reason}")]
    InvalidMinCascadeVersion { id: String, reason: String },

    #[error(
        "plugin \"{id}\" declares unknown capability \"{capability}\". \
         Allowed capabilities are: {allowed}"
    )]
    UnknownCapability {
        id: String,
        capability: String,
        allowed: String,
    },

    // Shared
    #[error("manifest validation error for plugin '{id}': {reason}")]
    Validation { id: String, reason: String },

    #[error("failed to read manifest at {path}: {reason}")]
    Io { path: PathBuf, reason: String },
}

// ─── JSON Schema generation helper ───────────────────────────────────────────

/// Generate and return the JSON Schema for `plugin.json` as a pretty-printed string.
pub fn generate_json_schema() -> String {
    let schema = schemars::schema_for!(PluginJsonManifest);
    serde_json::to_string_pretty(&schema).expect("schema serialization is infallible")
}

// ─── Tests ───────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use std::io::Write;

    use tempfile::NamedTempFile;

    use super::*;

    // ── helpers ──────────────────────────────────────────────────────────────

    fn valid_json() -> &'static str {
        r#"{
            "id": "com.example.my-plugin",
            "name": "My Plugin",
            "version": "1.0.0",
            "description": "A test plugin",
            "author": "Test Author",
            "license": "MIT",
            "entry_wasm": "plugin.wasm",
            "kind": "ChatTool",
            "min_cascade_version": ">=0.1.0",
            "exports": ["run", "describe"],
            "capabilities": ["fs_read"],
            "permissions": {
                "fs": [{"path": "data/", "read": true}],
                "net": [{"host": "api.example.com", "ports": [443]}],
                "env": [{"name": "HOME"}]
            }
        }"#
    }

    fn write_temp(json: &str) -> NamedTempFile {
        let mut f = NamedTempFile::new().unwrap();
        f.write_all(json.as_bytes()).unwrap();
        f
    }

    // ── valid manifest ───────────────────────────────────────────────────────

    #[test]
    fn valid_manifest_loads_correctly() {
        let f = write_temp(valid_json());
        let m = PluginJsonManifest::load(f.path()).unwrap();

        assert_eq!(m.id, "com.example.my-plugin");
        assert_eq!(m.name, "My Plugin");
        assert_eq!(m.version, "1.0.0");
        assert_eq!(m.kind, PluginKind::ChatTool);
        assert_eq!(m.entry_wasm, "plugin.wasm");
        assert_eq!(m.exports, vec!["run", "describe"]);
        assert_eq!(m.capabilities, vec!["fs_read"]);
        assert_eq!(m.permissions.fs.len(), 1);
        assert_eq!(m.permissions.net.len(), 1);
        assert_eq!(m.permissions.env.len(), 1);
        assert_eq!(m.permissions.fs[0].path, "data/");
        assert!(m.permissions.fs[0].read);
        assert_eq!(m.permissions.net[0].host, "api.example.com");
        assert_eq!(m.permissions.env[0].name, "HOME");
    }

    #[test]
    fn all_plugin_kinds_parse() {
        for (kind_str, expected) in &[
            ("DataSource", PluginKind::DataSource),
            ("ChatTool", PluginKind::ChatTool),
            ("Widget", PluginKind::Widget),
            ("Provider", PluginKind::Provider),
            ("Agent", PluginKind::Agent),
        ] {
            let json = format!(
                r#"{{"id":"com.ex.p","name":"x","version":"1.0.0","description":"x",
                "author":"a","license":"MIT","entry_wasm":"p.wasm","kind":"{}",
                "min_cascade_version":">=0.1.0"}}"#,
                kind_str
            );
            let m = PluginJsonManifest::from_json_str(&json).unwrap();
            assert_eq!(&m.kind, expected, "kind={kind_str}");
        }
    }

    // ── invalid id ───────────────────────────────────────────────────────────

    #[test]
    fn invalid_id_single_segment_rejected() {
        let json = r#"{
            "id": "myplugin",
            "name": "x","version":"1.0.0","description":"x","author":"a",
            "license":"MIT","entry_wasm":"p.wasm","kind":"Agent",
            "min_cascade_version":">=0.1.0"
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(
            matches!(err, ManifestError::InvalidId { .. }),
            "expected InvalidId, got: {err}"
        );
        let msg = err.to_string();
        assert!(
            msg.contains("reverse-domain"),
            "error should mention reverse-domain: {msg}"
        );
    }

    #[test]
    fn invalid_id_uppercase_rejected() {
        let json = r#"{
            "id": "com.Example.plugin",
            "name": "x","version":"1.0.0","description":"x","author":"a",
            "license":"MIT","entry_wasm":"p.wasm","kind":"Provider",
            "min_cascade_version":">=0.1.0"
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(matches!(err, ManifestError::InvalidId { .. }));
    }

    // ── invalid version ──────────────────────────────────────────────────────

    #[test]
    fn bad_semver_version_rejected() {
        let json = r#"{
            "id": "com.ex.plugin",
            "name": "x","version":"not-semver","description":"x","author":"a",
            "license":"MIT","entry_wasm":"p.wasm","kind":"DataSource",
            "min_cascade_version":">=0.1.0"
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(
            matches!(err, ManifestError::InvalidVersion { .. }),
            "expected InvalidVersion, got: {err}"
        );
        let msg = err.to_string();
        assert!(
            msg.contains("not valid semver"),
            "error should mention semver: {msg}"
        );
    }

    // ── absolute wasm path ───────────────────────────────────────────────────

    #[test]
    fn absolute_wasm_path_rejected() {
        let json = r#"{
            "id": "com.ex.plugin",
            "name": "x","version":"1.0.0","description":"x","author":"a",
            "license":"MIT","entry_wasm":"/usr/local/plugin.wasm","kind":"Widget",
            "min_cascade_version":">=0.1.0"
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(
            matches!(err, ManifestError::AbsoluteWasmPath { .. }),
            "expected AbsoluteWasmPath, got: {err}"
        );
        let msg = err.to_string();
        assert!(
            msg.contains("relative"),
            "error should mention relative path: {msg}"
        );
    }

    // ── missing required field ───────────────────────────────────────────────

    #[test]
    fn missing_required_field_rejected() {
        // Missing "author" field
        let json = r#"{
            "id": "com.ex.plugin",
            "name": "x","version":"1.0.0","description":"x",
            "license":"MIT","entry_wasm":"p.wasm","kind":"ChatTool",
            "min_cascade_version":">=0.1.0"
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(
            matches!(err, ManifestError::JsonParse(_)),
            "expected JsonParse for missing required field, got: {err}"
        );
        let msg = err.to_string();
        assert!(
            msg.to_lowercase().contains("author") || msg.contains("missing field"),
            "error should reference missing field: {msg}"
        );
    }

    // ── permission escalation (unknown capability) ───────────────────────────

    #[test]
    fn permission_escalation_rejected() {
        let json = r#"{
            "id": "com.ex.plugin",
            "name": "x","version":"1.0.0","description":"x","author":"a",
            "license":"MIT","entry_wasm":"p.wasm","kind":"Agent",
            "min_cascade_version":">=0.1.0",
            "capabilities": ["fs_read", "root_access"]
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(
            matches!(err, ManifestError::UnknownCapability { .. }),
            "expected UnknownCapability, got: {err}"
        );
        let msg = err.to_string();
        assert!(
            msg.contains("root_access"),
            "error should name the bad capability: {msg}"
        );
        assert!(
            msg.contains("Allowed capabilities"),
            "error should list allowed capabilities: {msg}"
        );
    }

    // ── net wildcard rejected ─────────────────────────────────────────────────

    #[test]
    fn net_wildcard_rejected() {
        let json = r#"{
            "id": "com.ex.plugin",
            "name": "x","version":"1.0.0","description":"x","author":"a",
            "license":"MIT","entry_wasm":"p.wasm","kind":"Provider",
            "min_cascade_version":">=0.1.0",
            "permissions": {"net": [{"host": "*.example.com"}]}
        }"#;
        let err = PluginJsonManifest::from_json_str(json).unwrap_err();
        assert!(
            matches!(err, ManifestError::Validation { .. }),
            "expected Validation for wildcard host, got: {err}"
        );
    }

    // ── JSON schema generates valid JSON ──────────────────────────────────────

    #[test]
    fn json_schema_generates_valid_json() {
        let schema_str = generate_json_schema();
        let parsed: serde_json::Value =
            serde_json::from_str(&schema_str).expect("schema must be valid JSON");
        // schemars always emits a top-level "$schema" key
        assert!(
            parsed.get("$schema").is_some() || parsed.get("title").is_some(),
            "generated schema must have $schema or title"
        );
    }

    // ── TOML manifest (legacy) tests ─────────────────────────────────────────

    const MINIMAL_TOML: &str = r#"
schema_version = 1
name = "my-chunker"
version = "1.0.0"
description = "Test chunker"
plugin_type = "chunker"
cascade_version = ">=0.1.0, <0.2.0"
"#;

    #[test]
    fn parse_valid_toml_manifest() {
        let manifest = PluginManifest::parse(MINIMAL_TOML.as_bytes()).unwrap();
        assert_eq!(manifest.name, "my-chunker");
        assert_eq!(manifest.schema_version, 1);
        assert_eq!(manifest.plugin_type, PluginType::Chunker);
    }

    #[test]
    fn toml_missing_schema_version() {
        let toml = r#"
name = "x"
version = "1.0.0"
description = "x"
plugin_type = "chunker"
cascade_version = ">=0.1.0"
"#;
        let err = PluginManifest::parse(toml.as_bytes()).unwrap_err();
        assert!(
            matches!(err, ManifestError::MissingSchemaVersion),
            "expected MissingSchemaVersion, got: {err:?}"
        );
    }

    #[test]
    fn toml_schema_version_too_new() {
        let toml = r#"
schema_version = 99
name = "future-plugin"
version = "1.0.0"
description = "x"
plugin_type = "chunker"
cascade_version = ">=0.1.0"
"#;
        let err = PluginManifest::parse(toml.as_bytes()).unwrap_err();
        let msg = err.to_string();
        assert!(
            msg.contains("Update Cascade"),
            "should suggest update: {msg}"
        );
    }
}
