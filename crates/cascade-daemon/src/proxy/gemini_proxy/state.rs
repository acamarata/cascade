//! Routing-state construction and credential resolution.
//!
//! Purpose: load providers.json, build RoutingTable + credentials map,
//! and provide the single authoritative API-key lookup path.
//! SPORT: proxy credential resolution (T-P3-E00-02)

use std::collections::HashMap;

use secrecy::{ExposeSecret, SecretString};
use tracing::debug;

use cascade_core::{providers_store::read_providers_store, routing_table::RoutingTable};

// ── Build routing state from providers path ───────────────────────────────────

/// Load `providers.json`, build a `RoutingTable` and credentials map for all
/// enabled `gemini` harness entries.
///
/// Inputs:  `providers_path` — path to `~/.cascade/providers.json`.
/// Outputs: `(RoutingTable, HashMap<slot_id, api_key>)`.
///          On any read/parse error returns an empty table + empty map (non-fatal).
pub fn build_routing_state(
    providers_path: &std::path::Path,
) -> (RoutingTable, HashMap<String, String>) {
    match read_providers_store(providers_path) {
        Err(e) => {
            debug!(error = %e, path = %providers_path.display(),
                "gemini_proxy: providers.json not readable — empty routing table");
            (RoutingTable::new(&[]), HashMap::new())
        }
        Ok(store) => {
            // Only API-key providers are valid for the proxy routing table.
            // OAuth providers (auth_kind == "OAuthToken") carry a client_id in
            // account_id, not an API key, so they must be excluded to avoid
            // forwarding requests with an invalid key.
            let api_key_providers: Vec<_> = store
                .providers
                .iter()
                .filter(|p| p.enabled && p.harness == "gemini" && p.auth_kind == "ApiKey")
                .cloned()
                .collect();
            let table = RoutingTable::new(&api_key_providers);
            let creds: HashMap<String, String> = api_key_providers
                .iter()
                .map(|p| (p.id.clone(), p.account_id.clone()))
                .collect();
            (table, creds)
        }
    }
}

// ── Credential resolution ─────────────────────────────────────────────────────

/// Resolve the API key for `slot_id`, preferring the OS-keychain entry when
/// present and falling back to the providers.json credentials map.
///
/// Purpose: single authoritative key-lookup path per ADR-014.
/// Inputs:  `credentials` — providers.json fallback map (slot_id → plaintext).
///          `keychain_keys` — SecretString map loaded from OS keychain at startup.
///          `slot_id` — routing slot identifier.
/// Outputs: resolved API key as an owned `String` (empty string if not found).
/// Constraints: `expose_secret()` is called ONLY here; the `&str` is never stored
///              outside this function nor logged at any level.
/// SPORT: proxy credential resolution — T-P3-E00-02.
pub(crate) fn resolve_api_key(
    credentials: &HashMap<String, String>,
    keychain_keys: &HashMap<String, SecretString>,
    slot_id: &str,
) -> String {
    if let Some(secret) = keychain_keys.get(slot_id) {
        return secret.expose_secret().to_owned();
    }
    credentials.get(slot_id).cloned().unwrap_or_default()
}
