//! InMemoryKeychain — HashMap-backed `Keychain` implementation.
//!
//! # Purpose
//! Provides a fully functional, always-compiled `Keychain` implementation backed by a
//! `HashMap`. Used as:
//!   1. The backend for all unit tests (no OS keychain needed, no entitlements).
//!   2. A runtime fallback on platforms where the OS keychain is unavailable.
//!
//! # Security invariant
//! Secret values are stored in plain memory. This is intentional for a test/fallback
//! backend; the real OS implementations provide hardware-backed protection.
//! Secret values MUST NOT appear in any tracing or error output from this module.
//!
//! # SPORT
//! MASTER-COMPONENTS.md: cascade-keychain / InMemoryKeychain

use std::collections::HashMap;
use std::sync::Mutex;

use crate::{Keychain, KeychainError};

/// HashMap-backed keychain for tests and headless environments.
///
/// # Purpose
/// Thread-safe in-memory credential store; implements [`Keychain`] on all platforms.
///
/// # Inputs / Outputs
/// Matches the `Keychain` trait contract exactly.
///
/// # Constraints
/// - Not persistent: data is lost when the process exits.
/// - Secret values never appear in tracing or error output.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-keychain / InMemoryKeychain
pub struct InMemoryKeychain {
    // key: (service, account), value: secret
    store: Mutex<HashMap<(String, String), String>>,
}

impl InMemoryKeychain {
    /// Create a new, empty in-memory keychain.
    pub fn new() -> Self {
        Self {
            store: Mutex::new(HashMap::new()),
        }
    }
}

impl Default for InMemoryKeychain {
    fn default() -> Self {
        Self::new()
    }
}

impl Keychain for InMemoryKeychain {
    fn get_key(&self, service: &str, account: &str) -> Result<String, KeychainError> {
        let store = self
            .store
            .lock()
            .map_err(|e| KeychainError::Other(format!("lock poisoned: {e}")))?;
        store
            .get(&(service.to_owned(), account.to_owned()))
            .cloned()
            .ok_or(KeychainError::NotFound)
        // NOTE: secret value is returned to caller but never logged here.
    }

    fn set_key(&self, service: &str, account: &str, secret: &str) -> Result<(), KeychainError> {
        let mut store = self
            .store
            .lock()
            .map_err(|e| KeychainError::Other(format!("lock poisoned: {e}")))?;
        // Log only service/account — never the secret value.
        tracing::debug!(service, account, "in-memory keychain: set_key");
        store.insert((service.to_owned(), account.to_owned()), secret.to_owned());
        Ok(())
    }

    fn delete_key(&self, service: &str, account: &str) -> Result<(), KeychainError> {
        let mut store = self
            .store
            .lock()
            .map_err(|e| KeychainError::Other(format!("lock poisoned: {e}")))?;
        if store
            .remove(&(service.to_owned(), account.to_owned()))
            .is_some()
        {
            tracing::debug!(service, account, "in-memory keychain: deleted key");
            Ok(())
        } else {
            Err(KeychainError::NotFound)
        }
    }

    fn list_keys(&self, service: &str) -> Result<Vec<String>, KeychainError> {
        let store = self
            .store
            .lock()
            .map_err(|e| KeychainError::Other(format!("lock poisoned: {e}")))?;
        let accounts: Vec<String> = store
            .keys()
            .filter(|(svc, _)| svc == service)
            .map(|(_, acct)| acct.clone())
            .collect();
        Ok(accounts)
    }
}
