//! Consent gate — install-time capability approval and personal-path enforcement.
//!
//! Purpose: block fs_read on personal paths unless PersonalData is granted; record
//!   requested capabilities as pending until the user explicitly grants them.
//! Inputs: plugin id, requested CapabilitySet, GrantStore.
//! Outputs: ConsentError on denied access; unit on success.
//! Constraints: personal paths include ~/.claude/, ~/.ssh/, ~/.gnupg/, ~/Documents/,
//!   ~/Desktop/, ~/.cascade/ — configurable via PERSONAL_PATHS const.
//! SPORT: cascade-plugins / consent gate (plug-01)

use std::path::{Path, PathBuf};

use thiserror::Error;

use crate::capability::Capability;
use crate::grants::GrantStore;

/// Paths considered personal — fs_read requires PersonalData grant.
const PERSONAL_PREFIXES: &[&str] = &[
    ".claude",
    ".ssh",
    ".gnupg",
    ".cascade",
    "Documents",
    "Desktop",
    "Library",
];

#[derive(Debug, Error)]
pub enum ConsentError {
    #[error(
        "plugin '{plugin}' requires capability '{cap}' which has not been granted by the user. \
         Run: cascade plugin grant {plugin} {cap}"
    )]
    CapabilityNotGranted { plugin: String, cap: String },

    #[error(
        "plugin '{plugin}' attempted to read personal path '{path}' without PersonalData grant. \
         Run: cascade plugin grant {plugin} personal_data"
    )]
    PersonalPathDenied { plugin: String, path: PathBuf },
}

/// Check that `plugin_id` has a user grant for `cap`.
///
/// Used at install-time and call-time for sensitive capabilities.
pub fn check_capability_granted(
    plugin_id: &str,
    cap: &Capability,
    store: &GrantStore,
) -> Result<(), ConsentError> {
    if store.is_granted(plugin_id, cap) {
        Ok(())
    } else {
        Err(ConsentError::CapabilityNotGranted {
            plugin: plugin_id.to_owned(),
            cap: cap_name(cap).to_owned(),
        })
    }
}

/// Check whether `path` is a personal path.
///
/// A path is personal if any component matches a PERSONAL_PREFIXES entry,
/// OR if the path starts with the user's home directory followed by one of those components.
pub fn is_personal_path(path: &Path) -> bool {
    let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("/home/user"));

    // Check if it's under home and its first sub-component is a personal prefix.
    if path.starts_with(&home) {
        if let Ok(rel) = path.strip_prefix(&home) {
            if let Some(first) = rel.components().next() {
                let s = first.as_os_str().to_string_lossy();
                return PERSONAL_PREFIXES.iter().any(|p| *p == s.as_ref());
            }
        }
    }

    // Also check any component by name (catch absolute paths like /Users/foo/.claude/).
    path.components().any(|c| {
        let s = c.as_os_str().to_string_lossy();
        PERSONAL_PREFIXES.iter().any(|p| *p == s.as_ref())
    })
}

/// Enforce that a fs_read on `path` is allowed for `plugin_id`.
///
/// Denied if `path` is personal AND the plugin does not hold a PersonalData grant.
pub fn check_fs_read_allowed(
    plugin_id: &str,
    path: &Path,
    store: &GrantStore,
) -> Result<(), ConsentError> {
    if is_personal_path(path) && !store.is_granted(plugin_id, &Capability::PersonalData) {
        return Err(ConsentError::PersonalPathDenied {
            plugin: plugin_id.to_owned(),
            path: path.to_owned(),
        });
    }
    Ok(())
}

fn cap_name(cap: &Capability) -> &'static str {
    match cap {
        Capability::FsRead       => "fs_read",
        Capability::FsWrite      => "fs_write",
        Capability::FsExec       => "fs_exec",
        Capability::NetOutbound  => "net_outbound",
        Capability::NetListen    => "net_listen",
        Capability::IpcCascade   => "ipc_cascade",
        Capability::PersonalData => "personal_data",
        Capability::McpInvoke    => "mcp_invoke",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;
    use crate::grants::GrantStore;

    fn store_with_grant(tmp: &TempDir, plugin: &str, cap: &Capability) -> GrantStore {
        let path = tmp.path().join("grants.json");
        let mut store = GrantStore::load_from(path).unwrap();
        store.grant(plugin, cap).unwrap();
        store
    }

    fn empty_store(tmp: &TempDir) -> GrantStore {
        let path = tmp.path().join("grants.json");
        GrantStore::load_from(path).unwrap()
    }

    #[test]
    fn fs_read_personal_path_without_grant_denied() {
        let tmp = TempDir::new().unwrap();
        let store = empty_store(&tmp);
        let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("/home/user"));
        let personal = home.join(".claude").join("CLAUDE.md");
        let result = check_fs_read_allowed("com.ex.p", &personal, &store);
        assert!(result.is_err(), "should deny personal path without grant");
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("PersonalData") || msg.contains("personal_data"), "error: {msg}");
    }

    #[test]
    fn fs_read_personal_path_with_grant_allowed() {
        let tmp = TempDir::new().unwrap();
        let store = store_with_grant(&tmp, "com.ex.p", &Capability::PersonalData);
        let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("/home/user"));
        let personal = home.join(".claude").join("CLAUDE.md");
        assert!(check_fs_read_allowed("com.ex.p", &personal, &store).is_ok());
    }

    #[test]
    fn fs_read_non_personal_path_always_allowed() {
        let tmp = TempDir::new().unwrap();
        let store = empty_store(&tmp);
        let path = std::path::PathBuf::from("/tmp/plugin-data/output.json");
        assert!(check_fs_read_allowed("com.ex.p", &path, &store).is_ok());
    }
}
