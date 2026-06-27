//! Plugin signing — Ed25519 publisher trust and signature verification.
//!
//! Purpose: verify plugin signatures against a trusted-publishers registry;
//!   warn and optionally block unsigned plugins.
//! Inputs: plugin directory, trusted-publishers.json path, optional override flag.
//! Outputs: VerifyResult (trusted / unsigned-warn / unsigned-blocked), SigningError.
//! Constraints: trusted-publishers.json lives at ~/.cascade/trusted-publishers.json;
//!   plugin signatures are stored as <entry_wasm>.sig (raw Ed25519, base64-encoded);
//!   unsigned plugins WARN and are blocked unless --allow-unsigned is set.
//! SPORT: cascade-plugins / signing (plug-01)

use std::path::{Path, PathBuf};

use base64::Engine;
use ed25519_dalek::{Signature, VerifyingKey};
use serde::{Deserialize, Serialize};
use thiserror::Error;

#[derive(Debug, Error)]
pub enum SigningError {
    #[error("failed to read {path}: {source}")]
    Io { path: PathBuf, source: std::io::Error },
    #[error("failed to parse trusted-publishers.json at {path}: {source}")]
    Parse { path: PathBuf, source: serde_json::Error },
    #[error("plugin '{id}' is unsigned; install with --allow-unsigned to override")]
    Unsigned { id: String },
    #[error("plugin '{id}' signature verification failed: {reason}")]
    BadSignature { id: String, reason: String },
    #[error("publisher key '{key}' for plugin '{id}' is not valid base64-encoded Ed25519: {reason}")]
    BadKey { id: String, key: String, reason: String },
}

/// Outcome of signature verification.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum VerifyResult {
    /// Plugin is signed by a trusted publisher.
    Trusted { publisher: String },
    /// Plugin has no signature; blocked unless override flag is set.
    UnsignedBlocked,
    /// Plugin has no signature; warning emitted but allowed by override.
    UnsignedWarned,
}

/// A trusted publisher entry.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TrustedPublisher {
    /// Human-readable publisher name.
    pub name: String,
    /// Base64-encoded Ed25519 public key (32 bytes → 44 base64 chars).
    pub public_key: String,
}

/// The trusted-publishers registry.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TrustedPublishers {
    pub publishers: Vec<TrustedPublisher>,
}

impl TrustedPublishers {
    /// Load from `~/.cascade/trusted-publishers.json`.
    pub fn load() -> Result<Self, SigningError> {
        Self::load_from(&publishers_path())
    }

    /// Load from an explicit path (for tests).
    pub fn load_from(path: &Path) -> Result<Self, SigningError> {
        if !path.exists() {
            return Ok(Self::default());
        }
        let bytes = std::fs::read(path)
            .map_err(|source| SigningError::Io { path: path.to_owned(), source })?;
        serde_json::from_slice(&bytes)
            .map_err(|source| SigningError::Parse { path: path.to_owned(), source })
    }

    /// Add a trusted publisher and persist.
    pub fn trust(&mut self, name: String, public_key: String) -> Result<(), SigningError> {
        // Validate the key is parseable before storing.
        let _ = decode_key(&public_key, "test", &name)?;
        self.publishers.push(TrustedPublisher { name, public_key });
        self.save()
    }

    fn save(&self) -> Result<(), SigningError> {
        let path = publishers_path();
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).ok();
        }
        let json = serde_json::to_vec_pretty(self).expect("infallible");
        std::fs::write(&path, json)
            .map_err(|source| SigningError::Io { path, source })
    }
}

/// Verify a plugin WASM file against all trusted publisher keys.
///
/// Looks for `<wasm_path>.sig` (raw bytes, base64-encoded Ed25519 signature).
/// If no sig file exists, returns `UnsignedBlocked` (or `UnsignedWarned` if `allow_unsigned`).
pub fn verify_plugin(
    plugin_id: &str,
    wasm_path: &Path,
    publishers: &TrustedPublishers,
    allow_unsigned: bool,
) -> Result<VerifyResult, SigningError> {
    use ed25519_dalek::Verifier;

    let sig_path = PathBuf::from(format!("{}.sig", wasm_path.display()));

    if !sig_path.exists() {
        if allow_unsigned {
            tracing::warn!(plugin_id, "plugin is unsigned (allowed by --allow-unsigned)");
            return Ok(VerifyResult::UnsignedWarned);
        } else {
            return Err(SigningError::Unsigned { id: plugin_id.to_owned() });
        }
    }

    // Read the WASM bytes to verify.
    let wasm_bytes = std::fs::read(wasm_path)
        .map_err(|source| SigningError::Io { path: wasm_path.to_owned(), source })?;

    // Read and decode the signature.
    let sig_b64 = std::fs::read_to_string(&sig_path)
        .map_err(|source| SigningError::Io { path: sig_path.clone(), source })?;
    let sig_b64 = sig_b64.trim();

    let sig_bytes = base64::engine::general_purpose::STANDARD
        .decode(sig_b64)
        .map_err(|e| SigningError::BadSignature {
            id: plugin_id.to_owned(),
            reason: format!("base64 decode failed: {e}"),
        })?;

    let sig = Signature::from_slice(&sig_bytes).map_err(|e| SigningError::BadSignature {
        id: plugin_id.to_owned(),
        reason: format!("invalid signature bytes: {e}"),
    })?;

    // Try each trusted publisher key.
    for publisher in &publishers.publishers {
        let key = match decode_key(&publisher.public_key, plugin_id, &publisher.name) {
            Ok(k) => k,
            Err(_) => continue,
        };
        if key.verify(&wasm_bytes, &sig).is_ok() {
            return Ok(VerifyResult::Trusted { publisher: publisher.name.clone() });
        }
    }

    // Signature present but not matched by any trusted key.
    Err(SigningError::BadSignature {
        id: plugin_id.to_owned(),
        reason: "signature does not match any trusted publisher key".to_owned(),
    })
}

fn decode_key(b64: &str, plugin_id: &str, key_name: &str) -> Result<VerifyingKey, SigningError> {
    let key_bytes = base64::engine::general_purpose::STANDARD
        .decode(b64)
        .map_err(|e| SigningError::BadKey {
            id: plugin_id.to_owned(),
            key: key_name.to_owned(),
            reason: format!("base64: {e}"),
        })?;

    let arr: [u8; 32] = key_bytes.try_into().map_err(|_| SigningError::BadKey {
        id: plugin_id.to_owned(),
        key: key_name.to_owned(),
        reason: "key must be exactly 32 bytes".to_owned(),
    })?;

    VerifyingKey::from_bytes(&arr).map_err(|e| SigningError::BadKey {
        id: plugin_id.to_owned(),
        key: key_name.to_owned(),
        reason: e.to_string(),
    })
}

fn publishers_path() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".cascade")
        .join("trusted-publishers.json")
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::Engine;
    use ed25519_dalek::SigningKey;
    use tempfile::TempDir;

    fn make_keypair() -> (SigningKey, VerifyingKey) {
        use rand::RngCore;
        let mut bytes = [0u8; 32];
        rand::rngs::OsRng.fill_bytes(&mut bytes);
        let sk = SigningKey::from_bytes(&bytes);
        let vk = sk.verifying_key();
        (sk, vk)
    }

    fn pub_key_b64(vk: &VerifyingKey) -> String {
        base64::engine::general_purpose::STANDARD.encode(vk.as_bytes())
    }

    fn sign_bytes(sk: &SigningKey, bytes: &[u8]) -> String {
        use ed25519_dalek::Signer;
        let sig = sk.sign(bytes);
        base64::engine::general_purpose::STANDARD.encode(sig.to_bytes())
    }

    #[test]
    fn unsigned_plugin_blocked() {
        let tmp = TempDir::new().unwrap();
        let wasm_path = tmp.path().join("plugin.wasm");
        std::fs::write(&wasm_path, b"(module)").unwrap();
        let publishers = TrustedPublishers::default();
        let result = verify_plugin("com.ex.p", &wasm_path, &publishers, false);
        assert!(matches!(result, Err(SigningError::Unsigned { .. })));
    }

    #[test]
    fn unsigned_plugin_warned_when_allowed() {
        let tmp = TempDir::new().unwrap();
        let wasm_path = tmp.path().join("plugin.wasm");
        std::fs::write(&wasm_path, b"(module)").unwrap();
        let publishers = TrustedPublishers::default();
        let result = verify_plugin("com.ex.p", &wasm_path, &publishers, true);
        assert_eq!(result.unwrap(), VerifyResult::UnsignedWarned);
    }

    #[test]
    fn valid_signature_from_trusted_publisher() {
        let tmp = TempDir::new().unwrap();
        let (sk, vk) = make_keypair();
        let wasm_bytes = b"(module)";
        let wasm_path = tmp.path().join("plugin.wasm");
        std::fs::write(&wasm_path, wasm_bytes).unwrap();

        // Write signature file.
        let sig_b64 = sign_bytes(&sk, wasm_bytes);
        let sig_path = tmp.path().join("plugin.wasm.sig");
        std::fs::write(&sig_path, &sig_b64).unwrap();

        let publishers = TrustedPublishers {
            publishers: vec![TrustedPublisher {
                name: "Trusted Author".to_owned(),
                public_key: pub_key_b64(&vk),
            }],
        };

        let result = verify_plugin("com.ex.p", &wasm_path, &publishers, false);
        assert!(matches!(result, Ok(VerifyResult::Trusted { .. })), "{result:?}");
    }

    #[test]
    fn bad_signature_rejected() {
        let tmp = TempDir::new().unwrap();
        let (sk, vk) = make_keypair();
        let wasm_path = tmp.path().join("plugin.wasm");
        std::fs::write(&wasm_path, b"(module)").unwrap();

        // Sign different bytes (simulating tampered WASM).
        let sig_b64 = sign_bytes(&sk, b"different content");
        std::fs::write(tmp.path().join("plugin.wasm.sig"), sig_b64).unwrap();

        let publishers = TrustedPublishers {
            publishers: vec![TrustedPublisher {
                name: "Trusted".to_owned(),
                public_key: pub_key_b64(&vk),
            }],
        };

        let result = verify_plugin("com.ex.p", &wasm_path, &publishers, false);
        assert!(matches!(result, Err(SigningError::BadSignature { .. })), "{result:?}");
    }
}
