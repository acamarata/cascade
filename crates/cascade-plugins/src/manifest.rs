//! Plugin manifest — `cascade-plugin.toml` schema and schema-version negotiation.
//!
//! Purpose: parse and validate the plugin manifest file, negotiate schema version
//!   with the host's supported range, and emit actionable errors on mismatch.
//! Inputs: raw TOML bytes from `cascade-plugin.toml`.
//! Outputs: `PluginManifest` struct or `ManifestError` with an actionable message.
//! Constraints: `schema_version` is required; forward-compat policy is additive only
//!   (new optional fields are backward-compatible; removals/renames require a bump).
//! SPORT: cascade-plugins / manifest layer

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::capability::DeclaredCapabilities;

/// Maximum schema version this build of Cascade supports.
pub const HOST_MAX_SCHEMA_VERSION: u32 = 1;

/// Plugin type — determines sandbox resource limits and ABI entry points.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
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

/// Parsed `cascade-plugin.toml` manifest.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginManifest {
    /// Schema version for this manifest format.
    ///
    /// # Forward-compat policy
    /// - Adding optional fields: backward-compatible, no version bump needed.
    /// - Removing, renaming, or changing types of fields: requires incrementing this value.
    /// - Host reads this field first; mismatches produce an actionable error message.
    pub schema_version: u32,

    /// Plugin identifier — must be a valid Rust crate name (lowercase, hyphens).
    pub name: String,

    /// Semantic version string (e.g. "1.0.0").
    pub version: String,

    /// Human-readable description.
    pub description: String,

    /// The plugin type determines the ABI entry points and sandbox resource profile.
    pub plugin_type: PluginType,

    /// Semver range of Cascade host versions this plugin is compatible with.
    /// Example: ">=0.1.0, <0.2.0"
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

/// Errors from manifest parsing or schema negotiation.
#[derive(Debug, Error)]
pub enum ManifestError {
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

    #[error("manifest validation error for plugin '{name}': {reason}")]
    Validation { name: String, reason: String },
}

impl PluginManifest {
    /// Parse and validate a `cascade-plugin.toml` manifest.
    ///
    /// # Version negotiation
    /// 1. Deserialize TOML — fail fast with actionable message on missing fields.
    /// 2. Check `schema_version` against `HOST_MAX_SCHEMA_VERSION`.
    /// 3. Validate required fields (name format, semver syntax, etc.).
    pub fn parse(toml_bytes: &[u8]) -> Result<Self, ManifestError> {
        // Step 1: deserialize. Use a partial struct first to extract schema_version
        // before full deserialization, so the version error comes before field errors.
        let raw: toml::Value = toml::from_str(std::str::from_utf8(toml_bytes).unwrap_or_default())
            .map_err(|e| ManifestError::TomlParse(e.to_string()))?;

        let version_val = raw
            .get("schema_version")
            .ok_or(ManifestError::MissingSchemaVersion)?;

        let schema_version = version_val
            .as_integer()
            .ok_or(ManifestError::MissingSchemaVersion)? as u32;

        // Step 2: schema version negotiation.
        if schema_version > HOST_MAX_SCHEMA_VERSION {
            // Extract name for a better error message, or use placeholder.
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

        // Step 3: full deserialization.
        let manifest: PluginManifest =
            toml::from_str(std::str::from_utf8(toml_bytes).unwrap_or_default())
                .map_err(|e| ManifestError::TomlParse(e.to_string()))?;

        manifest.validate()?;
        Ok(manifest)
    }

    fn validate(&self) -> Result<(), ManifestError> {
        // Name must be non-empty and follow crate naming conventions.
        if self.name.is_empty() {
            return Err(ManifestError::Validation {
                name: "<empty>".to_owned(),
                reason: "plugin name must not be empty".to_owned(),
            });
        }
        if self.name.contains(' ') {
            return Err(ManifestError::Validation {
                name: self.name.clone(),
                reason: "plugin name must not contain spaces; use hyphens".to_owned(),
            });
        }
        // Version must be parseable as semver.
        semver::Version::parse(&self.version).map_err(|e| ManifestError::Validation {
            name: self.name.clone(),
            reason: format!("invalid semver in `version`: {e}"),
        })?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const MINIMAL_MANIFEST: &str = r#"
schema_version = 1
name = "my-chunker"
version = "1.0.0"
description = "Test chunker"
plugin_type = "chunker"
cascade_version = ">=0.1.0, <0.2.0"
"#;

    #[test]
    fn parse_valid_manifest() {
        let manifest = PluginManifest::parse(MINIMAL_MANIFEST.as_bytes()).unwrap();
        assert_eq!(manifest.name, "my-chunker");
        assert_eq!(manifest.schema_version, 1);
        assert_eq!(manifest.plugin_type, PluginType::Chunker);
    }

    #[test]
    fn missing_schema_version() {
        let toml = r#"name = "x" version = "1.0.0" description = "x" plugin_type = "chunker" cascade_version = ">=0.1.0""#;
        let err = PluginManifest::parse(toml.as_bytes()).unwrap_err();
        assert!(matches!(err, ManifestError::MissingSchemaVersion));
    }

    #[test]
    fn schema_version_too_new() {
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
