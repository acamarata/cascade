//! # cascade-providers
//!
//! Provider provisioning, OAuth PKCE flows, auto-auth import, and the core
//! `ProviderAdapter` trait for the Cascade AI context framework.
//!
//! ## Modules
//!
//! | Module | Ticket | Purpose |
//! |--------|--------|---------|
//! | [`adapter`] | T-P3-E04-01 | `ProviderAdapter` trait + `NoopProvider` stub |
//! | [`types`] | T-P3-E04-02 | `CompletionRequest`, `CompletionResponse`, shared wire types |
//! | [`provider_info`] | T-P3-E04-03 | `ProviderInfo`, `ProviderCapabilities`, `TaskType`, `AuthMethod` |
//! | [`error`] | T-P3-E04-04 | `ProviderError` enum — all failure variants |
//! | [`registry`] | T-P3-E04-05 | `ProviderRegistry` + `RoutingTable` — thread-safe adapter store |
//! | [`http_client`] | T-P3-E04-06 | `CascadeHttpClient` — shared reqwest wrapper + retry logic |
//! | [`cost`] | T-P3-E04-14 | `CostTable`, `ModelPricing`, `compute_cost()` — static pricing table |
//! | [`oauth`] | T-P3-E04-20 | Generic OAuth 2.0 PKCE foundation: OAuthClient, PKCE helpers, TokenStore |
//! | [`google_oauth`] | T-P3-E03-39 | Google OAuth PKCE client + Gemini key validation (delegates to `oauth/`) |
//! | [`google_provision`] | T-P3-E03-39b | GCP project + API key provisioning engine |
//! | [`auto_auth_import`] | T-P3-E03-41 | Detect and import accounts from installed harnesses |
//!
//! ## Design invariants
//!
//! 1. **No plaintext credential logging.** Token values and API keys are never
//!    written to tracing spans or log lines.
//! 2. **Read-only harness scan.** `auto_auth_import` reads but never modifies
//!    any harness config file.
//! 3. **Keychain-backed storage.** All provisioned credentials are stored via
//!    `cascade-keychain`; this crate never writes secrets to disk directly.

// ── E-04 core type-system modules ────────────────────────────────────────────
pub mod adapter;
pub mod adapters;
pub mod connect;
pub mod cost;
pub mod error;
pub mod http_client;
pub mod provider_info;
pub mod registry;
pub mod types;

// ── E-04-06 test helpers (test builds only) ───────────────────────────────────
#[cfg(test)]
pub mod test_helpers;

// ── E-04-20 generic OAuth PKCE foundation ────────────────────────────────────
pub mod oauth;

// ── E-03 provisioning modules ─────────────────────────────────────────────────
pub mod auto_auth_import;
pub mod google_oauth;
pub mod google_provision;

// ── Top-level re-exports ──────────────────────────────────────────────────────

// E-04 exports — adapter trait, types, error, registry
pub use adapter::{NoopProvider, ProviderAdapter};
pub use cost::{compute_cost, CostTable, ModelPricing};
pub use error::ProviderError;
pub use provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo, TaskType};
pub use registry::{ProviderRegistry, RoutingTable};
pub use types::{
    CompletionRequest, CompletionResponse, Message, MessageRole, ModelInfo, StreamChunk, TokenUsage,
};

// E-03 exports — auto_auth types live in cascade-types; re-export for convenience.
pub use auto_auth_import::scan_all;
pub use cascade_types::auto_auth::{AuthSource, AuthType, DiscoveredAccount, ImportResult};

pub use google_oauth::{validate_gemini_key, GoogleOAuthClient, GoogleToken};
pub use google_provision::{
    GoogleProvisionClient, ProvisionError, ProvisionMultiResult, ProvisionOptions,
    ProvisionedKey, ProvisioningCheckpoint,
};

// E-P5-08 exports — CLI-accessible provider credential connect/validate path.
pub use connect::{
    connect_provider, connect_provider_at, list_providers, list_providers_at, remove_provider,
    remove_provider_at, validate_api_key, ConnectedProvider, Credential, ProviderKind,
    KEYCHAIN_SERVICE as PROVIDER_KEYCHAIN_SERVICE, PROVIDERS_JSON_NAME,
};

// E-P7-06 exports — subscription detect+config adapters (DETECT+CONFIG mode only).
pub use adapters::cursor::{generate_cursor_cascade_config, CursorAdapter};
pub use adapters::antigravity::{generate_antigravity_cascade_config, AntigravityAdapter};
pub use auto_auth_import::scan_antigravity;
