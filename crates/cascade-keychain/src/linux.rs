//! Linux Secret Service keychain implementation using the `oo7` crate.
//!
//! # Purpose
//! Wraps the D-Bus Secret Service protocol via `oo7::Keyring` to provide
//! OS-backed credential storage on Linux. Compiled only on `target_os = "linux"`.
//!
//! oo7 exposes an async-only API. The [`crate::Keychain`] trait is synchronous,
//! so each call drives the relevant future to completion with
//! [`futures_lite::future::block_on`]. This is sound because oo7's underlying
//! zbus connection runs its own internal reactor thread, so blocking the calling
//! thread does not starve the D-Bus socket I/O.
//!
//! # Security invariant
//! Secret values MUST NOT appear in any tracing span, log message, or error string.
//!
//! # Constraints
//! - Requires a running D-Bus Secret Service daemon (e.g. GNOME Keyring, KWallet 6).
//! - In headless CI, `platform_keychain()` falls back to `InMemoryKeychain` if
//!   `new()` returns `Err` (no Secret Service available).
//!
//! # SPORT
//! MASTER-SECURITY.md: os-keychain-crate / LinuxKeychain

use futures_lite::future::block_on;
use oo7::Keyring;

use crate::{Keychain, KeychainError};

/// Linux D-Bus Secret Service keychain implementation.
///
/// # Purpose
/// Stores credentials in the session/login keyring via the Secret Service protocol.
/// `service` and `account` are stored as lookup attributes.
///
/// # Constraints
/// - Only compiled on `target_os = "linux"`.
/// - Secret values never appear in tracing or error output.
///
/// # SPORT
/// MASTER-SECURITY.md: os-keychain-crate / LinuxKeychain
pub struct LinuxKeychain {
    keyring: Keyring,
}

impl LinuxKeychain {
    /// Connect to the default keyring. Returns `Err` if D-Bus / Secret Service is unavailable.
    pub fn new() -> Result<Self, KeychainError> {
        let keyring = block_on(Keyring::new())
            .map_err(|e| KeychainError::Unavailable(format!("oo7 keyring unavailable: {e}")))?;
        Ok(Self { keyring })
    }
}

/// Build a lookup attribute map for oo7.
fn attrs<'a>(service: &'a str, account: &'a str) -> Vec<(&'a str, &'a str)> {
    vec![("service", service), ("account", account)]
}

/// Build a service-only attribute map for listing.
fn service_attrs(service: &str) -> Vec<(&str, &str)> {
    vec![("service", service)]
}

impl Keychain for LinuxKeychain {
    fn get_key(&self, service: &str, account: &str) -> Result<String, KeychainError> {
        tracing::debug!(service, account, "Linux keychain: get_key");
        let attrs = attrs(service, account);
        block_on(async {
            let items = self
                .keyring
                .search_items(&attrs)
                .await
                .map_err(|e| KeychainError::Other(format!("oo7 search error: {e}")))?;
            let item = items.into_iter().next().ok_or(KeychainError::NotFound)?;
            let secret_bytes = item
                .secret()
                .await
                .map_err(|e| KeychainError::Other(format!("oo7 secret retrieval error: {e}")))?;
            // Secret returned to caller only — never logged.
            String::from_utf8(secret_bytes.to_vec())
                .map_err(|_| KeychainError::Other("stored secret is not valid UTF-8".to_owned()))
        })
    }

    fn set_key(&self, service: &str, account: &str, secret: &str) -> Result<(), KeychainError> {
        tracing::debug!(service, account, "Linux keychain: set_key");
        let attrs = attrs(service, account);
        let label = format!("cascade/{service}/{account}");
        block_on(
            self.keyring
                .create_item(&label, &attrs, secret.as_bytes(), true),
        )
        .map_err(|e| KeychainError::Other(format!("oo7 create_item error: {e}")))
    }

    fn delete_key(&self, service: &str, account: &str) -> Result<(), KeychainError> {
        tracing::debug!(service, account, "Linux keychain: delete_key");
        let attrs = attrs(service, account);
        block_on(async {
            let items = self
                .keyring
                .search_items(&attrs)
                .await
                .map_err(|e| KeychainError::Other(format!("oo7 search error: {e}")))?;
            let item = items.into_iter().next().ok_or(KeychainError::NotFound)?;
            item.delete()
                .await
                .map_err(|e| KeychainError::Other(format!("oo7 delete error: {e}")))
        })
    }

    fn list_keys(&self, service: &str) -> Result<Vec<String>, KeychainError> {
        tracing::debug!(service, "Linux keychain: list_keys");
        let attrs = service_attrs(service);
        block_on(async {
            let items = self
                .keyring
                .search_items(&attrs)
                .await
                .map_err(|e| KeychainError::Other(format!("oo7 search error: {e}")))?;
            let mut accounts = Vec::new();
            for item in items {
                if let Ok(attrs_map) = item.attributes().await {
                    if let Some(account) = attrs_map.get("account") {
                        accounts.push(account.clone());
                    }
                }
            }
            Ok(accounts)
        })
    }
}
