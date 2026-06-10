//! WIT binding types for the `retriever` interface (mirrors `wit/retriever.wit`).
//!
//! Constraints: must round-trip through JSON without field loss.
//! SPORT: cascade-plugins / WIT bindings

use serde::{Deserialize, Serialize};

/// Mirrors WIT `record retrieval-result`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RetrievalResult {
    pub text: String,
    pub score: f32,
    pub source: String,
    pub chunk_index: u32,
}

/// Mirrors WIT `record retrieve-opts`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RetrieveOpts {
    pub top_k: u32,
    pub min_score: f32,
}

/// Mirrors WIT `variant retriever-error`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum RetrieverError {
    EmptyQuery,
    NotReady,
    ResourceExhausted,
    Internal { message: String },
}
