//! Embedding provider implementations.
//!
//! All implementations satisfy the [`cascade_types::EmbeddingProvider`] trait.
//! The primary local provider is [`bge_m3::BgeM3Provider`] via fastembed-rs ONNX
//! inference; cloud providers and alternative local models are opt-in via config.
//!
//! ## Provider selection
//!
//! The active provider is chosen from `cascade.toml`:
//!
//! ```toml
//! [rag]
//! embedding_model = "bge-m3"   # default
//! # embedding_model = "nomic"
//! # embedding_model = "jina"
//! # embedding_model = "openai"  # requires OPENAI_API_KEY in vault
//! ```
//!
//! Cloud providers (OpenAI, Cohere, Voyage, Gemini) must be explicitly opted
//! into and require the appropriate API key in `~/.claude/vault.env`.
//! A privacy warning is logged on first use.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::embed

#[cfg(feature = "fastembed")]
pub mod bge_m3;

pub mod jina;
pub mod nomic;

pub use cascade_types::{EmbedOpts, EmbedUsage, Embedding, EmbeddingProvider, ProviderKind};
