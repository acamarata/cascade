//! WIT binding types for the `provider` interface (mirrors `wit/provider.wit`).
//!
//! Constraints: must round-trip through JSON without field loss.
//! SPORT: cascade-plugins / WIT bindings

use serde::{Deserialize, Serialize};

/// Mirrors WIT `record embedding`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Embedding {
    pub vector: Vec<f32>,
    pub input_index: u32,
}

/// Mirrors WIT `record embed-opts`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EmbedOpts {
    pub model_hint: Option<String>,
    pub normalise: bool,
}

/// Mirrors WIT `variant provider-error`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ProviderError {
    EmptyBatch,
    TextTooLong { index: u32 },
    ResourceExhausted,
    Internal { message: String },
}
