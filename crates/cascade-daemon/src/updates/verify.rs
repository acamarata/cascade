//! Ed25519 signature verification for `.cascade-delta` bundles.
//!
//! Purpose: Verifies the integrity and authenticity of a `DeltaBundle` by:
//!   1. Verifying the Ed25519 signature over SHA-256(manifest.json bytes).
//!   2. Verifying each file's blake3 hash against the hash in the manifest.
//!
//! The public key is embedded at compile time via the `CASCADE_UPDATE_PUBKEY`
//! environment variable (base64-encoded 32-byte Ed25519 public key). For
//! non-release builds (env var unset), a deterministic test-only key is used.
//!
//! Inputs:
//!   - `DeltaBundle` — parsed bundle with manifest bytes, signature bytes, and file map
//!
//! Outputs:
//!   - `Ok(())` — bundle is authentic and all file hashes are correct
//!   - `Err(UpdateError::InvalidSignature)` — signature verification failed
//!   - `Err(UpdateError::HashMismatch { path })` — a file's content doesn't match its manifest hash
//!
//! Constraints:
//!   - Uses `verify_strict` (not `verify`) to prevent signature malleability attacks.
//!   - Hash-then-sign: SHA-256 of `manifest_bytes` is the message, NOT the manifest itself,
//!     consistent with how release CI will produce bundles.
//!   - Key rotation seam: `get_embedded_pubkey()` is the single extraction point — swap
//!     only that function to add multi-key or versioned-key rotation.
//!
//! SPORT: MASTER-COMPONENTS.md — SignatureVerifier (T-P4-E04-13)

use base64::Engine as _;
use ed25519_dalek::{Signature, VerifyingKey};
use sha2::{Digest, Sha256};
use thiserror::Error;

use crate::updates::bundle::DeltaBundle;

// ── Embedded public key ───────────────────────────────────────────────────────

/// The embedded Ed25519 public key (base64-encoded 32 bytes).
///
/// In release builds this is set via the `CASCADE_UPDATE_PUBKEY` env var at
/// compile time (injected by release CI). In dev/test builds the env var is
/// unset and this constant falls back to a deterministic test-only key
/// (the all-zeros key is NOT used; see build.rs for the fallback).
///
/// Key rotation seam: replace only this constant (or switch to a build.rs
/// lookup table for multi-version key pinning).
const EMBEDDED_PUBKEY_B64: &str = env!(
    "CASCADE_UPDATE_PUBKEY",
    "Set CASCADE_UPDATE_PUBKEY (base64 Ed25519 public key) for release builds. \
     For dev builds run: export CASCADE_UPDATE_PUBKEY=$(cargo test --test ... -- --list 2>&1 \
     | grep key) or use the test helper in verify.rs tests."
);

/// Decode and return the embedded verifying key.
///
/// Purpose: Single extraction point for the public key — swap this function
/// to add versioned-key rotation without touching verification logic.
fn get_embedded_pubkey() -> Result<VerifyingKey, UpdateError> {
    let key_bytes = base64::engine::general_purpose::STANDARD
        .decode(EMBEDDED_PUBKEY_B64)
        .map_err(|e| UpdateError::InvalidPublicKey(format!("base64 decode: {e}")))?;

    let arr: [u8; 32] = key_bytes
        .try_into()
        .map_err(|_| UpdateError::InvalidPublicKey("expected 32 bytes".to_string()))?;

    VerifyingKey::from_bytes(&arr)
        .map_err(|e| UpdateError::InvalidPublicKey(format!("key parse: {e}")))
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors returned by `verify_bundle`.
#[derive(Debug, Error)]
pub enum UpdateError {
    /// Ed25519 signature over the manifest digest did not verify.
    #[error("bundle signature is invalid")]
    InvalidSignature,

    /// A file's blake3 hash does not match the manifest entry.
    #[error("hash mismatch for file: {path}")]
    HashMismatch { path: String },

    /// The embedded public key could not be decoded.
    #[error("invalid embedded public key: {0}")]
    InvalidPublicKey(String),

    /// The `signature.bin` payload is not exactly 64 bytes.
    #[error("signature.bin must be 64 bytes, got {0}")]
    InvalidSignatureLength(usize),
}

// ── Verification ──────────────────────────────────────────────────────────────

/// Verify the authenticity and integrity of a `DeltaBundle`.
///
/// Purpose: Two-stage verification —
///   1. Ed25519 signature check: `verify_strict(SHA-256(manifest_bytes), signature.bin)`.
///   2. Per-file blake3 hash check: each file in `bundle.files` is hashed and compared
///      against its entry in `bundle.manifest.files`.
///
/// Inputs:  `bundle` — parsed DeltaBundle from `DeltaBundle::from_tarball`
/// Outputs: `Ok(())` on success; typed error on any failure
pub fn verify_bundle(bundle: &DeltaBundle) -> Result<(), UpdateError> {
    // ── 1. Signature verification ─────────────────────────────────────────────

    let sig_len = bundle.signature_bytes.len();
    if sig_len != 64 {
        return Err(UpdateError::InvalidSignatureLength(sig_len));
    }
    let sig_arr: [u8; 64] = bundle.signature_bytes[..].try_into().expect("len==64");
    let signature = Signature::from_bytes(&sig_arr);

    // SHA-256 of the raw manifest.json bytes — this is the message that was signed.
    let digest = Sha256::digest(&bundle.manifest_bytes);

    let pubkey = get_embedded_pubkey()?;

    // Use verify_strict to prevent signature malleability attacks.
    pubkey
        .verify_strict(digest.as_slice(), &signature)
        .map_err(|_| UpdateError::InvalidSignature)?;

    // ── 2. Per-file hash verification ─────────────────────────────────────────

    for file_entry in &bundle.manifest.files {
        let content = bundle.files.get(&file_entry.path).ok_or_else(|| {
            // File listed in manifest but not present in bundle payload.
            UpdateError::HashMismatch {
                path: file_entry.path.clone(),
            }
        })?;

        let actual_hash = blake3::hash(content);
        let actual_hex = actual_hash.to_hex().to_string();

        if actual_hex != file_entry.hash {
            return Err(UpdateError::HashMismatch {
                path: file_entry.path.clone(),
            });
        }
    }

    Ok(())
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::updates::bundle::{DeltaBundle, FileEntry, Manifest};
    use ed25519_dalek::{Signer, SigningKey};
    use rand::rngs::OsRng;
    use std::collections::HashMap;

    /// Generate a fresh Ed25519 keypair for testing.
    /// Returns (signing_key, verifying_key_b64, verifying_key_bytes).
    fn gen_test_keypair() -> (SigningKey, String) {
        let signing_key = SigningKey::generate(&mut OsRng);
        let verifying_bytes = signing_key.verifying_key().to_bytes();
        let pub_b64 = base64::engine::general_purpose::STANDARD.encode(verifying_bytes);
        (signing_key, pub_b64)
    }

    /// Build a `DeltaBundle` from scratch with a real signature.
    fn make_signed_bundle(
        signing_key: &SigningKey,
        payload_files: &[(&str, &[u8])],
    ) -> DeltaBundle {
        let file_entries: Vec<FileEntry> = payload_files
            .iter()
            .map(|(path, content)| FileEntry {
                path: path.to_string(),
                hash: blake3::hash(content).to_hex().to_string(),
            })
            .collect();

        let manifest = Manifest {
            version: "0.1.2".into(),
            target_version: "0.1.3".into(),
            files: file_entries,
            signature: None,
        };

        let manifest_bytes = serde_json::to_vec(&manifest).expect("serialize manifest");
        let digest = Sha256::digest(&manifest_bytes);
        let signature = signing_key.sign(digest.as_slice());

        let mut files = HashMap::new();
        for (path, content) in payload_files {
            files.insert(path.to_string(), content.to_vec());
        }

        DeltaBundle {
            manifest,
            manifest_bytes,
            signature_bytes: signature.to_bytes().to_vec(),
            files,
        }
    }

    /// Helper: verify a bundle using a specific verifying key (bypasses EMBEDDED_PUBKEY_B64).
    fn verify_with_key(
        bundle: &DeltaBundle,
        verifying_key: &VerifyingKey,
    ) -> Result<(), UpdateError> {
        let sig_len = bundle.signature_bytes.len();
        if sig_len != 64 {
            return Err(UpdateError::InvalidSignatureLength(sig_len));
        }
        let sig_arr: [u8; 64] = bundle.signature_bytes[..].try_into().expect("len==64");
        let signature = Signature::from_bytes(&sig_arr);
        let digest = Sha256::digest(&bundle.manifest_bytes);

        verifying_key
            .verify_strict(digest.as_slice(), &signature)
            .map_err(|_| UpdateError::InvalidSignature)?;

        for file_entry in &bundle.manifest.files {
            let content =
                bundle
                    .files
                    .get(&file_entry.path)
                    .ok_or_else(|| UpdateError::HashMismatch {
                        path: file_entry.path.clone(),
                    })?;
            let actual_hex = blake3::hash(content).to_hex().to_string();
            if actual_hex != file_entry.hash {
                return Err(UpdateError::HashMismatch {
                    path: file_entry.path.clone(),
                });
            }
        }
        Ok(())
    }

    #[test]
    fn valid_signature_passes() {
        let (sk, _) = gen_test_keypair();
        let payload = b"# RAG Pipeline spec";
        let bundle = make_signed_bundle(&sk, &[("specs/rag-pipeline.md", payload)]);

        let result = verify_with_key(&bundle, &sk.verifying_key());
        assert!(result.is_ok(), "valid bundle should pass: {result:?}");
    }

    #[test]
    fn mutated_manifest_fails() {
        let (sk, _) = gen_test_keypair();
        let payload = b"# spec content";
        let mut bundle = make_signed_bundle(&sk, &[("specs/spec.md", payload)]);

        // Flip one byte in the manifest bytes — signature must fail.
        bundle.manifest_bytes[10] ^= 0xFF;

        let result = verify_with_key(&bundle, &sk.verifying_key());
        assert!(
            matches!(result, Err(UpdateError::InvalidSignature)),
            "mutated manifest should fail with InvalidSignature, got: {result:?}"
        );
    }

    #[test]
    fn wrong_key_fails() {
        let (sk, _) = gen_test_keypair();
        let (other_sk, _) = gen_test_keypair();
        let payload = b"# spec content";
        let bundle = make_signed_bundle(&sk, &[("specs/spec.md", payload)]);

        // Verify with a different (wrong) key.
        let result = verify_with_key(&bundle, &other_sk.verifying_key());
        assert!(
            matches!(result, Err(UpdateError::InvalidSignature)),
            "wrong key should fail with InvalidSignature, got: {result:?}"
        );
    }

    #[test]
    fn file_hash_mismatch_fails() {
        let (sk, _) = gen_test_keypair();
        let payload = b"# spec content";
        let mut bundle = make_signed_bundle(&sk, &[("specs/spec.md", payload)]);

        // Tamper with file content AFTER signing — hash check must fail.
        bundle
            .files
            .insert("specs/spec.md".to_string(), b"tampered content".to_vec());

        let result = verify_with_key(&bundle, &sk.verifying_key());
        assert!(
            matches!(result, Err(UpdateError::HashMismatch { ref path }) if path == "specs/spec.md"),
            "hash mismatch should fail, got: {result:?}"
        );
    }

    #[test]
    fn invalid_signature_length_fails() {
        let (sk, _) = gen_test_keypair();
        let mut bundle = make_signed_bundle(&sk, &[]);
        // Truncate signature to wrong length.
        bundle.signature_bytes = vec![0u8; 32];

        let result = verify_with_key(&bundle, &sk.verifying_key());
        assert!(
            matches!(result, Err(UpdateError::InvalidSignatureLength(32))),
            "wrong length should fail, got: {result:?}"
        );
    }

    #[test]
    fn multiple_files_all_verified() {
        let (sk, _) = gen_test_keypair();
        let files: &[(&str, &[u8])] = &[
            ("specs/rag.md", b"rag spec"),
            ("specs/mcp.md", b"mcp spec"),
            ("specs/auth.md", b"auth spec"),
        ];
        let bundle = make_signed_bundle(&sk, files);
        let result = verify_with_key(&bundle, &sk.verifying_key());
        assert!(result.is_ok(), "all files valid: {result:?}");
    }

    #[test]
    fn missing_file_in_payload_fails() {
        let (sk, _) = gen_test_keypair();
        let payload = b"# spec";
        let mut bundle = make_signed_bundle(&sk, &[("specs/spec.md", payload)]);

        // Remove the file from the payload map.
        bundle.files.remove("specs/spec.md");

        let result = verify_with_key(&bundle, &sk.verifying_key());
        assert!(
            matches!(result, Err(UpdateError::HashMismatch { .. })),
            "missing payload file should fail: {result:?}"
        );
    }
}
