//! Provider adapter implementations.
//!
//! # Purpose
//!
//! Each sub-module contains a concrete `ProviderAdapter` implementation for a
//! specific AI provider.  All adapters are wired through the module tree from
//! `lib.rs`; callers use `Box<dyn ProviderAdapter>` exclusively.
//!
//! ## Subscription detect+config adapters
//!
//! `cursor` and `antigravity` operate in DETECT + CONFIG mode only.  They
//! identify installed subscriptions and generate MCP-wired config files but
//! do NOT route inference through the paid subscription (ToS compliance).
//! See each module's doc comment for the full rationale.

pub mod anthropic;
pub mod antigravity;
pub mod cohere;
pub mod cursor;
pub mod deepseek;
pub mod gemini;
pub mod generic_openai;
pub mod groq;
pub mod mistral;
pub mod ollama;
pub mod openai;
pub mod openai_compat;
pub mod opencode;
pub mod openrouter;
pub mod together;
