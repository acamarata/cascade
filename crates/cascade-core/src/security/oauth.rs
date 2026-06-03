//! # cascade_core::security::oauth
//!
//! OAuth callback HMAC state validation.
//!
//! ## Design
//!
//! The OAuth 2.0 `state` parameter is a 32-byte random nonce HMAC-SHA256
//! signed with a per-session key. On callback, the validator:
//!
//! 1. Verifies the HMAC using `constant_time_eq` to prevent timing attacks.
//! 2. Checks the redirect URI against the registered exact-match list.
//! 3. Verifies the PKCE `code_verifier` against the stored `code_challenge`
//!    (SHA-256 code challenge method only — no plain PKCE).
//!
//! ## STRIDE mapping
//!
//! Spoofing (S) — ensures that OAuth callbacks arrive from the legitimate
//! authorization server and were not forged or replayed by an attacker.
//!
//! ## Acceptance criteria (from S02 ticket T-P1-E10-S02-09)
//!
//! - 32-byte random nonce, HMAC-SHA256 signed with a per-session key
//! - Callback validator uses `constant_time_eq` (timing-safe)
//! - Redirect URI: exact-match against registered URIs (no prefix/wildcard)
//! - PKCE verifier: SHA-256 code challenge must match before token exchange
//! - Tests: tampered state param, wrong redirect URI, replayed code — all rejected

use std::collections::HashSet;

use crate::security::audit::{AuditEvent, AuditLogger};

// ── OAuthCallbackValidator ────────────────────────────────────────────────────

/// Validates an incoming OAuth 2.0 authorization callback.
///
/// One instance per OAuth session: created when the authorization redirect is
/// initiated and destroyed after the callback is processed (to prevent replay).
pub struct OAuthCallbackValidator {
    /// HMAC-SHA256 of the state nonce, computed at session start.
    expected_state_hmac: Vec<u8>,
    /// The raw state nonce sent in the authorization request.
    original_nonce: Vec<u8>,
    /// Per-session HMAC signing key (32 bytes, random).
    session_key: Vec<u8>,
    /// Registered redirect URIs (exact match, case-sensitive).
    registered_uris: HashSet<String>,
    /// SHA-256 code challenge computed from the PKCE verifier.
    code_challenge: Vec<u8>,
    /// `true` if the callback has already been processed (replay guard).
    consumed: bool,
}

/// A validated and ready-to-use authorization code.
#[derive(Debug)]
pub struct AuthorizationCode(pub String);

/// Reasons an OAuth callback can be rejected.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CallbackRejection {
    /// HMAC verification failed — state was tampered with.
    StateHmacMismatch,
    /// The callback was already processed (replay).
    AlreadyConsumed,
    /// The redirect URI is not in the registered list.
    UnregisteredRedirectUri,
    /// PKCE code challenge does not match the verifier.
    PkceVerifierMismatch,
}

impl std::fmt::Display for CallbackRejection {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CallbackRejection::StateHmacMismatch => write!(f, "state HMAC mismatch"),
            CallbackRejection::AlreadyConsumed => write!(f, "callback already consumed (replay)"),
            CallbackRejection::UnregisteredRedirectUri => {
                write!(f, "redirect URI not in registered list")
            }
            CallbackRejection::PkceVerifierMismatch => write!(f, "PKCE code challenge mismatch"),
        }
    }
}

impl OAuthCallbackValidator {
    /// Create a validator for a new OAuth session.
    ///
    /// # Parameters
    ///
    /// - `session_key`: 32 random bytes generated at session start.
    /// - `nonce`: 32 random bytes included in the `state` query parameter.
    /// - `registered_uris`: exact redirect URIs registered for this client.
    /// - `pkce_verifier`: the PKCE code verifier generated before the redirect.
    pub fn new(
        session_key: Vec<u8>,
        nonce: Vec<u8>,
        registered_uris: impl IntoIterator<Item = String>,
        pkce_verifier: &[u8],
    ) -> Self {
        let state_hmac = hmac_sha256(&session_key, &nonce);
        let code_challenge = sha256_digest(pkce_verifier);
        Self {
            expected_state_hmac: state_hmac,
            original_nonce: nonce,
            session_key,
            registered_uris: registered_uris.into_iter().collect(),
            code_challenge,
            consumed: false,
        }
    }

    /// Validate an incoming callback.
    ///
    /// On success, returns the [`AuthorizationCode`] for token exchange.
    /// On failure, returns a [`CallbackRejection`] and emits an audit event.
    pub async fn validate(
        &mut self,
        state_param: &[u8],
        redirect_uri: &str,
        code: &str,
        pkce_verifier: &[u8],
        logger: &AuditLogger,
    ) -> Result<AuthorizationCode, CallbackRejection> {
        // Replay guard.
        if self.consumed {
            let reason = CallbackRejection::AlreadyConsumed.to_string();
            logger
                .log(AuditEvent::OauthCallbackRejected {
                    reason: reason.clone(),
                })
                .await;
            return Err(CallbackRejection::AlreadyConsumed);
        }

        // Reconstruct the HMAC from the received state param.
        let received_hmac = hmac_sha256(&self.session_key, state_param);
        if !constant_time_eq(&received_hmac, &self.expected_state_hmac) {
            let reason = CallbackRejection::StateHmacMismatch.to_string();
            logger
                .log(AuditEvent::OauthCallbackRejected { reason })
                .await;
            return Err(CallbackRejection::StateHmacMismatch);
        }

        // Exact redirect URI check.
        if !self.registered_uris.contains(redirect_uri) {
            let reason = CallbackRejection::UnregisteredRedirectUri.to_string();
            logger
                .log(AuditEvent::OauthCallbackRejected { reason })
                .await;
            return Err(CallbackRejection::UnregisteredRedirectUri);
        }

        // PKCE code challenge verification.
        let verifier_challenge = sha256_digest(pkce_verifier);
        if !constant_time_eq(&verifier_challenge, &self.code_challenge) {
            let reason = CallbackRejection::PkceVerifierMismatch.to_string();
            logger
                .log(AuditEvent::OauthCallbackRejected { reason })
                .await;
            return Err(CallbackRejection::PkceVerifierMismatch);
        }

        // Mark as consumed to prevent replay.
        self.consumed = true;

        Ok(AuthorizationCode(code.to_owned()))
    }
}

// ── Crypto primitives (stubs — replace with sha2 + hmac when added to workspace) ──

/// HMAC-SHA256 stub. Replace with `hmac::Hmac<sha2::Sha256>` when those crates
/// are added to the workspace Cargo.toml.
///
/// # Why a stub
///
/// The workspace does not yet declare `sha2` or `hmac` as workspace dependencies.
/// This stub ensures the module compiles and the interface is stable. The
/// constant-time property of the comparison is preserved via [`constant_time_eq`].
fn hmac_sha256(key: &[u8], data: &[u8]) -> Vec<u8> {
    // Minimal placeholder: XOR each data byte with the corresponding key byte
    // (cyclic) then append the data length as a u64 LE footer.
    // This is NOT a real HMAC — swap in the `hmac` crate before shipping.
    let mut result: Vec<u8> = data
        .iter()
        .enumerate()
        .map(|(i, &b)| b ^ key[i % key.len()])
        .collect();
    result.extend_from_slice(&(data.len() as u64).to_le_bytes());
    result
}

/// SHA-256 stub. Replace with `sha2::Sha256::digest` when the crate is available.
fn sha256_digest(data: &[u8]) -> Vec<u8> {
    // Placeholder: return 32 bytes derived from the XOR of the input.
    let mut out = vec![0u8; 32];
    for (i, &b) in data.iter().enumerate() {
        out[i % 32] ^= b;
    }
    out
}

/// Constant-time byte-slice comparison to prevent timing attacks.
///
/// Returns `true` if `a` and `b` are equal. Does NOT short-circuit.
/// The caller must ensure both slices have the expected length before calling.
pub fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let mut diff: u8 = 0;
    for (x, y) in a.iter().zip(b.iter()) {
        diff |= x ^ y;
    }
    diff == 0
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn make_validator() -> OAuthCallbackValidator {
        OAuthCallbackValidator::new(
            vec![0u8; 32],                                      // session_key
            vec![1u8; 32],                                      // nonce
            vec!["https://localhost:9761/callback".to_owned()], // registered
            b"pkce-verifier-bytes",                             // pkce verifier
        )
    }

    #[tokio::test]
    async fn valid_callback_accepted() {
        let mut v = make_validator();
        let logger = AuditLogger::noop();
        // Reconstruct the expected state param (same nonce).
        let state = vec![1u8; 32];
        let result = v
            .validate(
                &state,
                "https://localhost:9761/callback",
                "auth-code-123",
                b"pkce-verifier-bytes",
                &logger,
            )
            .await;
        assert!(result.is_ok());
    }

    #[tokio::test]
    async fn tampered_state_rejected() {
        let mut v = make_validator();
        let logger = AuditLogger::noop();
        let tampered_state = vec![0xffu8; 32]; // wrong nonce
        let err = v
            .validate(
                &tampered_state,
                "https://localhost:9761/callback",
                "code",
                b"pkce-verifier-bytes",
                &logger,
            )
            .await
            .unwrap_err();
        assert_eq!(err, CallbackRejection::StateHmacMismatch);
    }

    #[tokio::test]
    async fn wrong_redirect_uri_rejected() {
        let mut v = make_validator();
        let logger = AuditLogger::noop();
        let state = vec![1u8; 32];
        let err = v
            .validate(
                &state,
                "https://evil.example/callback", // not registered
                "code",
                b"pkce-verifier-bytes",
                &logger,
            )
            .await
            .unwrap_err();
        assert_eq!(err, CallbackRejection::UnregisteredRedirectUri);
    }

    #[tokio::test]
    async fn replay_rejected() {
        let mut v = make_validator();
        let logger = AuditLogger::noop();
        let state = vec![1u8; 32];
        // First call succeeds.
        v.validate(
            &state,
            "https://localhost:9761/callback",
            "code",
            b"pkce-verifier-bytes",
            &logger,
        )
        .await
        .unwrap();
        // Second call (replay) is rejected.
        let err = v
            .validate(
                &state,
                "https://localhost:9761/callback",
                "code",
                b"pkce-verifier-bytes",
                &logger,
            )
            .await
            .unwrap_err();
        assert_eq!(err, CallbackRejection::AlreadyConsumed);
    }

    #[test]
    fn constant_time_eq_correct() {
        assert!(constant_time_eq(b"hello", b"hello"));
        assert!(!constant_time_eq(b"hello", b"world"));
        assert!(!constant_time_eq(b"hi", b"hello"));
    }
}
