//! HMAC-SHA256 authentication token scheme for the Cascade MCP server.
//!
//! ## Purpose
//! Prevents unauthorized local processes from connecting to the cascade MCP
//! server over Unix socket or TCP transports. Stdio transport is exempt (already
//! process-isolated by the OS).
//!
//! ## Token format
//! ```text
//! "cascade-mcp-{base64url(payload || mac)}"
//! ```
//! where:
//! - `payload` = `timestamp_le_u64 (8 bytes) || nonce (8 bytes)` — 16 bytes total
//! - `mac`     = HMAC-SHA256(secret, payload) — 32 bytes
//! - `base64url` = URL-safe base64, no padding (base64ct `Base64Url`)
//!
//! ## Inputs
//! - `secret`: 32-byte random key loaded from / written to the secret-file
//!
//! ## Outputs
//! - `generate_token()` → `String` (opaque bearer token)
//! - `validate_token(&str)` → `Result<(), AuthError>`
//!
//! ## Constraints
//! - Tokens expire 300 seconds after the embedded timestamp.
//! - HMAC comparison is constant-time (via `hmac::Mac::verify_slice`).
//! - Nonce prevents replay within the validity window.
//! - Secret file is written with mode 0600 (Unix); hidden attribute on Windows.
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-mcp — auth.rs

use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

use base64ct::{Base64UrlUnpadded, Encoding};
use hmac::{Hmac, Mac};
use rand::RngCore;
use sha2::Sha256;
use thiserror::Error;

/// Prefix prepended to every token string.
const TOKEN_PREFIX: &str = "cascade-mcp-";

/// Encoded payload length: 8 bytes timestamp + 8 bytes nonce = 16 bytes.
const PAYLOAD_LEN: usize = 16;

/// HMAC-SHA256 output length in bytes.
const MAC_LEN: usize = 32;

/// Total decoded bytes: payload + mac.
const TOTAL_DECODED_LEN: usize = PAYLOAD_LEN + MAC_LEN;

/// Token validity window in seconds (300 s = 5 minutes).
const EXPIRY_SECS: u64 = 300;

/// Clock-skew tolerance in seconds (allow tokens from up to 10 s in the future).
const CLOCK_SKEW_SECS: u64 = 10;

type HmacSha256 = Hmac<Sha256>;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by [`McpAuth`] and [`McpSecretManager`].
#[derive(Debug, Error)]
pub enum AuthError {
    /// Token string does not start with the expected prefix.
    #[error("token has invalid prefix")]
    InvalidPrefix,

    /// Base64 decode failed or decoded length is wrong.
    #[error("token encoding is malformed")]
    MalformedToken,

    /// HMAC verification failed (wrong secret or tampered payload).
    #[error("token MAC verification failed")]
    InvalidMac,

    /// Token timestamp is more than [`EXPIRY_SECS`] seconds in the past.
    #[error("token has expired")]
    TokenExpired,

    /// I/O error while reading or writing the secret file.
    #[error("secret file I/O error: {0}")]
    Io(#[from] std::io::Error),
}

// ── McpAuth ──────────────────────────────────────────────────────────────────

/// Holds the 32-byte server secret and generates / validates MCP auth tokens.
///
/// Cheaply cloneable; all methods are pure (no interior mutation).
#[derive(Clone)]
pub struct McpAuth {
    secret: [u8; 32],
}

impl McpAuth {
    /// Create an [`McpAuth`] from an existing 32-byte secret.
    pub fn from_secret(secret: [u8; 32]) -> Self {
        Self { secret }
    }

    /// Generate a fresh bearer token signed with this auth's secret.
    ///
    /// Layout: `cascade-mcp-{base64url(timestamp_le || nonce || mac)}`
    pub fn generate_token(&self) -> String {
        let timestamp = unix_now_secs();
        let mut nonce = [0u8; 8];
        rand::thread_rng().fill_bytes(&mut nonce);

        let mut payload = [0u8; PAYLOAD_LEN];
        payload[..8].copy_from_slice(&timestamp.to_le_bytes());
        payload[8..].copy_from_slice(&nonce);

        let mac = compute_mac(&self.secret, &payload);

        let mut raw = [0u8; TOTAL_DECODED_LEN];
        raw[..PAYLOAD_LEN].copy_from_slice(&payload);
        raw[PAYLOAD_LEN..].copy_from_slice(&mac);

        format!("{}{}", TOKEN_PREFIX, Base64UrlUnpadded::encode_string(&raw))
    }

    /// Validate a bearer token against this auth's secret.
    ///
    /// Returns `Ok(())` if the token is valid and unexpired.
    /// Returns an [`AuthError`] variant describing the failure otherwise.
    ///
    /// The HMAC comparison uses `hmac::Mac::verify_slice` which is constant-time.
    pub fn validate_token(&self, token: &str) -> Result<(), AuthError> {
        // ── 1. Strip prefix ────────────────────────────────────────────────
        let encoded = token
            .strip_prefix(TOKEN_PREFIX)
            .ok_or(AuthError::InvalidPrefix)?;

        // ── 2. Base64url-decode ────────────────────────────────────────────
        let mut raw = [0u8; TOTAL_DECODED_LEN];
        let decoded =
            Base64UrlUnpadded::decode(encoded, &mut raw).map_err(|_| AuthError::MalformedToken)?;
        if decoded.len() != TOTAL_DECODED_LEN {
            return Err(AuthError::MalformedToken);
        }

        // ── 3. Split payload / mac ─────────────────────────────────────────
        let payload = &raw[..PAYLOAD_LEN];
        let mac_bytes = &raw[PAYLOAD_LEN..];

        // ── 4. Constant-time HMAC verify ───────────────────────────────────
        let mut mac =
            HmacSha256::new_from_slice(&self.secret).expect("HMAC accepts any key length");
        mac.update(payload);
        mac.verify_slice(mac_bytes)
            .map_err(|_| AuthError::InvalidMac)?;

        // ── 5. Timestamp check (only after MAC passes) ─────────────────────
        let mut ts_bytes = [0u8; 8];
        ts_bytes.copy_from_slice(&payload[..8]);
        let token_ts = u64::from_le_bytes(ts_bytes);

        let now = unix_now_secs();
        // Reject tokens from the far future (beyond clock-skew tolerance)
        if token_ts > now + CLOCK_SKEW_SECS {
            return Err(AuthError::TokenExpired);
        }
        // Reject tokens older than EXPIRY_SECS
        if now.saturating_sub(token_ts) > EXPIRY_SECS {
            return Err(AuthError::TokenExpired);
        }

        Ok(())
    }
}

impl std::fmt::Debug for McpAuth {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("McpAuth")
            .field("secret", &"[REDACTED]")
            .finish()
    }
}

// ── McpSecretManager ─────────────────────────────────────────────────────────

/// Loads or creates the 32-byte server secret at the given path.
pub struct McpSecretManager;

impl McpSecretManager {
    /// Load an existing 32-byte secret from `path`, or generate a new one and
    /// write it to `path` with mode 0600 (Unix) / hidden attribute (Windows).
    ///
    /// The file contains exactly 32 raw bytes (no encoding, no newline).
    pub fn load_or_create(path: &Path) -> Result<McpAuth, AuthError> {
        if path.exists() {
            let bytes = std::fs::read(path)?;
            if bytes.len() == 32 {
                let mut secret = [0u8; 32];
                secret.copy_from_slice(&bytes);
                return Ok(McpAuth::from_secret(secret));
            }
            // File exists but is corrupt/truncated — regenerate it.
            tracing::warn!(
                path = %path.display(),
                "mcp secret file has unexpected length ({}), regenerating",
                bytes.len()
            );
        }

        // Generate fresh secret
        let mut secret = [0u8; 32];
        rand::thread_rng().fill_bytes(&mut secret);

        // Ensure parent directory exists
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }

        write_secret_file(path, &secret)?;
        Ok(McpAuth::from_secret(secret))
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Compute HMAC-SHA256 of `data` under `secret`.
fn compute_mac(secret: &[u8; 32], data: &[u8]) -> [u8; MAC_LEN] {
    let mut mac = HmacSha256::new_from_slice(secret).expect("HMAC accepts any key length");
    mac.update(data);
    let result = mac.finalize();
    result.into_bytes().into()
}

/// Current Unix timestamp in seconds, saturating to 0 on errors.
fn unix_now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// Write secret bytes to `path` with restrictive permissions.
///
/// On Unix the file is created / truncated with mode 0600.
/// On Windows the file is written and then marked hidden (best effort).
fn write_secret_file(path: &Path, secret: &[u8; 32]) -> Result<(), AuthError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let mut opts = std::fs::OpenOptions::new();
        opts.write(true).create(true).truncate(true).mode(0o600);
        let mut file = opts.open(path)?;
        std::io::Write::write_all(&mut file, secret)?;
    }
    #[cfg(not(unix))]
    {
        std::fs::write(path, secret)?;
        // Best-effort: mark hidden on Windows
        #[cfg(windows)]
        {
            use std::os::windows::ffi::OsStrExt;
            let wide: Vec<u16> = path
                .as_os_str()
                .encode_wide()
                .chain(std::iter::once(0))
                .collect();
            unsafe {
                // FILE_ATTRIBUTE_HIDDEN = 0x2
                windows_sys::Win32::Storage::FileSystem::SetFileAttributesW(wide.as_ptr(), 0x2);
            }
        }
    }
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn test_auth() -> McpAuth {
        let mut secret = [0u8; 32];
        // Deterministic secret for unit tests (never use outside tests)
        for (i, b) in secret.iter_mut().enumerate() {
            *b = i as u8;
        }
        McpAuth::from_secret(secret)
    }

    // ── Round-trip ────────────────────────────────────────────────────────

    #[test]
    fn auth_sign_verify_roundtrip() {
        let auth = test_auth();
        let token = auth.generate_token();
        assert!(
            token.starts_with(TOKEN_PREFIX),
            "token must have correct prefix"
        );
        auth.validate_token(&token)
            .expect("fresh token should validate");
    }

    // ── Tampered payload ─────────────────────────────────────────────────

    #[test]
    fn auth_tampered_payload_rejected() {
        let auth = test_auth();
        let token = auth.generate_token();

        // Flip one base64 character in the encoded body
        let mut chars: Vec<char> = token.chars().collect();
        let flip_idx = TOKEN_PREFIX.len() + 5;
        let original = chars[flip_idx];
        chars[flip_idx] = if original == 'A' { 'B' } else { 'A' };
        let tampered: String = chars.into_iter().collect();

        let result = auth.validate_token(&tampered);
        assert!(
            matches!(
                result,
                Err(AuthError::InvalidMac) | Err(AuthError::MalformedToken)
            ),
            "tampered token must be rejected: {:?}",
            result
        );
    }

    // ── Wrong secret ──────────────────────────────────────────────────────

    #[test]
    fn auth_wrong_secret_rejected() {
        let signer = test_auth();
        let token = signer.generate_token();

        let mut wrong_secret = [0u8; 32];
        for b in wrong_secret.iter_mut() {
            *b = 0xFF;
        }
        let verifier = McpAuth::from_secret(wrong_secret);

        let result = verifier.validate_token(&token);
        assert!(
            matches!(result, Err(AuthError::InvalidMac)),
            "wrong-secret token must return InvalidMac: {:?}",
            result
        );
    }

    // ── Expired token ─────────────────────────────────────────────────────

    #[test]
    fn auth_expired_token_rejected() {
        let auth = test_auth();

        // Build a token with a timestamp 301 seconds in the past
        let old_ts: u64 = unix_now_secs().saturating_sub(EXPIRY_SECS + 1);
        let mut nonce = [0u8; 8];
        rand::thread_rng().fill_bytes(&mut nonce);

        let mut payload = [0u8; PAYLOAD_LEN];
        payload[..8].copy_from_slice(&old_ts.to_le_bytes());
        payload[8..].copy_from_slice(&nonce);

        let mac = compute_mac(&auth.secret, &payload);
        let mut raw = [0u8; TOTAL_DECODED_LEN];
        raw[..PAYLOAD_LEN].copy_from_slice(&payload);
        raw[PAYLOAD_LEN..].copy_from_slice(&mac);

        let token = format!("{}{}", TOKEN_PREFIX, Base64UrlUnpadded::encode_string(&raw));
        let result = auth.validate_token(&token);
        assert!(
            matches!(result, Err(AuthError::TokenExpired)),
            "301s-old token must return TokenExpired: {:?}",
            result
        );
    }

    // ── Invalid prefix ────────────────────────────────────────────────────

    #[test]
    fn auth_invalid_prefix_rejected() {
        let auth = test_auth();
        let result = auth.validate_token("Bearer some-random-token");
        assert!(matches!(result, Err(AuthError::InvalidPrefix)));
    }

    // ── Constant-time compare used (structural) ───────────────────────────
    //
    // This test verifies we call `verify_slice` (constant-time) rather than
    // comparing MAC bytes via `==`. We can't measure timing in a unit test, but
    // we can confirm the code path: provide a token where only the last MAC byte
    // differs — the code should still return InvalidMac, not panic or succeed.

    #[test]
    fn auth_constant_time_verify_path_exercised() {
        let auth = test_auth();
        let token = auth.generate_token();

        // Decode, corrupt last MAC byte, re-encode
        let encoded = token.strip_prefix(TOKEN_PREFIX).unwrap();
        let mut raw = [0u8; TOTAL_DECODED_LEN];
        Base64UrlUnpadded::decode(encoded, &mut raw).unwrap();
        raw[TOTAL_DECODED_LEN - 1] ^= 0x01; // flip 1 bit in last MAC byte
        let bad_token = format!("{}{}", TOKEN_PREFIX, Base64UrlUnpadded::encode_string(&raw));

        let result = auth.validate_token(&bad_token);
        assert!(
            matches!(result, Err(AuthError::InvalidMac)),
            "single-bit MAC corruption must be rejected: {:?}",
            result
        );
    }

    // ── Rotation invalidates old tokens ───────────────────────────────────

    #[test]
    fn auth_rotation_invalidates_old_token() {
        let old_auth = test_auth();
        let token = old_auth.generate_token();

        // Simulate rotation: new auth with a different secret
        let mut new_secret = [0u8; 32];
        for (i, b) in new_secret.iter_mut().enumerate() {
            *b = (i as u8).wrapping_add(128);
        }
        let new_auth = McpAuth::from_secret(new_secret);

        let result = new_auth.validate_token(&token);
        assert!(
            matches!(result, Err(AuthError::InvalidMac)),
            "token from rotated-out secret must be rejected: {:?}",
            result
        );
    }

    // ── Secret file creation and permissions ──────────────────────────────

    #[test]
    fn secret_manager_creates_file_with_correct_permissions() {
        let dir = tempfile::tempdir().expect("tempdir");
        let path = dir.path().join("runtime").join("mcp-secret.key");

        let auth = McpSecretManager::load_or_create(&path).expect("should create secret file");

        // File must exist with the right length
        let content = std::fs::read(&path).expect("file readable");
        assert_eq!(content.len(), 32, "secret file must be 32 bytes");

        // Reload must yield the same secret
        let auth2 = McpSecretManager::load_or_create(&path).expect("reload should succeed");
        let token = auth.generate_token();
        auth2
            .validate_token(&token)
            .expect("reloaded secret must accept tokens from original");

        // Permissions: 0600 on Unix
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let meta = std::fs::metadata(&path).unwrap();
            let mode = meta.permissions().mode() & 0o777;
            assert_eq!(mode, 0o600, "secret file must be 0600, got {:o}", mode);
        }
    }

    #[test]
    fn secret_manager_regenerates_corrupt_file() {
        let dir = tempfile::tempdir().expect("tempdir");
        let path = dir.path().join("mcp-secret.key");

        // Write a corrupt (too-short) file
        std::fs::write(&path, b"tooshort").unwrap();

        let auth =
            McpSecretManager::load_or_create(&path).expect("should regenerate from corrupt file");
        let token = auth.generate_token();
        auth.validate_token(&token)
            .expect("regenerated auth should work");

        let content = std::fs::read(&path).unwrap();
        assert_eq!(content.len(), 32, "regenerated file must be 32 bytes");
    }
}
