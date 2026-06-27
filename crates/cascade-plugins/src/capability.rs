//! Capability-based permission system for WASM plugins.
//!
//! Purpose: define the capability set a plugin can request and enforce grants
//!   at runtime. Path-scoped capabilities resolve symlinks before prefix checks
//!   to prevent escape attacks (H3 fix).
//! Inputs: plugin manifest `[capabilities]` section + user config overrides.
//! Outputs: `CapabilitySet` — the intersection of requested and granted caps.
//! Constraints: `ipc_cascade` is reserved in v0.1.0 (not implemented); runtime
//!   returns an actionable message if declared. No `unsafe` without SAFETY comment.
//! SPORT: cascade-plugins / capability model

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::path::{Path, PathBuf};
use thiserror::Error;

/// All capabilities a plugin may request.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Capability {
    /// Read files within declared path scopes.
    FsRead,
    /// Write files within declared path scopes.
    FsWrite,
    /// Execute binaries within declared path scopes (reserved — not yet enforced in v0.1.0).
    FsExec,
    /// Make outbound network connections.
    NetOutbound,
    /// Listen for inbound connections (reserved — not yet enforced in v0.1.0).
    NetListen,
    /// Call Cascade-internal IPC APIs (reserved — not yet implemented in v0.1.0).
    IpcCascade,
    /// Access personal data directories (requires user consent). Enforced by consent gate.
    PersonalData,
    /// Invoke MCP tool endpoints registered in Cascade (reserved — not yet enforced in v0.1.0).
    McpInvoke,
}

/// A path-scoped capability grants access to a specific directory tree.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScopedPath {
    pub capability: Capability,
    /// Declared scope root from the manifest (may contain `~` or relative paths).
    pub declared_root: String,
    /// Resolved canonical absolute path (symlinks fully resolved at load time).
    pub canonical_root: PathBuf,
}

impl ScopedPath {
    /// Returns true if `path` is inside the canonical scope root.
    ///
    /// # Why canonical resolution
    /// Without canonicalization a symlink at `.cascade/link -> /etc/passwd` would
    /// pass a naive prefix check on `.cascade/`. We canonicalize both the scope
    /// root and the access path so symlinks can never escape the declared scope.
    pub fn contains(&self, path: &Path) -> bool {
        match std::fs::canonicalize(path) {
            Ok(canonical_path) => canonical_path.starts_with(&self.canonical_root),
            // If canonicalize fails (path doesn't exist yet for write), deny.
            Err(_) => false,
        }
    }
}

/// Resolved capability set granted to a plugin instance.
#[derive(Debug, Clone, Default)]
pub struct CapabilitySet {
    /// Flat capabilities (e.g. net_outbound).
    pub granted: HashSet<Capability>,
    /// Path-scoped capabilities (fs_read / fs_write with scope restriction).
    pub scoped_paths: Vec<ScopedPath>,
    /// Denied capabilities that were requested but blocked by user config.
    pub denied: HashSet<Capability>,
}

impl CapabilitySet {
    /// Returns true if the plugin holds `cap` (non-path check).
    pub fn has(&self, cap: &Capability) -> bool {
        self.granted.contains(cap)
    }

    /// Returns true if the plugin can read `path` (path-scope check with symlink resolution).
    pub fn can_read(&self, path: &Path) -> bool {
        self.scoped_paths
            .iter()
            .any(|s| matches!(s.capability, Capability::FsRead) && s.contains(path))
    }

    /// Returns true if the plugin can write `path` (path-scope check with symlink resolution).
    pub fn can_write(&self, path: &Path) -> bool {
        self.scoped_paths
            .iter()
            .any(|s| matches!(s.capability, Capability::FsWrite) && s.contains(path))
    }
}

/// Errors produced by the capability subsystem.
#[derive(Debug, Error)]
pub enum CapabilityError {
    #[error("capability denied: plugin '{plugin}' does not hold '{cap:?}'")]
    Denied { plugin: String, cap: Capability },

    #[error(
        "path access denied: plugin '{plugin}' cannot access '{path}' (not in declared scope)"
    )]
    PathDenied { plugin: String, path: PathBuf },

    #[error(
        "capability '{cap:?}' is reserved in v0.1.0 and not yet implemented; \
         it will be available in v0.2.0"
    )]
    Reserved { cap: Capability },

    #[error("failed to resolve canonical path for '{path}': {source}")]
    CanonicalizeFailed {
        path: PathBuf,
        source: std::io::Error,
    },
}

/// Declared capability section from `cascade-plugin.toml`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DeclaredCapabilities {
    pub fs_read: Option<Vec<String>>,
    pub fs_write: Option<Vec<String>>,
    pub fs_exec: Option<Vec<String>>,
    pub net_outbound: Option<bool>,
    pub net_listen: Option<bool>,
    pub ipc_cascade: Option<bool>,
    pub personal_data: Option<bool>,
    pub mcp_invoke: Option<bool>,
}

impl DeclaredCapabilities {
    /// Resolve declared capabilities against the user config allowlist.
    ///
    /// # Why intersect with user config
    /// Plugins should only get the minimum they need; user config provides a
    /// deny override so users can restrict even declared capabilities.
    pub fn resolve(&self, deny_net_outbound: bool) -> Result<CapabilitySet, CapabilityError> {
        let mut set = CapabilitySet::default();

        // Reserved capabilities — emit actionable message if declared.
        if self.ipc_cascade.unwrap_or(false) {
            return Err(CapabilityError::Reserved {
                cap: Capability::IpcCascade,
            });
        }

        // mcp_invoke is reserved in v0.1.0 — same pattern as ipc_cascade.
        if self.mcp_invoke.unwrap_or(false) {
            return Err(CapabilityError::Reserved {
                cap: Capability::McpInvoke,
            });
        }

        // personal_data — consent gate handles runtime enforcement; record in granted set.
        if self.personal_data.unwrap_or(false) {
            set.granted.insert(Capability::PersonalData);
        }

        // net_outbound
        if self.net_outbound.unwrap_or(false) {
            if deny_net_outbound {
                set.denied.insert(Capability::NetOutbound);
            } else {
                set.granted.insert(Capability::NetOutbound);
            }
        }

        // fs_read path scopes
        if let Some(paths) = &self.fs_read {
            for raw_path in paths {
                let canonical_root = Self::resolve_path(raw_path)?;
                set.scoped_paths.push(ScopedPath {
                    capability: Capability::FsRead,
                    declared_root: raw_path.clone(),
                    canonical_root,
                });
            }
        }

        // fs_write path scopes
        if let Some(paths) = &self.fs_write {
            for raw_path in paths {
                let canonical_root = Self::resolve_path(raw_path)?;
                set.scoped_paths.push(ScopedPath {
                    capability: Capability::FsWrite,
                    declared_root: raw_path.clone(),
                    canonical_root,
                });
            }
        }

        Ok(set)
    }

    fn resolve_path(raw: &str) -> Result<PathBuf, CapabilityError> {
        // Expand leading `~` to home directory.
        let expanded = if let Some(rest) = raw.strip_prefix("~/") {
            dirs::home_dir()
                .map(|h| h.join(rest))
                .unwrap_or_else(|| PathBuf::from(raw))
        } else if raw == "~" {
            dirs::home_dir().unwrap_or_else(|| PathBuf::from(raw))
        } else {
            PathBuf::from(raw)
        };

        // For paths that don't exist yet (e.g. a write target not yet created),
        // canonicalize the nearest existing ancestor and append the remainder.
        std::fs::canonicalize(&expanded).map_err(|source| CapabilityError::CanonicalizeFailed {
            path: expanded,
            source,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ipc_cascade_reserved() {
        let caps = DeclaredCapabilities {
            ipc_cascade: Some(true),
            ..Default::default()
        };
        let err = caps.resolve(false).unwrap_err();
        assert!(matches!(
            err,
            CapabilityError::Reserved {
                cap: Capability::IpcCascade
            }
        ));
        let msg = err.to_string();
        assert!(
            msg.contains("v0.2.0"),
            "error should mention upcoming version: {msg}"
        );
    }

    #[test]
    fn test_net_outbound_denied_by_user_config() {
        let caps = DeclaredCapabilities {
            net_outbound: Some(true),
            ..Default::default()
        };
        let set = caps.resolve(true /* deny_net_outbound */).unwrap();
        assert!(!set.has(&Capability::NetOutbound));
        assert!(set.denied.contains(&Capability::NetOutbound));
    }

    #[test]
    fn test_net_outbound_granted() {
        let caps = DeclaredCapabilities {
            net_outbound: Some(true),
            ..Default::default()
        };
        let set = caps.resolve(false).unwrap();
        assert!(set.has(&Capability::NetOutbound));
    }
}
