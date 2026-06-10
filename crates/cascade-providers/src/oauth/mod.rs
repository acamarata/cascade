//! # oauth
//!
//! Generic OAuth 2.0 PKCE foundation for cascade-providers.
//!
//! ## Purpose
//! This module provides the generic machinery used by all provider-specific OAuth
//! implementations:
//! - `pkce`: PKCE verifier generation and S256 challenge computation (RFC 7636).
//! - `client`: generic `OAuthClient` + `OAuthProviderConfig` + loopback redirect server.
//! - `token_store`: keychain-backed `TokenStore` for `OAuthTokens`.
//! - `providers`: scaffold for per-provider config factories (filled in T-21/T-22).
//!
//! ## Design
//! The `google_oauth` module (E-03) delegates PKCE and callback logic to this module.
//! Provider-specific configs live under `providers/`.
//!
//! ## Constraints
//! - Token values MUST NOT appear in any log output.
//! - All redirect servers bind to loopback (`127.0.0.1`) only.
//! - PKCE S256 method only.

pub mod client;
pub mod pkce;
pub mod providers;
pub mod token_store;

// Re-export the most commonly needed types at the `oauth` level.
pub use client::{AuthorizeResult, OAuthClient, OAuthClientError, OAuthProviderConfig};
pub use pkce::{generate_code_challenge, generate_code_verifier};
pub use token_store::{OAuthTokens, TokenStore, TokenStoreError};
