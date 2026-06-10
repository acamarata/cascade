//! # oauth::providers
//!
//! Provider-specific `OAuthProviderConfig` constructors.
//!
//! ## Purpose
//! Each sub-module builds an `OAuthProviderConfig` for a specific OAuth provider
//! and exposes a factory function. Configs are passed to `OAuthClient::new`.
//!
//! ## Providers
//! - `gemini`: Google/Gemini PKCE flow (T-P3-E04-21)
//! - `openai`: OpenAI PKCE flow (T-P3-E04-22)
//! - `opencode`: OpenCode Go PKCE flow (T-P3-E04-22)

pub mod gemini;
pub mod openai;
pub mod opencode;

pub use gemini::{gemini_oauth_config, GEMINI_PROVIDER_ID};
pub use openai::{openai_oauth_config, OPENAI_PROVIDER_ID};
pub use opencode::{opencode_oauth_config, OPENCODE_PROVIDER_ID};
