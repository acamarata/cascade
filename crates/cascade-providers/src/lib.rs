//! # cascade-providers
//!
//! Provider provisioning, OAuth PKCE flows, and auto-auth import for the
//! Cascade AI context framework.
//!
//! ## Modules
//!
//! | Module | Ticket | Purpose |
//! |--------|--------|---------|
//! | [`google_oauth`] | T-P3-E03-39 | Google OAuth PKCE client + Gemini key validation |
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

pub mod auto_auth_import;
pub mod google_oauth;
pub mod google_provision;

// ── Top-level re-exports ──────────────────────────────────────────────────────

// auto_auth types live in cascade-types; re-export for convenience.
pub use cascade_types::auto_auth::{AuthSource, AuthType, DiscoveredAccount, ImportResult};
pub use auto_auth_import::scan_all;

pub use google_oauth::{validate_gemini_key, GoogleOAuthClient, GoogleToken};
pub use google_provision::{GoogleProvisionClient, ProvisionError};
