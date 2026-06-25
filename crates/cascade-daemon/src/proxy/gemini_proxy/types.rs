//! Types shared across gemini_proxy sub-modules.
//!
//! Purpose: constants, error enum, and thread-safe ProxyState handle.
//! SPORT: proxy module (T-P2-E02-36)

use std::collections::HashMap;
use std::sync::{Arc, Mutex, RwLock};

use secrecy::SecretString;

use crate::config::ProxyConfig;
use cascade_core::routing_table::RoutingTable;

// ── Gemini upstream ───────────────────────────────────────────────────────────

/// Base URL for the Gemini generativelanguage API.
pub const GEMINI_UPSTREAM_BASE: &str = "https://generativelanguage.googleapis.com";

// ── Body size limit ────────────────────────────────────────────────────────────

/// Maximum request body size: 1MB (1,048,576 bytes). Prevents memory exhaustion
/// from malicious or misconfigured clients. Returns HTTP 413 if exceeded.
pub const MAX_REQUEST_BODY_SIZE: usize = 1_048_576;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by the Gemini proxy.
#[derive(Debug, thiserror::Error)]
pub enum ProxyError {
    /// No enabled Gemini providers are available (empty routing table).
    #[error("no Gemini providers available")]
    NoProvidersAvailable,
    /// All providers were exhausted after `max_retries` attempts.
    #[error("all Gemini providers exhausted after {attempts} attempts")]
    AllProvidersExhausted {
        /// Number of attempts made before giving up.
        attempts: usize,
    },
    /// An upstream HTTP or network error occurred (not a 429).
    #[error("upstream error: {0}")]
    Upstream(#[from] reqwest::Error),
    /// Failed to bind or run the HTTP listener.
    #[error("listener error: {0}")]
    Listener(std::io::Error),
}

// ── Shared proxy state ────────────────────────────────────────────────────────

/// Thread-safe, cloneable handle to the proxy routing state.
///
/// Shared between the HTTP handler tasks and the `providers.updated` rebuild
/// task. Both fields are wrapped independently so a routing-table rebuild only
/// needs to replace the table contents, not the whole Arc.
#[derive(Clone)]
pub struct ProxyState {
    /// Round-robin routing table.  NEVER hold the Mutex across an `.await`.
    pub table: Arc<Mutex<RoutingTable>>,
    /// `slot_id → api_key` map rebuilt alongside the routing table.
    /// `account_id` from `ProviderEntry` is treated as the Gemini API key.
    /// Kept as metadata/fallback per ADR-014. When `keychain_keys` has an entry
    /// for a slot, it takes precedence as the authoritative secret source.
    pub credentials: Arc<Mutex<HashMap<String, String>>>,
    /// `slot_id → SecretString` map loaded from the OS keychain at startup.
    ///
    /// Purpose: authoritative API-key source when present; falls back to
    /// `credentials` (providers.json metadata) when absent.
    /// Inputs: populated by `load_api_keys()` + positional slot mapping at init.
    /// Outputs: `expose_secret()` called ONLY inside `resolve_api_key()`, never logged.
    /// Constraints: NEVER log the inner value; NEVER store the exposed `&str`.
    /// SPORT: proxy credential resolution — T-P3-E00-02.
    pub keychain_keys: Arc<RwLock<HashMap<String, SecretString>>>,
    /// Proxy configuration (immutable after startup).
    pub config: ProxyConfig,
    /// Upstream base URL (override in tests via `GeminiProxy::with_upstream`).
    pub upstream_base: Arc<String>,
}
