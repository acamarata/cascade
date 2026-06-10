//! Provider adapter implementations.
//!
//! # Purpose
//!
//! Each sub-module contains a concrete `ProviderAdapter` implementation for a
//! specific AI provider.  All adapters are wired through the module tree from
//! `lib.rs`; callers use `Box<dyn ProviderAdapter>` exclusively.

pub mod anthropic;
pub mod cohere;
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
