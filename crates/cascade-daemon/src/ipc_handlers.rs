//! IPC handlers for worktree management and providers auto-detection commands.
//!
//! Implements AddWorktree, RemoveWorktree, ListWorktrees, and RescanProviders handlers.
//! WorktreeStore is backed by ~/.cascade/worktrees.json.
//! ProvidersStore is backed by ~/.cascade/providers.json.

use chrono::Utc;
use std::path::PathBuf;
use std::process::Command;
use tracing::{debug, info, warn};
use uuid::Uuid;

use cascade_core::auth_detector::detect_harness_accounts;
use cascade_core::providers_store::{
    merge_providers, read_providers_store, write_providers_store, ProvidersStore,
};
use cascade_core::quota_store::{read_quota_store, QuotaStore};
use cascade_core::routing_table::{ProviderMetricsSnapshot, RoutingTable};
use cascade_core::worktree_store::{read_worktree_store, write_worktree_store, WorktreeEntry};

/// Error type for IPC handler operations.
#[derive(Debug)]
pub enum IpcHandlerError {
    /// I/O error reading/writing the store.
    StoreIo(std::io::Error),
    /// Git subprocess failed with a non-zero exit code.
    GitError(String),
    /// Worktree entry not found.
    NotFound(String),
}

impl std::fmt::Display for IpcHandlerError {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        match self {
            Self::StoreIo(e) => write!(f, "store I/O error: {}", e),
            Self::GitError(msg) => write!(f, "git error: {}", msg),
            Self::NotFound(id) => write!(f, "worktree not found: {}", id),
        }
    }
}

/// Add a new worktree entry to the registry.
///
/// If `create_worktree` is true, spawns `git -C {repo_root} worktree add {worktree_path} {branch}`.
/// Returns the new entry ID.
pub async fn handle_add_worktree(
    repo_root: String,
    branch: String,
    worktree_path: String,
    harness: String,
    session_id: String,
    create_worktree: bool,
) -> Result<String, IpcHandlerError> {
    // If requested, create the git worktree via subprocess.
    if create_worktree {
        let output = Command::new("git")
            .arg("-C")
            .arg(&repo_root)
            .arg("worktree")
            .arg("add")
            .arg(&worktree_path)
            .arg(&branch)
            .output()
            .map_err(|e| IpcHandlerError::GitError(format!("failed to spawn git: {}", e)))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(IpcHandlerError::GitError(format!(
                "git worktree add failed: {}",
                stderr
            )));
        }
        debug!(repo_root = %repo_root, branch = %branch, worktree_path = %worktree_path, "git worktree add succeeded");
    }

    // Build the WorktreeEntry.
    let id = Uuid::new_v4().to_string();
    let entry = WorktreeEntry {
        id: id.clone(),
        repo_root: PathBuf::from(&repo_root),
        branch: branch.clone(),
        worktree_path: PathBuf::from(&worktree_path),
        harness: harness.clone(),
        session_id: session_id.clone(),
        created_at: Utc::now().to_rfc3339(),
        stale: false,
    };

    // Load store, add entry, and write back.
    let mut store = read_worktree_store().map_err(IpcHandlerError::StoreIo)?;
    store.add_worktree(entry);
    write_worktree_store(&store).map_err(IpcHandlerError::StoreIo)?;

    debug!(id = %id, repo_root = %repo_root, harness = %harness, "worktree entry added");
    Ok(id)
}

/// Remove a worktree entry from the registry.
///
/// If `remove_from_disk` is true, spawns `git worktree remove --force {worktree_path}`.
/// Returns the removed entry's ID if found, or NotFound error if the ID doesn't exist.
pub async fn handle_remove_worktree(
    id: String,
    remove_from_disk: bool,
) -> Result<String, IpcHandlerError> {
    // Load store and find the entry.
    let mut store = read_worktree_store().map_err(IpcHandlerError::StoreIo)?;

    let entry = store
        .worktrees
        .iter()
        .find(|e| e.id == id)
        .ok_or_else(|| IpcHandlerError::NotFound(id.clone()))?
        .clone();

    // If requested, remove the git worktree via subprocess.
    if remove_from_disk {
        let output = Command::new("git")
            .arg("worktree")
            .arg("remove")
            .arg("--force")
            .arg(&entry.worktree_path)
            .output()
            .map_err(|e| IpcHandlerError::GitError(format!("failed to spawn git: {}", e)))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            warn!(worktree_path = ?entry.worktree_path, stderr = %stderr, "git worktree remove failed");
            // Don't fail the IPC call; just log and proceed with removal from store.
        } else {
            debug!(worktree_path = ?entry.worktree_path, "git worktree remove succeeded");
        }
    }

    // Remove from store and write back.
    store.remove_worktree(&id);
    write_worktree_store(&store).map_err(IpcHandlerError::StoreIo)?;

    debug!(id = %id, "worktree entry removed");
    Ok(id)
}

/// List all registered worktrees.
///
/// Calls scan_stale first to mark any stale entries, then returns the list.
pub async fn handle_list_worktrees() -> Result<Vec<WorktreeEntry>, IpcHandlerError> {
    // Load store, scan for stale entries, and write back if any were marked.
    let mut store = read_worktree_store().map_err(IpcHandlerError::StoreIo)?;

    let stale_ids = store.scan_stale();
    if !stale_ids.is_empty() {
        write_worktree_store(&store).map_err(IpcHandlerError::StoreIo)?;
        for id in stale_ids {
            debug!(id = %id, "stale worktree detected");
        }
    }

    Ok(store.worktrees)
}

/// Re-scan for auto-detected harness accounts and update providers.json.
///
/// This handler is called by the onboarding wizard, CLI, or widgets to trigger
/// a fresh scan for newly-installed harnesses or updated credentials without
/// restarting the daemon. It reads the current providers store, re-detects harness
/// accounts from well-known config paths, merges them (preserving manual entries),
/// and writes back to ~/.cascade/providers.json.
///
/// Returns the count of providers after merge.
pub async fn handle_rescan_providers() -> Result<usize, IpcHandlerError> {
    // Get home directory: try HOME env var, then use dirs::home_dir()
    let home_dir = std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(dirs::home_dir)
        .ok_or_else(|| {
            IpcHandlerError::StoreIo(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                "no home directory found",
            ))
        })?;

    let config_dir = home_dir.join(".cascade");
    let providers_path = config_dir.join("providers.json");

    // Detect harness accounts
    let detected = detect_harness_accounts(&home_dir);
    info!(count = detected.len(), "harness accounts re-scanned");

    // Load existing store or create empty
    let mut store = read_providers_store(&providers_path).unwrap_or_else(|_| ProvidersStore {
        schema_version: cascade_core::providers_store::PROVIDERS_STORE_SCHEMA_VERSION,
        updated_at: Utc::now().to_rfc3339(),
        providers: Vec::new(),
    });

    // Merge detected accounts into existing store (preserves manual entries)
    merge_providers(&mut store.providers, &detected);
    store.updated_at = Utc::now().to_rfc3339();

    // Get count before writing
    let count = store.providers.len();

    // Write to disk
    write_providers_store(&providers_path, &store).map_err(|e| {
        IpcHandlerError::StoreIo(std::io::Error::other(format!(
            "failed to write providers store: {:?}",
            e
        )))
    })?;

    debug!(providers_path = %providers_path.display(), count = count, "providers store written after rescan");
    Ok(count)
}

/// Read the persistent quota store from disk.
///
/// Returns the `QuotaStore` containing aggregated quota snapshots across accounts.
/// If the file does not exist, returns `NotFound` error.
pub async fn handle_read_quota_store() -> Result<QuotaStore, IpcHandlerError> {
    let home_dir = dirs::home_dir().unwrap_or_else(|| PathBuf::from("/tmp"));
    let quota_store_path = home_dir.join(".cascade/quota-store.json");
    read_quota_store(&quota_store_path).map_err(|e| {
        // CascadeError doesn't have a kind() method like io::Error;
        // map all variants to a generic Other io::Error.
        IpcHandlerError::StoreIo(std::io::Error::other(format!(
            "failed to read quota store: {:?}",
            e
        )))
    })
}

/// Read proxy metrics for all provider slots.
///
/// Locks the routing table, iterates over all slots, computes p50/p95 latencies,
/// and returns a Vec<ProviderMetricsSnapshot> for the CLI and dashboard.
///
/// Takes an Arc<Mutex<RoutingTable>> as a parameter (passed from the proxy state).
pub fn handle_read_proxy_metrics(
    table: &std::sync::Arc<std::sync::Mutex<RoutingTable>>,
) -> Result<Vec<ProviderMetricsSnapshot>, IpcHandlerError> {
    let guard = table.lock().unwrap();

    let snapshots: Vec<ProviderMetricsSnapshot> = guard
        .slots()
        .iter()
        .map(|slot| {
            let p50 = cascade_core::routing_table::p50(slot);
            let p95 = cascade_core::routing_table::p95(slot);

            ProviderMetricsSnapshot {
                slot_id: slot.id.clone(),
                display_name: slot.display_name.clone(),
                total_requests: slot.request_count,
                error_count: slot.error_count,
                rate_limit_count: slot.rate_limit_count,
                p50_ms: p50,
                p95_ms: p95,
                enabled: slot.enabled,
            }
        })
        .collect();

    debug!(
        "read_proxy_metrics: returning {} metric snapshots",
        snapshots.len()
    );
    Ok(snapshots)
}

/// Read harness status snapshots from the in-memory cache (IPC endpoint).
///
/// Returns a Vec<HarnessStatus> without spawning pgrep processes (reads from
/// cache written by HarnessMonitor background task).
///
/// Takes the HarnessCache as a parameter (Arc<RwLock<Vec<HarnessStatus>>>).
pub async fn handle_harness_status(
    cache: &crate::harness_bridge::HarnessCache,
) -> Result<Vec<crate::harness_bridge::HarnessStatus>, IpcHandlerError> {
    let guard = cache.read().await;
    let statuses = guard.clone();
    debug!(
        count = statuses.len(),
        "harness.status IPC handler: returning cached statuses"
    );
    Ok(statuses)
}
