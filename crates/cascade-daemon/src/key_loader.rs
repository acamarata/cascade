//! key_loader — Load API keys from the OS keychain with vault.env fallback.
//!
//! Purpose: Provides `load_api_keys()` for daemon startup and `load_from_keychain()`
//! for testable key loading. Keys are never logged at any level.
//! Inputs: OS keychain (via cascade-keychain crate) or GEMINI_API_KEY_{n} env vars.
//! Outputs: Vec<SecretString> — zeroized on drop.
//! Constraints: NEVER log key values. Break on first NotFound. Warn on other errors.
//! SPORT: cascade-daemon key management.

use cascade_keychain::{platform_keychain, KeychainError};
use secrecy::SecretString;

/// Load API keys from the provided keychain implementation.
///
/// Purpose: Testable core — inject any Keychain (including InMemoryKeychain in tests).
/// Inputs: `kc` — reference to any Keychain implementation.
/// Outputs: Vec of SecretString keys loaded from "dev.cascade" / "gemini_key_{n}".
/// Constraints: Breaks on first NotFound; warns on other errors; never logs values.
pub fn load_from_keychain(kc: &dyn cascade_keychain::Keychain) -> Vec<SecretString> {
    let mut keys = Vec::new();
    for n in 1..=28 {
        match kc.get_key("dev.cascade", &format!("gemini_key_{n}")) {
            Ok(k) => keys.push(SecretString::new(k)),
            Err(KeychainError::NotFound) => break,
            Err(e) => tracing::warn!("keychain read error for slot {n}: {e}"),
        }
    }
    keys
}

/// Load API keys at daemon startup.
///
/// Purpose: Production entry point — uses platform_keychain(), falls back to vault.env.
/// Outputs: Vec of SecretString keys; logs count and source at INFO level.
/// Constraints: Never logs key values. Source label indicates "OS keychain" or "vault.env fallback".
// Called in main.rs under #[cfg(feature = "gemini-proxy")] — warning is a feature-gate FP.
#[allow(dead_code)]
pub fn load_api_keys() -> Vec<SecretString> {
    let kc = platform_keychain();
    let mut keys = load_from_keychain(kc.as_ref());
    let from_keychain = !keys.is_empty();
    if keys.is_empty() {
        for n in 1..=28 {
            match std::env::var(format!("GEMINI_API_KEY_{n}")) {
                Ok(v) => keys.push(SecretString::new(v)),
                Err(_) => break,
            }
        }
    }
    let source = if from_keychain {
        "OS keychain"
    } else {
        "vault.env fallback"
    };
    tracing::info!(count = keys.len(), source, "API keys loaded");
    keys
}

/// Whether the daemon should advise the user to run `cascade migrate-keys`.
///
/// Purpose: nudge users still holding plaintext keys in vault.env to migrate
///   them into the OS keychain — but only when migration is actually pending
///   (keychain has no keys AND vault.env-style env vars are present).
/// Outputs: `true` when the OS keychain is empty but `GEMINI_API_KEY_1` is set
///   in the environment; `false` otherwise.
/// Constraints: reads no secret values; safe to call at startup.
pub fn should_advise_migration() -> bool {
    let kc = platform_keychain();
    let keychain_empty = load_from_keychain(kc.as_ref()).is_empty();
    let vault_has_keys = std::env::var("GEMINI_API_KEY_1").is_ok();
    keychain_empty && vault_has_keys
}

#[cfg(test)]
mod tests {
    use super::*;
    // The `Keychain` trait must be in scope to call its methods (set_key/get_key)
    // on the concrete `InMemoryKeychain` — unlike the `&dyn Keychain` call sites
    // above, which dispatch through the trait object without needing the import.
    use cascade_keychain::{InMemoryKeychain, Keychain};

    #[test]
    fn load_from_keychain_returns_injected_keys() {
        let kc = InMemoryKeychain::new();
        for n in 1..=3 {
            kc.set_key(
                "dev.cascade",
                &format!("gemini_key_{n}"),
                &format!("secret{n}"),
            )
            .unwrap();
        }
        let keys = load_from_keychain(&kc);
        assert_eq!(keys.len(), 3);
    }

    #[test]
    fn load_from_keychain_stops_at_not_found() {
        let kc = InMemoryKeychain::new();
        // Only set slots 1 and 2 — slot 3 is absent, should stop there
        kc.set_key("dev.cascade", "gemini_key_1", "s1").unwrap();
        kc.set_key("dev.cascade", "gemini_key_2", "s2").unwrap();
        let keys = load_from_keychain(&kc);
        assert_eq!(keys.len(), 2);
    }

    #[test]
    fn load_from_keychain_empty_keychain_returns_empty() {
        let kc = InMemoryKeychain::new();
        let keys = load_from_keychain(&kc);
        assert!(keys.is_empty());
    }
}
