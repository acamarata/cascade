//! # cascade-personal::crypto
//!
//! Purpose: AES-256-GCM encrypt/decrypt for personal vault records.
//!
//! Wire format: `nonce (12 bytes) || ciphertext (variable) || tag (16 bytes)`
//! The tag is appended by `aes-gcm` and verified on decrypt; the caller stores
//! the entire blob (nonce + ct + tag) as a single BLOB column.
//!
//! Inputs: 32-byte DEK, plaintext bytes (encrypt) or sealed blob (decrypt).
//! Outputs: sealed blob (encrypt) or plaintext bytes (decrypt).
//!
//! Constraints:
//! - Every record uses a fresh 12-byte random nonce (AES-GCM nonce reuse under
//!   the same key is catastrophic; per-call CSPRng guarantees uniqueness).
//! - DEK bytes MUST NOT appear in tracing or error output.
//! - In tests, a clearly-labelled test-only DEK is used when no keychain is wired.
//!
//! SPORT: cascade-personal / crypto

use aes_gcm::{
    aead::{Aead, AeadCore, KeyInit, OsRng},
    Aes256Gcm, Key, Nonce,
};

use crate::vault::VaultError;

/// Size of the AES-256-GCM nonce prepended to every blob.
const NONCE_LEN: usize = 12;

/// Encrypt `plaintext` with the provided 32-byte DEK.
///
/// # Purpose
/// Produces a sealed blob `nonce(12) || ciphertext || tag(16)` suitable for
/// storage in the `records.ciphertext` BLOB column.
///
/// # Inputs
/// - `dek`: 32-byte data-encryption key.  NEVER log or surface in errors.
/// - `plaintext`: arbitrary bytes to protect.
///
/// # Outputs
/// `Vec<u8>` — sealed blob.
///
/// # Constraints
/// Each call generates a fresh random nonce via OS CSPRNG.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / crypto::seal
pub fn seal(dek: &[u8; 32], plaintext: &[u8]) -> Result<Vec<u8>, VaultError> {
    let key = Key::<Aes256Gcm>::from_slice(dek);
    let cipher = Aes256Gcm::new(key);
    let nonce = Aes256Gcm::generate_nonce(&mut OsRng);
    let ct = cipher
        .encrypt(&nonce, plaintext)
        .map_err(|_| VaultError::Crypto("encryption failed".into()))?;
    let mut blob = Vec::with_capacity(NONCE_LEN + ct.len());
    blob.extend_from_slice(&nonce);
    blob.extend_from_slice(&ct);
    Ok(blob)
}

/// Decrypt a sealed blob produced by [`seal`].
///
/// # Purpose
/// Verifies the AEAD tag and returns the original plaintext.
///
/// # Inputs
/// - `dek`: 32-byte data-encryption key.
/// - `blob`: `nonce(12) || ciphertext || tag(16)` as stored in the DB.
///
/// # Outputs
/// Plaintext bytes on success; `VaultError::Crypto` if the tag fails or the blob
/// is malformed.
///
/// # Constraints
/// DEK bytes MUST NOT appear in error messages.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / crypto::open
pub fn open(dek: &[u8; 32], blob: &[u8]) -> Result<Vec<u8>, VaultError> {
    if blob.len() < NONCE_LEN {
        return Err(VaultError::Crypto("blob too short to contain nonce".into()));
    }
    let (nonce_bytes, ct) = blob.split_at(NONCE_LEN);
    let key = Key::<Aes256Gcm>::from_slice(dek);
    let cipher = Aes256Gcm::new(key);
    let nonce = Nonce::from_slice(nonce_bytes);
    cipher
        .decrypt(nonce, ct)
        .map_err(|_| VaultError::Crypto("decryption failed (bad key or corrupted blob)".into()))
}

/// A clearly-labelled 32-byte test DEK — ONLY for `cfg(test)`.
///
/// This constant is intentionally not exported outside the crate and must never
/// be used in production paths.
#[cfg(test)]
pub(crate) const TEST_DEK: [u8; 32] = *b"cascade-personal-test-dek-00000\x00";

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn seal_open_roundtrip() {
        let plaintext = b"ssn:123-45-6789";
        let blob = seal(&TEST_DEK, plaintext).expect("seal");
        let recovered = open(&TEST_DEK, &blob).expect("open");
        assert_eq!(recovered, plaintext);
    }

    #[test]
    fn seal_produces_ciphertext_not_plaintext() {
        let plaintext = b"ssn:123-45-6789";
        let blob = seal(&TEST_DEK, plaintext).expect("seal");
        // The blob must not contain the plaintext as a substring.
        assert!(
            !blob.windows(plaintext.len()).any(|w| w == plaintext),
            "plaintext must not appear in ciphertext blob"
        );
    }

    #[test]
    fn each_seal_uses_unique_nonce() {
        let plaintext = b"data";
        let b1 = seal(&TEST_DEK, plaintext).unwrap();
        let b2 = seal(&TEST_DEK, plaintext).unwrap();
        // Different nonces → different blobs even for same plaintext.
        assert_ne!(&b1[..NONCE_LEN], &b2[..NONCE_LEN], "nonces must differ");
        assert_ne!(b1, b2, "blobs must differ");
    }

    #[test]
    fn tampered_blob_fails_auth() {
        let plaintext = b"sensitive data";
        let mut blob = seal(&TEST_DEK, plaintext).unwrap();
        // Flip a byte in the ciphertext region (after nonce).
        blob[NONCE_LEN] ^= 0xFF;
        let err = open(&TEST_DEK, &blob).unwrap_err();
        assert!(matches!(err, VaultError::Crypto(_)));
    }

    #[test]
    fn short_blob_returns_error() {
        let blob = vec![0u8; 4]; // shorter than NONCE_LEN
        let err = open(&TEST_DEK, &blob).unwrap_err();
        assert!(matches!(err, VaultError::Crypto(_)));
    }

    #[test]
    fn wrong_key_fails_auth() {
        let plaintext = b"private";
        let blob = seal(&TEST_DEK, plaintext).unwrap();
        let mut wrong_key = TEST_DEK;
        wrong_key[0] ^= 0x01;
        let err = open(&wrong_key, &blob).unwrap_err();
        assert!(matches!(err, VaultError::Crypto(_)));
    }
}
