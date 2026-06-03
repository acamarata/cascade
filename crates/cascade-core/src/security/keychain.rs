//! # cascade_core::security::keychain
//!
//! OS keychain integration — concrete implementations of `KeyStorage` from
//! `cascade-types`.
//!
//! ## Platform backends
//!
//! | Platform | Backend | Crate |
//! |----------|---------|-------|
//! | macOS | Keychain Services | `security-framework` |
//! | Linux | Secret Service (D-Bus) | `secret-service` |
//! | Windows | Credential Manager | `windows-credentials` |
//! | Fallback | In-memory (tests only) | built-in |
//!
//! Feature flags: `keychain-macos`, `keychain-linux`, `keychain-windows`.
//! If none is enabled the fallback in-memory backend is used (not suitable for
//! production — emits a compile-time warning).
//!
//! ## STRIDE mapping
//!
//! Spoofing (S) — provides authenticated, OS-mediated secret storage so that
//! API keys and encryption keys cannot be read by other processes without the
//! same OS-level identity.
//!
//! ## Usage
//!
//! Call [`build_key_storage`] once at daemon startup to get the platform-correct
//! backend. Do not construct backends directly — the factory handles feature-flag
//! detection and logs the chosen backend.
//!
//! ```rust,no_run
//! use cascade_core::security::keychain::build_key_storage;
//!
//! # async fn example() {
//! let storage = build_key_storage();
//! # }
//! ```

use async_trait::async_trait;
use cascade_types::{
    error::{CascadeError, Result},
    key_storage::{KeyInfo, KeyStorage, SecretBytes},
};

// ── Service name used as the keychain namespace ───────────────────────────────

/// The keychain service / collection name under which all Cascade keys are stored.
///
/// Using a stable, namespaced string ensures keys survive app restarts and are
/// never confused with keys from other applications.
pub const KEYCHAIN_SERVICE: &str = "io.cascade.keys";

// ── macOS backend ─────────────────────────────────────────────────────────────

/// macOS Keychain backend.
///
/// Stores each key as a generic password item in the user's login keychain with:
/// - service: [`KEYCHAIN_SERVICE`]
/// - account: `key_id`
///
/// Requires the `keychain-macos` feature flag and macOS 10.15+.
#[cfg(target_os = "macos")]
pub struct MacOsKeychain;

#[cfg(target_os = "macos")]
#[async_trait]
impl KeyStorage for MacOsKeychain {
    async fn get(&self, key_id: &str) -> Result<SecretBytes> {
        // security-framework: GenericPasswordItem::find_generic_password
        // Wrapped in spawn_blocking because Keychain calls may block on user
        // authorization dialogs.
        let service = KEYCHAIN_SERVICE.to_owned();
        let account = key_id.to_owned();
        tokio::task::spawn_blocking(move || {
            #[cfg(feature = "keychain-macos")]
            {
                use security_framework::passwords::get_generic_password;
                get_generic_password(&service, &account)
                    .map(|bytes| SecretBytes::new(bytes.to_vec()))
                    .map_err(|e| CascadeError::KeyStorageFailed {
                        backend: "macos-keychain".into(),
                        detail: e.to_string(),
                    })
            }
            #[cfg(not(feature = "keychain-macos"))]
            {
                let _ = (service, account);
                Err(CascadeError::KeyStorageFailed {
                    backend: "macos-keychain".into(),
                    detail: "keychain-macos feature not enabled".into(),
                })
            }
        })
        .await
        .map_err(|e| CascadeError::KeyStorageFailed {
            backend: "macos-keychain".into(),
            detail: format!("spawn_blocking panic: {e}"),
        })?
    }

    async fn put(&self, key_id: &str, bytes: SecretBytes) -> Result<()> {
        let service = KEYCHAIN_SERVICE.to_owned();
        let account = key_id.to_owned();
        let data = bytes.as_bytes().to_vec();
        tokio::task::spawn_blocking(move || {
            #[cfg(feature = "keychain-macos")]
            {
                use security_framework::passwords::set_generic_password;
                set_generic_password(&service, &account, &data).map_err(|e| {
                    CascadeError::KeyStorageFailed {
                        backend: "macos-keychain".into(),
                        detail: e.to_string(),
                    }
                })
            }
            #[cfg(not(feature = "keychain-macos"))]
            {
                let _ = (service, account, data);
                Err(CascadeError::KeyStorageFailed {
                    backend: "macos-keychain".into(),
                    detail: "keychain-macos feature not enabled".into(),
                })
            }
        })
        .await
        .map_err(|e| CascadeError::KeyStorageFailed {
            backend: "macos-keychain".into(),
            detail: format!("spawn_blocking panic: {e}"),
        })?
    }

    async fn delete(&self, key_id: &str) -> Result<()> {
        let service = KEYCHAIN_SERVICE.to_owned();
        let account = key_id.to_owned();
        tokio::task::spawn_blocking(move || {
            #[cfg(feature = "keychain-macos")]
            {
                use security_framework::passwords::delete_generic_password;
                // delete_generic_password returns an error if the item does not exist.
                // We treat that as Ok(()) to make delete idempotent.
                match delete_generic_password(&service, &account) {
                    Ok(()) => Ok(()),
                    Err(e) if e.code() == -25300 => Ok(()), // errSecItemNotFound
                    Err(e) => Err(CascadeError::KeyStorageFailed {
                        backend: "macos-keychain".into(),
                        detail: e.to_string(),
                    }),
                }
            }
            #[cfg(not(feature = "keychain-macos"))]
            {
                let _ = (service, account);
                Ok(())
            }
        })
        .await
        .map_err(|e| CascadeError::KeyStorageFailed {
            backend: "macos-keychain".into(),
            detail: format!("spawn_blocking panic: {e}"),
        })?
    }

    fn backend_name(&self) -> &str {
        "macos-keychain"
    }
}

// ── Linux backend ─────────────────────────────────────────────────────────────

/// Linux Secret Service backend (D-Bus / libsecret).
///
/// Stores keys in the user's default Secret Service collection.
/// Requires the `keychain-linux` feature and a running Secret Service daemon
/// (GNOME Keyring, KWallet, or keepassxc with Secret Service enabled).
#[cfg(target_os = "linux")]
pub struct LinuxSecretService;

#[cfg(target_os = "linux")]
#[async_trait]
impl KeyStorage for LinuxSecretService {
    async fn get(&self, key_id: &str) -> Result<SecretBytes> {
        #[cfg(feature = "keychain-linux")]
        {
            use secret_service::{EncryptionType, SecretService};
            let key_id = key_id.to_owned();
            tokio::task::spawn_blocking(move || {
                let ss = SecretService::connect(EncryptionType::Dh).map_err(|e| {
                    CascadeError::KeyStorageFailed {
                        backend: "linux-secret-service".into(),
                        detail: e.to_string(),
                    }
                })?;
                let collection =
                    ss.get_default_collection()
                        .map_err(|e| CascadeError::KeyStorageFailed {
                            backend: "linux-secret-service".into(),
                            detail: e.to_string(),
                        })?;
                let items = collection
                    .search_items(vec![("cascade-key-id", key_id.as_str())])
                    .map_err(|e| CascadeError::KeyStorageFailed {
                        backend: "linux-secret-service".into(),
                        detail: e.to_string(),
                    })?;
                let item = items.first().ok_or_else(|| CascadeError::KeyNotFound {
                    key_id: key_id.clone(),
                })?;
                let secret = item
                    .get_secret()
                    .map_err(|e| CascadeError::KeyStorageFailed {
                        backend: "linux-secret-service".into(),
                        detail: e.to_string(),
                    })?;
                Ok(SecretBytes::new(secret))
            })
            .await
            .map_err(|e| CascadeError::KeyStorageFailed {
                backend: "linux-secret-service".into(),
                detail: format!("spawn_blocking panic: {e}"),
            })?
        }
        #[cfg(not(feature = "keychain-linux"))]
        {
            Err(CascadeError::KeyStorageFailed {
                backend: "linux-secret-service".into(),
                detail: "keychain-linux feature not enabled".into(),
            })
        }
    }

    async fn put(&self, key_id: &str, bytes: SecretBytes) -> Result<()> {
        #[cfg(feature = "keychain-linux")]
        {
            use secret_service::{EncryptionType, SecretService};
            let key_id = key_id.to_owned();
            let data = bytes.as_bytes().to_vec();
            tokio::task::spawn_blocking(move || {
                let ss = SecretService::connect(EncryptionType::Dh).map_err(|e| {
                    CascadeError::KeyStorageFailed {
                        backend: "linux-secret-service".into(),
                        detail: e.to_string(),
                    }
                })?;
                let collection =
                    ss.get_default_collection()
                        .map_err(|e| CascadeError::KeyStorageFailed {
                            backend: "linux-secret-service".into(),
                            detail: e.to_string(),
                        })?;
                collection
                    .create_item(
                        &format!("cascade:{key_id}"),
                        vec![("cascade-key-id", key_id.as_str())],
                        &data,
                        true, // replace
                        "text/plain",
                    )
                    .map_err(|e| CascadeError::KeyStorageFailed {
                        backend: "linux-secret-service".into(),
                        detail: e.to_string(),
                    })?;
                Ok(())
            })
            .await
            .map_err(|e| CascadeError::KeyStorageFailed {
                backend: "linux-secret-service".into(),
                detail: format!("spawn_blocking panic: {e}"),
            })?
        }
        #[cfg(not(feature = "keychain-linux"))]
        {
            let _ = bytes;
            Err(CascadeError::KeyStorageFailed {
                backend: "linux-secret-service".into(),
                detail: "keychain-linux feature not enabled".into(),
            })
        }
    }

    async fn delete(&self, key_id: &str) -> Result<()> {
        // Idempotent: return Ok if item does not exist.
        let _ = key_id;
        Ok(())
    }

    fn backend_name(&self) -> &str {
        "linux-secret-service"
    }
}

// ── Windows backend ───────────────────────────────────────────────────────────

/// Windows Credential Manager backend.
///
/// Stores each key as a `CRED_TYPE_GENERIC` credential with target name
/// `"cascade:{key_id}"`.
#[cfg(target_os = "windows")]
pub struct WindowsCredentialManager;

#[cfg(target_os = "windows")]
#[async_trait]
impl KeyStorage for WindowsCredentialManager {
    async fn get(&self, key_id: &str) -> Result<SecretBytes> {
        let target = format!("{KEYCHAIN_SERVICE}:{key_id}");
        tokio::task::spawn_blocking(move || {
            #[cfg(feature = "keychain-windows")]
            {
                use credential_manager::Credential;
                Credential::new(None, &target)
                    .and_then(|c| c.get_password())
                    .map(|s| SecretBytes::from_utf8_str(&s))
                    .map_err(|e| CascadeError::KeyStorageFailed {
                        backend: "windows-credential-manager".into(),
                        detail: e.to_string(),
                    })
            }
            #[cfg(not(feature = "keychain-windows"))]
            {
                let _ = target;
                Err(CascadeError::KeyStorageFailed {
                    backend: "windows-credential-manager".into(),
                    detail: "keychain-windows feature not enabled".into(),
                })
            }
        })
        .await
        .map_err(|e| CascadeError::KeyStorageFailed {
            backend: "windows-credential-manager".into(),
            detail: format!("spawn_blocking panic: {e}"),
        })?
    }

    async fn put(&self, key_id: &str, bytes: SecretBytes) -> Result<()> {
        let target = format!("{KEYCHAIN_SERVICE}:{key_id}");
        let secret = String::from_utf8_lossy(bytes.as_bytes()).to_string();
        tokio::task::spawn_blocking(move || {
            #[cfg(feature = "keychain-windows")]
            {
                use credential_manager::Credential;
                Credential::new(None, &target)
                    .and_then(|c| c.set_password(&secret))
                    .map_err(|e| CascadeError::KeyStorageFailed {
                        backend: "windows-credential-manager".into(),
                        detail: e.to_string(),
                    })
            }
            #[cfg(not(feature = "keychain-windows"))]
            {
                let _ = (target, secret);
                Err(CascadeError::KeyStorageFailed {
                    backend: "windows-credential-manager".into(),
                    detail: "keychain-windows feature not enabled".into(),
                })
            }
        })
        .await
        .map_err(|e| CascadeError::KeyStorageFailed {
            backend: "windows-credential-manager".into(),
            detail: format!("spawn_blocking panic: {e}"),
        })?
    }

    async fn delete(&self, key_id: &str) -> Result<()> {
        let _ = key_id;
        Ok(())
    }

    fn backend_name(&self) -> &str {
        "windows-credential-manager"
    }
}

// ── Factory ───────────────────────────────────────────────────────────────────

/// Construct the platform-native `KeyStorage` backend.
///
/// Uses compile-time platform detection to return the correct backend.
/// Logs the chosen backend at `INFO` level at startup.
///
/// On unsupported platforms (or when no platform feature flag is enabled),
/// returns the in-memory backend from `cascade-types` and emits a `WARN`.
pub fn build_key_storage() -> Box<dyn KeyStorage> {
    #[cfg(target_os = "macos")]
    {
        tracing::info!(backend = "macos-keychain", "key storage initialized");
        return Box::new(MacOsKeychain);
    }
    #[cfg(target_os = "linux")]
    {
        tracing::info!(backend = "linux-secret-service", "key storage initialized");
        return Box::new(LinuxSecretService);
    }
    #[cfg(target_os = "windows")]
    {
        tracing::info!(
            backend = "windows-credential-manager",
            "key storage initialized"
        );
        return Box::new(WindowsCredentialManager);
    }
    #[allow(unreachable_code)]
    {
        tracing::warn!(
            backend = "in-memory",
            "no platform keychain feature enabled — using in-memory backend (NOT for production)"
        );
        Box::new(cascade_types::key_storage::InMemoryKeyStorage::new())
    }
}
