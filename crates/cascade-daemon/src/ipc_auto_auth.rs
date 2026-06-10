//! # ipc_auto_auth
//!
//! Daemon-side IPC handlers for auto-auth harness detection and import.
//!
//! ## Purpose
//! Implements the handlers for:
//! - `cascade_auto_auth_scan`: scan installed harnesses, return DiscoveredAccount list.
//! - `cascade_auto_auth_import`: import selected env API keys into cascade-keychain.
//!
//! ## Inputs
//! - None for scan.
//! - `Vec<DiscoveredAccount>` for import.
//!
//! ## Outputs
//! - `Vec<DiscoveredAccount>` from scan.
//! - `ImportResult` from import.
//!
//! ## Constraints
//! - Read-only for scan — no harness config file is ever written.
//! - API key values are never logged.
//! - Delegates to `cascade_providers::auto_auth_import` for all logic.
//!
//! ## SPORT
//! MASTER-RUST-CRATES.md: cascade-daemon / ipc_auto_auth

use cascade_types::auto_auth::{DiscoveredAccount, ImportResult};

/// Handle a `cascade_auto_auth_scan` request.
///
/// # Purpose
/// Runs all harness scanners via `cascade_providers::auto_auth_import::scan_all()`
/// and returns the merged `DiscoveredAccount` list.
///
/// # Inputs
/// None.
///
/// # Outputs
/// `Vec<DiscoveredAccount>` — empty vec when no harnesses are detected.
///
/// # Constraints
/// - Delegates entirely to the providers crate — no scan logic here.
/// - Safe to call before any AI provider is connected.
pub async fn handle_auto_auth_scan() -> Vec<DiscoveredAccount> {
    cascade_providers::auto_auth_import::scan_all()
}

/// Handle a `cascade_auto_auth_import` request.
///
/// # Purpose
/// For each selected `DiscoveredAccount` with `importable=true` and
/// `auth_type=EnvApiKey`, reads the env var value and stores it in
/// cascade-keychain under the appropriate provider key.
///
/// # Inputs
/// - `selected`: the accounts the user chose to import.
///
/// # Outputs
/// `ImportResult` with imported/skipped/errors lists.
///
/// # Constraints
/// - Non-importable accounts are silently moved to `skipped`.
/// - Key values are never logged.
/// - Delegates to `cascade_providers::auto_auth_import::import_accounts()`.
pub async fn handle_auto_auth_import(selected: Vec<DiscoveredAccount>) -> ImportResult {
    cascade_providers::auto_auth_import::import_accounts(selected).await
}
