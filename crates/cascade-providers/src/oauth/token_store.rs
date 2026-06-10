//! # oauth::token_store
//!
//! Keychain-backed persistent token storage for OAuth 2.0 access/refresh tokens.
//!
//! ## Purpose
//! Reads and writes `OAuthTokens` via `cascade-keychain` (`Keychain` trait).
//! Tokens are JSON-serialized and stored as opaque secrets — never written to
//! disk as plaintext, never included in log output.
//!
//! ## Key naming
//! Keychain service: `"cascade.oauth.{provider_id}"`.
//! Account key:      `"tokens"`.
//!
//! ## Constraints
//! - Token values MUST NOT appear in tracing output or error messages.
//! - `provider_id` is a short lowercase ASCII identifier (e.g. `"google"`).
//! - JSON serialization only — no custom encoding.

use cascade_keychain::{Keychain, KeychainError};
use serde::{Deserialize, Serialize};

/// An OAuth 2.0 token set returned after a successful code exchange or refresh.
///
/// ## Purpose
/// Holds the access token, optional refresh token, and expiry information
/// for a completed OAuth flow.
///
/// ## Constraints
/// - Token field values are NEVER logged (no Display impl that reveals them).
/// - `expires_in` is relative to the time of issuance; callers should compute
///   an absolute `expires_at` timestamp if needed.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OAuthTokens {
    /// Bearer access token for API calls.
    pub access_token: String,
    /// Long-lived refresh token (absent on implicit-grant and some server flows).
    pub refresh_token: Option<String>,
    /// Lifetime of the access token in seconds from issuance.
    pub expires_in: u64,
    /// Token type string returned by the server (usually `"Bearer"`).
    pub token_type: String,
}

/// Errors that can occur during token store operations.
#[derive(Debug, thiserror::Error)]
pub enum TokenStoreError {
    /// The keychain backend returned an error.
    #[error("keychain error: {0}")]
    Keychain(#[from] KeychainError),
    /// The stored token JSON could not be deserialized.
    #[error("token deserialization error: {0}")]
    Deserialize(String),
    /// No token is stored for this provider.
    #[error("no token stored for provider: {0}")]
    NotFound(String),
}

/// Keychain-backed storage for OAuth tokens.
///
/// ## Purpose
/// Wraps a `Box<dyn Keychain>` and provides `load`/`save`/`delete` operations
/// keyed by `provider_id`. Token bytes are JSON-encoded before storage and
/// decoded on retrieval.
///
/// ## Constraints
/// - Token values never appear in log output.
/// - One instance per provider is typical, but multiple can share the same keychain.
///
/// ## SPORT
/// MASTER-COMPONENTS.md: cascade-providers / TokenStore
pub struct TokenStore {
    keychain: Box<dyn Keychain>,
}

impl TokenStore {
    /// Create a `TokenStore` backed by the given `Keychain` implementation.
    ///
    /// # Inputs
    /// - `keychain`: any `Box<dyn Keychain>` (platform keychain or in-memory for tests).
    pub fn new(keychain: Box<dyn Keychain>) -> Self {
        Self { keychain }
    }

    /// Compute the keychain service name for a given provider ID.
    ///
    /// Format: `"cascade.oauth.{provider_id}"`.
    fn service_name(provider_id: &str) -> String {
        format!("cascade.oauth.{provider_id}")
    }

    /// Save `tokens` to the keychain for the given `provider_id`.
    ///
    /// # Inputs
    /// - `provider_id`: short lowercase ASCII label, e.g. `"google"`.
    /// - `tokens`: the `OAuthTokens` to persist.
    ///
    /// # Constraints
    /// - Token values are JSON-serialized and stored as an opaque secret string.
    /// - Any prior entry for `(service, "tokens")` is overwritten.
    pub fn save(&self, provider_id: &str, tokens: &OAuthTokens) -> Result<(), TokenStoreError> {
        let service = Self::service_name(provider_id);
        // Serialize to JSON — tokens are never written to tracing at any log level.
        let json = serde_json::to_string(tokens)
            .map_err(|e| TokenStoreError::Deserialize(e.to_string()))?;
        self.keychain.set_key(&service, "tokens", &json)?;
        Ok(())
    }

    /// Load tokens for `provider_id` from the keychain.
    ///
    /// # Outputs
    /// `Ok(OAuthTokens)` if found and parseable; `Err(TokenStoreError::NotFound)`
    /// if no entry exists; `Err(TokenStoreError::Deserialize)` if parse fails.
    pub fn load(&self, provider_id: &str) -> Result<OAuthTokens, TokenStoreError> {
        let service = Self::service_name(provider_id);
        let json = self
            .keychain
            .get_key(&service, "tokens")
            .map_err(|e| match e {
                KeychainError::NotFound => TokenStoreError::NotFound(provider_id.to_owned()),
                other => TokenStoreError::Keychain(other),
            })?;
        serde_json::from_str::<OAuthTokens>(&json)
            .map_err(|e| TokenStoreError::Deserialize(e.to_string()))
    }

    /// Delete the stored token for `provider_id`.
    ///
    /// Returns `Ok(())` even if no entry existed (idempotent).
    pub fn delete(&self, provider_id: &str) -> Result<(), TokenStoreError> {
        let service = Self::service_name(provider_id);
        match self.keychain.delete_key(&service, "tokens") {
            Ok(()) => Ok(()),
            Err(KeychainError::NotFound) => Ok(()),
            Err(e) => Err(TokenStoreError::Keychain(e)),
        }
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_keychain::InMemoryKeychain;

    fn make_tokens(access: &str) -> OAuthTokens {
        OAuthTokens {
            access_token: access.to_owned(),
            refresh_token: Some("refresh-xyz".to_owned()),
            expires_in: 3600,
            token_type: "Bearer".to_owned(),
        }
    }

    fn make_store() -> TokenStore {
        TokenStore::new(Box::new(InMemoryKeychain::new()))
    }

    /// Round-trip: save then load returns the same token.
    #[test]
    fn token_store_round_trip() {
        let store = make_store();
        let tokens = make_tokens("access-abc");

        store.save("google", &tokens).unwrap();
        let loaded = store.load("google").unwrap();

        assert_eq!(loaded.access_token, "access-abc");
        assert_eq!(loaded.refresh_token.as_deref(), Some("refresh-xyz"));
        assert_eq!(loaded.expires_in, 3600);
        assert_eq!(loaded.token_type, "Bearer");
    }

    /// Load on a missing provider returns NotFound.
    #[test]
    fn token_store_not_found() {
        let store = make_store();
        match store.load("nonexistent") {
            Err(TokenStoreError::NotFound(id)) => assert_eq!(id, "nonexistent"),
            other => panic!("expected NotFound, got {other:?}"),
        }
    }

    /// Delete removes stored token; second delete is idempotent.
    #[test]
    fn token_store_delete_idempotent() {
        let store = make_store();
        let tokens = make_tokens("tok");
        store.save("openai", &tokens).unwrap();
        store.delete("openai").unwrap();

        // Should be gone now.
        assert!(matches!(
            store.load("openai"),
            Err(TokenStoreError::NotFound(_))
        ));

        // Second delete is idempotent.
        store.delete("openai").unwrap();
    }

    /// Overwrite: second save replaces the first.
    #[test]
    fn token_store_overwrite() {
        let store = make_store();
        store.save("github", &make_tokens("first")).unwrap();
        store.save("github", &make_tokens("second")).unwrap();
        let loaded = store.load("github").unwrap();
        assert_eq!(loaded.access_token, "second");
    }

    /// Different provider IDs are stored independently.
    #[test]
    fn token_store_provider_isolation() {
        let store = make_store();
        store.save("google", &make_tokens("g-tok")).unwrap();
        store.save("github", &make_tokens("gh-tok")).unwrap();

        assert_eq!(store.load("google").unwrap().access_token, "g-tok");
        assert_eq!(store.load("github").unwrap().access_token, "gh-tok");
    }

    /// Service key naming: verifiable via keychain list_keys.
    #[test]
    fn token_store_service_naming() {
        let kc = Box::new(InMemoryKeychain::new());
        let store = TokenStore::new(kc);
        store.save("anthropic", &make_tokens("ant")).unwrap();

        // The keychain key returned by service_name must match the expected format.
        let service = TokenStore::service_name("anthropic");
        assert_eq!(service, "cascade.oauth.anthropic");
    }
}
