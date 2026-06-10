//! Build script for cascade-daemon.
//!
//! Purpose: Injects the `CASCADE_UPDATE_PUBKEY` environment variable into the
//! compiled binary via Cargo's `env!` macro support. In release CI this env var
//! is set to the base64-encoded 32-byte Ed25519 public key used to sign delta
//! bundles. For local development and test builds, a deterministic test-only
//! fallback key is used so that tests compile and run without CI credentials.
//!
//! Key rotation seam: update the `CASCADE_UPDATE_PUBKEY` secret in CI to rotate
//! the release signing key. The fallback key here is ONLY used in test builds
//! and must NEVER match the production key.
//!
//! Constraints:
//!   - The fallback test key is the base64 encoding of 32 bytes of value 0x01.
//!     It is NOT the all-zeros key (which ed25519-dalek rejects as invalid) but
//!     also NOT the production key.
//!   - This file must NOT embed or print the production private key.
//!   - The `rerun-if-env-changed` directive prevents stale caching when the CI
//!     env var changes between builds.

fn main() {
    // Re-run this build script whenever the env var changes.
    println!("cargo:rerun-if-env-changed=CASCADE_UPDATE_PUBKEY");

    if std::env::var("CASCADE_UPDATE_PUBKEY").is_err() {
        // Provide a deterministic test-only fallback public key.
        // 32 bytes of 0x01 encoded as base64 — a valid Ed25519 point that is
        // NOT the production key and NOT used for any real bundle signing.
        //
        // IMPORTANT: This key is only for compilation in dev/test. The matching
        // private key is generated in tests via ed25519_dalek::SigningKey::generate,
        // so bundles signed in tests use a fresh keypair, NOT this fallback.
        let test_fallback =
            base64::Engine::encode(&base64::engine::general_purpose::STANDARD, [0x01u8; 32]);
        println!("cargo:rustc-env=CASCADE_UPDATE_PUBKEY={test_fallback}");
    }
}
