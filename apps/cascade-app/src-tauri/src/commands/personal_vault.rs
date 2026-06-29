//! Tauri command wrappers for the encrypted personal vault (`cascade-personal`).
//!
//! # Purpose
//! Expose the `cascade-personal` vault API to the desktop app. The vault holds
//! an AES-256-GCM-encrypted SQLite store keyed by a DEK in the OS keychain;
//! these commands let the Personal UI list collections, query/add records,
//! request consent, and read the exposure log.
//!
//! # Design
//! `PersonalVault` wraps a non-`Sync` SQLite connection, so each command opens
//! the vault fresh inside `spawn_blocking` and drops it on return — no shared
//! app state, no holding the connection across an await point. This mirrors the
//! per-call pattern used by the accounts commands.
//!
//! # Constraints
//! - All commands return `Result<T, String>` (the app-wide Tauri convention).
//! - Mode strings map `"personal"` → `CascadeMode::Personal`, everything else
//!   → `CascadeMode::Normal` (the restrictive default).
//!
//! # SPORT
//! MASTER-COMMANDS.md — personal vault IPC commands (rag-16-ui)

use std::path::PathBuf;

use cascade_personal::{
    exposure_log, list_collections, open_vault, query_records, request_consent, upsert_record,
    CascadeMode, ExposureEntry, PersonalVault, VaultRecord,
};
use serde::{Deserialize, Serialize};

/// A collection descriptor returned to the frontend.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CollectionInfo {
    /// Collection identifier (the storage key).
    pub name: String,
    /// Human-readable label.
    pub label: String,
    /// Sensitivity tier (e.g. `"normal"`, `"restricted"`).
    pub sensitivity: String,
}

/// Resolve the personal vault database path (`~/.cascade/personal.db`).
fn personal_db_path() -> PathBuf {
    cascade_types::paths::global_cascade_dir().join("personal.db")
}

/// Open the vault fresh against the OS keychain. Caller runs this inside
/// `spawn_blocking` (it does synchronous SQLite + keychain I/O).
fn open() -> Result<PersonalVault, String> {
    let kc = cascade_keychain::platform_keychain();
    open_vault(personal_db_path(), kc.as_ref()).map_err(|e| e.to_string())
}

/// Parse a frontend mode string into a [`CascadeMode`] (defaults to `Normal`).
fn parse_mode(mode: &str) -> CascadeMode {
    match mode {
        "personal" => CascadeMode::Personal,
        _ => CascadeMode::Normal,
    }
}

/// Open (or create) the vault. Returns the resolved db path on success.
#[tauri::command]
pub async fn open_vault_cmd() -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(|| {
        let _vault = open()?;
        Ok::<String, String>(personal_db_path().display().to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// List all collections in the vault.
#[tauri::command]
pub async fn list_collections_cmd() -> Result<Vec<CollectionInfo>, String> {
    tauri::async_runtime::spawn_blocking(|| {
        let vault = open()?;
        let cols = list_collections(&vault).map_err(|e| e.to_string())?;
        Ok::<Vec<CollectionInfo>, String>(
            cols.into_iter()
                .map(|(name, label, sensitivity)| CollectionInfo {
                    name,
                    label,
                    sensitivity,
                })
                .collect(),
        )
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Query the records of a collection under the given mode.
#[tauri::command]
pub async fn query_records_cmd(
    collection: String,
    mode: String,
) -> Result<Vec<VaultRecord>, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let vault = open()?;
        query_records(&vault, &collection, parse_mode(&mode)).map_err(|e| e.to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Insert or update a record; returns the record id.
#[tauri::command]
pub async fn upsert_record_cmd(
    collection: String,
    id: Option<String>,
    payload: serde_json::Value,
) -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let vault = open()?;
        upsert_record(&vault, &collection, id.as_deref(), &payload).map_err(|e| e.to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Record a consent grant for exposing a collection (or one item) to an audience.
#[tauri::command]
pub async fn request_consent_cmd(
    collection: String,
    item_id: Option<String>,
    exposed_to: String,
    mode: String,
) -> Result<bool, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let vault = open()?;
        request_consent(
            &vault,
            &collection,
            item_id.as_deref(),
            &exposed_to,
            parse_mode(&mode),
        )
        .map_err(|e| e.to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Read the exposure log for a collection.
#[tauri::command]
pub async fn exposure_log_cmd(collection: String) -> Result<Vec<ExposureEntry>, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let vault = open()?;
        exposure_log(&vault, &collection).map_err(|e| e.to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_mode_maps_personal_and_defaults_normal() {
        assert!(matches!(parse_mode("personal"), CascadeMode::Personal));
        assert!(matches!(parse_mode("normal"), CascadeMode::Normal));
        assert!(matches!(parse_mode("garbage"), CascadeMode::Normal));
    }

    #[test]
    fn db_path_is_under_cascade_dir() {
        let p = personal_db_path();
        assert!(p.ends_with("personal.db"));
    }
}
