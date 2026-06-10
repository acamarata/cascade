//! WIT binding types for the `chunker` interface (mirrors `wit/chunker.wit`).
//!
//! Purpose: Rust structs mirroring the WIT records so the host can
//!   serialise/deserialise values crossing the WASM boundary.
//! Constraints: must round-trip through JSON without field loss.
//! SPORT: cascade-plugins / WIT bindings

use serde::{Deserialize, Serialize};

/// Mirrors WIT `record chunk-meta`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChunkMeta {
    pub index: u32,
    pub byte_offset: u32,
    pub section: Option<String>,
}

/// Mirrors WIT `record chunk`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Chunk {
    pub text: String,
    pub meta: ChunkMeta,
}

/// Mirrors WIT `record chunk-opts`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChunkOpts {
    pub target_tokens: u32,
    pub overlap_tokens: u32,
}

/// Mirrors WIT `record document`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Document {
    pub id: String,
    pub content: String,
    pub mime_type: Option<String>,
}

/// Mirrors WIT `variant chunker-error`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ChunkerError {
    EmptyDocument,
    ResourceExhausted,
    Internal { message: String },
}
