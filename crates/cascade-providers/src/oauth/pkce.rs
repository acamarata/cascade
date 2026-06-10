//! # oauth::pkce
//!
//! PKCE (Proof Key for Code Exchange) helpers for OAuth 2.0 authorization flows.
//!
//! ## Purpose
//! Provides cryptographically-secure `code_verifier` generation and `code_challenge`
//! computation per RFC 7636 § 4.1–4.2. These helpers are shared by all OAuth
//! provider implementations in `oauth::client`.
//!
//! ## Inputs / Outputs
//! - `generate_code_verifier()` → 43-char base64url string (32 random bytes).
//! - `generate_code_challenge(verifier)` → 43-char base64url string (SHA-256).
//!
//! ## Constraints
//! - Verifier uses 32 random bytes (256 bits) from `pkce::code_verifier`.
//! - Challenge uses S256 method only — plain is disallowed by current spec guidance.
//! - Returned strings are unpadded base64url (no `=` characters).
//! - Inner verifier bytes are never logged.

/// Generate a cryptographically-random PKCE code_verifier.
///
/// # Purpose
/// Produces a 96-character base64url string (unpadded) using the `pkce` crate's
/// CSPRNG. The argument to `pkce::code_verifier` is the OUTPUT LENGTH (43–128 chars
/// per RFC 7636 § 4.1), not the raw byte count.
///
/// # Outputs
/// A 96-character base64url-encoded string suitable for use as `code_verifier`.
///
/// # Constraints
/// - Never log the returned value — it is the PKCE secret.
/// - 96 chars = ~72 bytes of entropy (well above the 32-byte minimum).
/// - `pkce::code_verifier(n)` panics if n < 43 or n > 128 — we use 96 (safe mid-range).
pub fn generate_code_verifier() -> String {
    // pkce::code_verifier(n) generates n output chars of base64url-encoded random data.
    // Must be in range [43, 128] per RFC 7636 § 4.1.
    let raw = pkce::code_verifier(96);
    String::from_utf8(raw).expect("pkce::code_verifier always returns valid UTF-8")
}

/// Compute the PKCE S256 code_challenge from a verifier string.
///
/// # Purpose
/// SHA-256 hashes the UTF-8 encoding of `verifier`, then base64url-encodes
/// (unpadded) the digest. This is the value sent to the authorization endpoint
/// as `code_challenge` with `code_challenge_method=S256`.
///
/// # Inputs
/// - `verifier`: the string returned by `generate_code_verifier()`.
///
/// # Outputs
/// A 43-character base64url-encoded SHA-256 digest.
///
/// # Constraints
/// - Uses the `pkce` crate to match the same encoding as the existing google_oauth.rs.
pub fn generate_code_challenge(verifier: &str) -> String {
    pkce::code_challenge(verifier.as_bytes())
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// RFC 7636 § B known-vector test.
    ///
    /// verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
    /// challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
    #[test]
    fn pkce_known_rfc_vector() {
        let verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
        let challenge = generate_code_challenge(verifier);
        assert_eq!(
            challenge, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
            "RFC 7636 § B S256 vector mismatch"
        );
    }

    /// Challenge from a fresh verifier is always 43 chars (base64url of 32-byte SHA-256).
    /// SHA-256 always produces 32 bytes → 43 unpadded base64url chars, regardless of verifier length.
    #[test]
    fn challenge_length_is_43() {
        let verifier = generate_code_verifier();
        let challenge = generate_code_challenge(&verifier);
        // SHA-256 digest is always 32 bytes → base64url unpadded is always 43 chars.
        assert_eq!(
            challenge.len(),
            43,
            "S256 challenge must be 43 chars, got {}",
            challenge.len()
        );
    }

    /// Verifier from generate_code_verifier() is 96 chars long.
    #[test]
    fn verifier_length_is_96() {
        let verifier = generate_code_verifier();
        assert_eq!(
            verifier.len(),
            96,
            "expected 96-char verifier, got {}",
            verifier.len()
        );
    }

    /// Verifier is non-empty and contains only RFC 7636 unreserved chars:
    /// ALPHA / DIGIT / "-" / "." / "_" / "~"
    #[test]
    fn verifier_is_base64url() {
        let verifier = generate_code_verifier();
        assert!(!verifier.is_empty(), "verifier must not be empty");
        assert!(
            verifier.chars().all(|c| {
                c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.' || c == '~'
            }),
            "verifier contains invalid chars (expected RFC 7636 unreserved): {verifier}"
        );
    }

    /// Two consecutive verifiers must differ (randomness check).
    #[test]
    fn verifiers_are_unique() {
        let v1 = generate_code_verifier();
        let v2 = generate_code_verifier();
        assert_ne!(v1, v2, "consecutive verifiers must differ");
    }

    /// Challenge contains no padding characters.
    #[test]
    fn challenge_has_no_padding() {
        let verifier = generate_code_verifier();
        let challenge = generate_code_challenge(&verifier);
        assert!(
            !challenge.contains('='),
            "challenge must be unpadded base64url"
        );
    }
}
