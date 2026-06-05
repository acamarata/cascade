//! macOS keychain implementation using the `security-framework` crate.
//!
//! # Purpose
//! Wraps the macOS Security Framework (via `security-framework`) to provide
//! OS-backed credential storage. Compiled and active only on `target_os = "macos"`.
//!
//! # Security invariant
//! Secret values MUST NOT appear in any tracing span, log message, or error string.
//! OS error codes and descriptions (which never contain secret values) may appear.
//!
//! # SPORT
//! MASTER-SECURITY.md: os-keychain-crate / MacOsKeychain

use security_framework::passwords::{
    delete_generic_password, get_generic_password, set_generic_password,
};
use security_framework::base::Error as SFError;

use crate::{Keychain, KeychainError};

/// macOS Security Framework keychain implementation.
///
/// # Purpose
/// Stores credentials in the macOS login keychain using the generic password API.
///
/// # Inputs / Outputs
/// Matches the `Keychain` trait contract. `service` maps to the Security Framework
/// "service" attribute; `account` maps to the "account" attribute.
///
/// # Constraints
/// - Only compiled on `target_os = "macos"`.
/// - Requires keychain access entitlements in production (signed app or `--entitlements`).
/// - Secret values never appear in tracing or error output.
///
/// # SPORT
/// MASTER-SECURITY.md: os-keychain-crate / MacOsKeychain
pub struct MacOsKeychain;

impl MacOsKeychain {
    /// Create a new macOS keychain handle.
    pub fn new() -> Self {
        Self
    }
}

impl Default for MacOsKeychain {
    fn default() -> Self {
        Self::new()
    }
}

/// Map a `security_framework::base::Error` to `KeychainError`.
///
/// IMPORTANT: The error message may contain OS descriptions but NEVER secret values.
fn map_sf_error(e: SFError, service: &str, account: &str) -> KeychainError {
    let code = e.code();
    tracing::debug!(
        service,
        account,
        os_error_code = code,
        "macOS Security Framework error"
        // Note: no secret value logged here.
    );
    match code {
        // errSecItemNotFound = -25300
        -25300 => KeychainError::NotFound,
        // errSecAuthFailed = -25293  /  errSecInteractionNotAllowed = -25308
        -25293 | -25308 => KeychainError::PermissionDenied,
        _ => KeychainError::Other(format!("Security Framework error code {code}")),
    }
}

impl Keychain for MacOsKeychain {
    fn get_key(&self, service: &str, account: &str) -> Result<String, KeychainError> {
        tracing::debug!(service, account, "macOS keychain: get_key");
        let bytes = get_generic_password(service, account)
            .map_err(|e| map_sf_error(e, service, account))?;
        // Convert bytes → String; treat non-UTF8 as Other.
        // The resulting String is the secret — returned to caller only, never logged.
        String::from_utf8(bytes)
            .map_err(|_| KeychainError::Other("stored value is not valid UTF-8".to_owned()))
    }

    fn set_key(&self, service: &str, account: &str, secret: &str) -> Result<(), KeychainError> {
        tracing::debug!(service, account, "macOS keychain: set_key");
        // Pass secret bytes to OS; value is not logged.
        set_generic_password(service, account, secret.as_bytes())
            .map_err(|e| map_sf_error(e, service, account))
    }

    fn delete_key(&self, service: &str, account: &str) -> Result<(), KeychainError> {
        tracing::debug!(service, account, "macOS keychain: delete_key");
        delete_generic_password(service, account)
            .map_err(|e| map_sf_error(e, service, account))
    }

    fn list_keys(&self, service: &str) -> Result<Vec<String>, KeychainError> {
        // The Security Framework generic password API does not expose a direct
        // "list all accounts for service" query without the Keychain Item List API
        // (SecKeychainSearchCreate), which is deprecated. Instead, we use a
        // SecItemCopyMatching query via security_framework's item search.
        //
        // If the crate doesn't expose a convenient list API we return Unavailable so
        // callers know to fall back to an internal registry. This is an acceptable
        // trade-off: set/get/delete are the primary operations; list is supplementary.
        tracing::debug!(service, "macOS keychain: list_keys (best-effort)");
        use security_framework::item::{ItemClass, ItemSearchOptions};
        let results = ItemSearchOptions::new()
            .class(ItemClass::generic_password())
            .service(service)
            .load_attributes(true)
            .search()
            .map_err(|e| {
                let code = e.code();
                if code == -25300 {
                    // No items found is not an error for list
                    return KeychainError::NotFound; // handled below
                }
                KeychainError::Other(format!("Security Framework error code {code}"))
            });

        match results {
            Ok(items) => {
                let accounts: Vec<String> = items
                    .into_iter()
                    .filter_map(|r| {
                        r.simplify_dict()
                            .and_then(|m| m.get("acct").cloned())
                    })
                    .collect();
                Ok(accounts)
            }
            Err(KeychainError::NotFound) => Ok(vec![]),
            Err(e) => Err(e),
        }
    }
}
