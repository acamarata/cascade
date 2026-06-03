//! KeyStorage trait and SecretBytes type.
//!
//! This module is the cycle-break resolution from the E7 ↔ E10 DAG conflict
//! identified in Pass 1. E10 (key management) and E7 (RAG vector store) would
//! have formed a circular dependency if E10 imported from E7. Instead:
//!
//! - `cascade-types` defines the `KeyStorage` trait (no dependency on any index).
//! - E7 imports the trait to accept an encrypted index path + key reference.
//! - E10 provides a concrete OS-keychain implementation of the trait.
//!
//! Neither E7 nor E10 depends on the other at compile time.
//!
//! # Security properties
//!
//! `SecretBytes` zeroes its memory on drop via [`zeroize`]-compatible patterns.
//! Callers must not copy the inner bytes into a non-zeroing buffer.

use crate::error::{CascadeError, Result};
use async_trait::async_trait;
use serde::{Deserialize, Serialize};

// ── SecretBytes ───────────────────────────────────────────────────────────────

/// A heap-allocated byte buffer that is zeroed on drop.
///
/// Used to hold API keys, encryption keys, and other secret material in memory.
/// The inner bytes are intentionally not `Clone` to prevent accidental copies.
///
/// # Security
///
/// On drop, all bytes are overwritten with zeroes before the heap allocation is
/// returned to the allocator. This limits the window in which secret material
/// can be read from a memory dump.
#[derive(Serialize, Deserialize)]
pub struct SecretBytes(Vec<u8>);

impl SecretBytes {
    /// Construct from a raw byte vector.
    ///
    /// The caller is responsible for ensuring the original `Vec<u8>` is not
    /// kept alive after handing ownership to `SecretBytes`.
    pub fn new(bytes: Vec<u8>) -> Self {
        Self(bytes)
    }

    /// Construct from a UTF-8 string (e.g. an API key).
    ///
    /// WHY renamed from `from_str`: avoids confusion with `std::str::FromStr::from_str`
    /// (clippy::should_implement_trait). Use `from_utf8_str` for clarity.
    pub fn from_utf8_str(s: &str) -> Self {
        Self(s.as_bytes().to_vec())
    }

    /// Return an immutable view of the secret bytes.
    ///
    /// Prefer this over converting to a `String` to avoid creating an
    /// unzeroed copy in the heap.
    pub fn as_bytes(&self) -> &[u8] {
        &self.0
    }

    /// Return the length in bytes.
    pub fn len(&self) -> usize {
        self.0.len()
    }

    /// Returns `true` if the buffer is empty.
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// Attempt to decode the secret bytes as UTF-8.
    ///
    /// Returns `Err` if the bytes are not valid UTF-8. The returned `&str`
    /// borrows from `self` and does not create an owned copy.
    pub fn as_str(&self) -> std::result::Result<&str, std::str::Utf8Error> {
        std::str::from_utf8(&self.0)
    }
}

impl Drop for SecretBytes {
    fn drop(&mut self) {
        // Zero the buffer before the allocator reclaims it.
        // Using volatile writes prevents the compiler from optimising this away.
        for byte in self.0.iter_mut() {
            // SAFETY: we have exclusive mutable access to the byte.
            unsafe {
                std::ptr::write_volatile(byte, 0u8);
            }
        }
    }
}

impl std::fmt::Debug for SecretBytes {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        // Never print secret contents in debug output.
        write!(f, "SecretBytes([REDACTED; {}])", self.0.len())
    }
}

// Intentionally NOT implementing Clone to prevent accidental copies.

// ── StorageInfo ───────────────────────────────────────────────────────────────

/// Metadata about a stored key, returned by `KeyStorage::describe`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct KeyInfo {
    /// The key identifier.
    pub key_id: String,

    /// A human-readable label (e.g. `"openai-api-key"`).
    pub label: Option<String>,

    /// The backend that stores this key (e.g. `"macos-keychain"`, `"env"`, `"file"`).
    pub backend: String,

    /// When the key was stored (ISO 8601), if the backend tracks timestamps.
    pub created_at: Option<String>,
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Secure storage and retrieval of secret key material.
///
/// Implementations may back this trait with OS keychains, environment variables,
/// encrypted TOML files, or remote secrets managers. The trait is intentionally
/// minimal: `get`, `put`, `delete`.
///
/// All methods take `&self` rather than `&mut self` because secret stores
/// are typically accessed concurrently from async tasks.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::key_storage::{KeyStorage, SecretBytes};
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct EnvKeyStorage;
///
/// #[async_trait]
/// impl KeyStorage for EnvKeyStorage {
///     async fn get(&self, key_id: &str) -> Result<SecretBytes> {
///         let val = std::env::var(key_id).map_err(|_| {
///             cascade_types::error::CascadeError::KeyNotFound { key_id: key_id.into() }
///         })?;
///         Ok(SecretBytes::from_str(&val))
///     }
///
///     async fn put(&self, _key_id: &str, _bytes: SecretBytes) -> Result<()> {
///         Err(cascade_types::error::CascadeError::KeyStorageFailed {
///             backend: "env".into(),
///             detail: "env backend is read-only".into(),
///         })
///     }
///
///     async fn delete(&self, _key_id: &str) -> Result<()> {
///         Ok(()) // no-op for env
///     }
/// }
/// ```
#[async_trait]
pub trait KeyStorage: Send + Sync {
    /// Retrieve secret bytes for `key_id`.
    ///
    /// # Errors
    ///
    /// - [`CascadeError::KeyNotFound`] if `key_id` does not exist in this store.
    /// - [`CascadeError::KeyStorageFailed`] for backend errors.
    async fn get(&self, key_id: &str) -> Result<SecretBytes>;

    /// Store secret bytes under `key_id`.
    ///
    /// If `key_id` already exists, the value is overwritten.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::KeyStorageFailed`] if the backend rejects the
    /// write (permissions, quota, unsupported operation).
    async fn put(&self, key_id: &str, bytes: SecretBytes) -> Result<()>;

    /// Delete the secret stored under `key_id`.
    ///
    /// Returns `Ok(())` even if the key did not exist (idempotent).
    async fn delete(&self, key_id: &str) -> Result<()>;

    /// List the key IDs known to this storage backend.
    ///
    /// May return a partial list if the backend does not support enumeration.
    /// The default implementation returns an empty vec.
    async fn list(&self) -> Result<Vec<KeyInfo>> {
        Ok(vec![])
    }

    /// Returns the backend name (used for `cascade doctor` and logging).
    fn backend_name(&self) -> &str {
        "unknown"
    }
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// In-memory key storage backed by a `HashMap`. For tests only.
///
/// Do not use in production: the map is not zeroed on drop, and there is no
/// access control.
pub struct InMemoryKeyStorage {
    store: std::sync::Mutex<std::collections::HashMap<String, Vec<u8>>>,
}

impl InMemoryKeyStorage {
    pub fn new() -> Self {
        Self {
            store: std::sync::Mutex::new(std::collections::HashMap::new()),
        }
    }
}

impl Default for InMemoryKeyStorage {
    fn default() -> Self {
        Self::new()
    }
}

impl std::fmt::Debug for InMemoryKeyStorage {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let count = self.store.lock().map(|m| m.len()).unwrap_or(0);
        write!(f, "InMemoryKeyStorage(entries={})", count)
    }
}

#[async_trait]
impl KeyStorage for InMemoryKeyStorage {
    async fn get(&self, key_id: &str) -> Result<SecretBytes> {
        let store = self.store.lock().expect("lock poisoned");
        store
            .get(key_id)
            .map(|v| SecretBytes::new(v.clone()))
            .ok_or_else(|| CascadeError::KeyNotFound {
                key_id: key_id.to_owned(),
            })
    }

    async fn put(&self, key_id: &str, bytes: SecretBytes) -> Result<()> {
        let mut store = self.store.lock().expect("lock poisoned");
        store.insert(key_id.to_owned(), bytes.as_bytes().to_vec());
        Ok(())
    }

    async fn delete(&self, key_id: &str) -> Result<()> {
        let mut store = self.store.lock().expect("lock poisoned");
        store.remove(key_id);
        Ok(())
    }

    fn backend_name(&self) -> &str {
        "in-memory"
    }
}

fn _assert_object_safe(_: &dyn KeyStorage) {}
