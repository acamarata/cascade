//! cascade-keychain — cross-platform OS credential storage for Cascade.
//!
//! # Purpose
//! Unified `Keychain` trait with platform implementations (macOS Security Framework,
//! Linux Secret Service via oo7, Windows Credential Manager via windows-rs) and an
//! `InMemoryKeychain` backend for tests and headless CI environments.
//!
//! # Security invariant
//! Secret *values* MUST NEVER appear in tracing spans, error messages, or log output.
//! Only service/account names may appear. Every error path is reviewed for this.
//!
//! # Usage
//! ```rust
//! use cascade_keychain::{platform_keychain, Keychain};
//! let kc = platform_keychain();
//! kc.set_key("dev.cascade", "api-key", "s3cr3t").unwrap();
//! let val = kc.get_key("dev.cascade", "api-key").unwrap();
//! assert_eq!(val, "s3cr3t");
//! kc.delete_key("dev.cascade", "api-key").unwrap();
//! ```

mod error;
mod memory;

#[cfg(target_os = "macos")]
mod macos;

#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "windows")]
mod windows;

pub use error::KeychainError;
pub use memory::InMemoryKeychain;

/// Unified cross-platform keychain trait.
///
/// # Purpose
/// Provides get/set/delete/list operations over OS-backed or in-memory credential storage.
///
/// # Inputs
/// All methods take `service` (a reverse-DNS bundle identifier e.g. `"dev.cascade"`)
/// and `account` (an opaque string key identifier, e.g. a user or API label).
///
/// # Outputs
/// `Result<T, KeychainError>` — no secret values appear in any error variant.
///
/// # Constraints
/// - Object-safe: `Box<dyn Keychain>` works.
/// - Secret values MUST NOT appear in any tracing or error output.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-keychain / Keychain trait
pub trait Keychain: Send + Sync {
    /// Retrieve the secret stored for `(service, account)`.
    ///
    /// Returns `Err(KeychainError::NotFound)` when no item exists.
    fn get_key(&self, service: &str, account: &str) -> Result<String, KeychainError>;

    /// Store or overwrite the secret for `(service, account)`.
    fn set_key(&self, service: &str, account: &str, secret: &str) -> Result<(), KeychainError>;

    /// Remove the item for `(service, account)`.
    ///
    /// Returns `Err(KeychainError::NotFound)` when no item exists.
    fn delete_key(&self, service: &str, account: &str) -> Result<(), KeychainError>;

    /// List all account names stored under `service`.
    fn list_keys(&self, service: &str) -> Result<Vec<String>, KeychainError>;
}

/// Return a boxed platform keychain.
///
/// # Purpose
/// cfg-selects the OS-native implementation. Falls back to `InMemoryKeychain`
/// (with a warning) if the OS keychain is structurally unavailable at runtime.
///
/// # Inputs
/// None — reads compile-time `cfg(target_os)`.
///
/// # Outputs
/// `Box<dyn Keychain>` — always succeeds; worst-case returns an in-memory fallback.
///
/// # Constraints
/// - Secret values never appear in fallback tracing output.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-keychain / platform_keychain factory
pub fn platform_keychain() -> Box<dyn Keychain> {
    #[cfg(target_os = "macos")]
    {
        Box::new(macos::MacOsKeychain::new())
    }

    #[cfg(target_os = "linux")]
    {
        match linux::LinuxKeychain::new() {
            Ok(kc) => Box::new(kc),
            Err(e) => {
                tracing::warn!(
                    reason = %e,
                    "Linux Secret Service unavailable; falling back to in-memory keychain \
                     (secrets will not persist across restarts)"
                );
                Box::new(InMemoryKeychain::new())
            }
        }
    }

    #[cfg(target_os = "windows")]
    {
        Box::new(windows::WindowsKeychain::new())
    }

    #[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
    {
        tracing::warn!(
            "Unsupported OS — falling back to in-memory keychain \
             (secrets will not persist across restarts)"
        );
        Box::new(InMemoryKeychain::new())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// InMemoryKeychain: set then get returns the stored secret.
    #[test]
    fn inmemory_set_get_roundtrip() {
        let kc = InMemoryKeychain::new();
        kc.set_key("dev.cascade", "test-account", "my-secret").unwrap();
        let val = kc.get_key("dev.cascade", "test-account").unwrap();
        assert_eq!(val, "my-secret");
    }

    /// InMemoryKeychain: delete then get returns NotFound.
    #[test]
    fn inmemory_delete_then_get_returns_not_found() {
        let kc = InMemoryKeychain::new();
        kc.set_key("dev.cascade", "to-delete", "value").unwrap();
        kc.delete_key("dev.cascade", "to-delete").unwrap();
        let err = kc.get_key("dev.cascade", "to-delete").unwrap_err();
        assert!(matches!(err, KeychainError::NotFound));
    }

    /// InMemoryKeychain: list_keys returns the stored accounts.
    #[test]
    fn inmemory_list_keys() {
        let kc = InMemoryKeychain::new();
        kc.set_key("dev.cascade", "alpha", "a").unwrap();
        kc.set_key("dev.cascade", "beta", "b").unwrap();
        // Different service should not appear
        kc.set_key("other.service", "gamma", "c").unwrap();

        let mut accounts = kc.list_keys("dev.cascade").unwrap();
        accounts.sort();
        assert_eq!(accounts, vec!["alpha", "beta"]);
    }

    /// InMemoryKeychain: get on missing key returns NotFound.
    #[test]
    fn inmemory_get_missing_returns_not_found() {
        let kc = InMemoryKeychain::new();
        let err = kc.get_key("dev.cascade", "no-such").unwrap_err();
        assert!(matches!(err, KeychainError::NotFound));
    }

    /// InMemoryKeychain: delete on missing key returns NotFound.
    #[test]
    fn inmemory_delete_missing_returns_not_found() {
        let kc = InMemoryKeychain::new();
        let err = kc.delete_key("dev.cascade", "no-such").unwrap_err();
        assert!(matches!(err, KeychainError::NotFound));
    }

    /// Box<dyn Keychain> compiles — verifies object safety.
    #[test]
    fn object_safety_box_dyn_keychain() {
        let _kc: Box<dyn Keychain> = Box::new(InMemoryKeychain::new());
    }
}
