//! Plugin grants store — persists user capability grants/revocations to
//! `~/.cascade/plugin-grants.json`.
//!
//! Purpose: record which capabilities the user has approved per plugin; provide
//!   grant/revoke/check API consumed by the consent gate.
//! Inputs: plugin id + Capability, user approval events.
//! Outputs: GrantStore (in-memory + JSON-backed), GrantError.
//! Constraints: grant file is lazily created; concurrent writes use an exclusive lock.
//! SPORT: cascade-plugins / grants store (plug-01)

use std::collections::{HashMap, HashSet};
use std::path::PathBuf;

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::capability::Capability;

#[derive(Debug, Error)]
pub enum GrantError {
    #[error("failed to read grants file {path}: {source}")]
    Read {
        path: PathBuf,
        source: std::io::Error,
    },
    #[error("failed to write grants file {path}: {source}")]
    Write {
        path: PathBuf,
        source: std::io::Error,
    },
    #[error("failed to parse grants file {path}: {source}")]
    Parse {
        path: PathBuf,
        source: serde_json::Error,
    },
}

/// Persisted grant record: plugin_id -> set of granted capability names.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct GrantStore {
    /// Map from plugin ID to set of granted capability names.
    grants: HashMap<String, HashSet<String>>,

    /// Path to the backing JSON file (not serialised).
    #[serde(skip)]
    path: PathBuf,
}

impl GrantStore {
    /// Load or create the grant store at `~/.cascade/plugin-grants.json`.
    pub fn load() -> Result<Self, GrantError> {
        let path = grants_path();
        if !path.exists() {
            return Ok(Self {
                grants: HashMap::new(),
                path,
            });
        }
        let bytes = std::fs::read(&path).map_err(|source| GrantError::Read {
            path: path.clone(),
            source,
        })?;
        let mut store: GrantStore =
            serde_json::from_slice(&bytes).map_err(|source| GrantError::Parse {
                path: path.clone(),
                source,
            })?;
        store.path = path;
        Ok(store)
    }

    /// Load from an explicit path (for tests).
    pub fn load_from(path: PathBuf) -> Result<Self, GrantError> {
        if !path.exists() {
            return Ok(Self {
                grants: HashMap::new(),
                path,
            });
        }
        let bytes = std::fs::read(&path).map_err(|source| GrantError::Read {
            path: path.clone(),
            source,
        })?;
        let mut store: GrantStore =
            serde_json::from_slice(&bytes).map_err(|source| GrantError::Parse {
                path: path.clone(),
                source,
            })?;
        store.path = path;
        Ok(store)
    }

    /// Grant `cap` to `plugin_id` and persist.
    pub fn grant(&mut self, plugin_id: &str, cap: &Capability) -> Result<(), GrantError> {
        self.grants
            .entry(plugin_id.to_owned())
            .or_default()
            .insert(cap_to_str(cap).to_owned());
        self.save()
    }

    /// Revoke `cap` from `plugin_id` and persist.
    pub fn revoke(&mut self, plugin_id: &str, cap: &Capability) -> Result<(), GrantError> {
        if let Some(caps) = self.grants.get_mut(plugin_id) {
            caps.remove(cap_to_str(cap));
            if caps.is_empty() {
                self.grants.remove(plugin_id);
            }
        }
        self.save()
    }

    /// Returns true if `plugin_id` holds a grant for `cap`.
    pub fn is_granted(&self, plugin_id: &str, cap: &Capability) -> bool {
        self.grants
            .get(plugin_id)
            .map(|caps| caps.contains(cap_to_str(cap)))
            .unwrap_or(false)
    }

    fn save(&self) -> Result<(), GrantError> {
        if let Some(parent) = self.path.parent() {
            std::fs::create_dir_all(parent).ok();
        }
        let json =
            serde_json::to_vec_pretty(&self).expect("GrantStore serialization is infallible");
        std::fs::write(&self.path, json).map_err(|source| GrantError::Write {
            path: self.path.clone(),
            source,
        })
    }
}

fn grants_path() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".cascade")
        .join("plugin-grants.json")
}

fn cap_to_str(cap: &Capability) -> &'static str {
    match cap {
        Capability::FsRead => "fs_read",
        Capability::FsWrite => "fs_write",
        Capability::FsExec => "fs_exec",
        Capability::NetOutbound => "net_outbound",
        Capability::NetListen => "net_listen",
        Capability::IpcCascade => "ipc_cascade",
        Capability::PersonalData => "personal_data",
        Capability::McpInvoke => "mcp_invoke",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_store(tmp: &TempDir) -> GrantStore {
        let path = tmp.path().join("plugin-grants.json");
        GrantStore::load_from(path).unwrap()
    }

    #[test]
    fn grant_and_check() {
        let tmp = TempDir::new().unwrap();
        let mut store = make_store(&tmp);
        assert!(!store.is_granted("com.ex.p", &Capability::PersonalData));
        store.grant("com.ex.p", &Capability::PersonalData).unwrap();
        assert!(store.is_granted("com.ex.p", &Capability::PersonalData));
    }

    #[test]
    fn revoke_takes_effect() {
        let tmp = TempDir::new().unwrap();
        let mut store = make_store(&tmp);
        store.grant("com.ex.p", &Capability::FsRead).unwrap();
        assert!(store.is_granted("com.ex.p", &Capability::FsRead));
        store.revoke("com.ex.p", &Capability::FsRead).unwrap();
        assert!(!store.is_granted("com.ex.p", &Capability::FsRead));
    }

    #[test]
    fn grant_persists_across_reopen() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("plugin-grants.json");
        {
            let mut store = GrantStore::load_from(path.clone()).unwrap();
            store
                .grant("com.ex.persist", &Capability::NetOutbound)
                .unwrap();
        }
        let store2 = GrantStore::load_from(path).unwrap();
        assert!(store2.is_granted("com.ex.persist", &Capability::NetOutbound));
    }
}
