//! Gemini adapter — Google Generative Language API v1beta.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Google Gemini models via two endpoint modes:
//!
//! - **Direct** (`use_gfp_proxy = false`): targets `generativelanguage.googleapis.com`
//!   with API key authentication via the `?key=` query parameter.
//! - **Pool-proxy / GFP** (`use_gfp_proxy = true`): targets the local Gemini Fleet
//!   Proxy daemon at `http://127.0.0.1:3761`. The proxy handles key rotation; the
//!   adapter sends **no** API key in proxy mode.

pub mod adapter;
pub mod config;
pub(super) mod stream;
pub(super) mod wire_types;

mod tests;

// ── Public re-exports (preserve original public API paths) ────────────────────

pub use adapter::GeminiAdapter;
pub use config::GeminiConfig;
