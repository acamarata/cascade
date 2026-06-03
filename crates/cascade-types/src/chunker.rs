//! Chunker trait and associated types.
//!
//! A `Chunker` splits a `Document` into `Chunk` pieces suitable for embedding
//! and retrieval. Implementations range from naive fixed-size character windows
//! to semantic paragraph-aware splitters.

use crate::error::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

// ── Document ──────────────────────────────────────────────────────────────────

/// A parsed document ready for chunking.
///
/// Produced by a [`crate::parser::Parser`] and consumed by a [`Chunker`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Document {
    /// Absolute path to the source file, if the document originated from disk.
    pub source_path: Option<std::path::PathBuf>,

    /// The full text content of the document (UTF-8).
    pub content: String,

    /// MIME type or format hint (e.g. `"text/markdown"`, `"text/x-rust"`).
    pub mime_type: Option<String>,

    /// Arbitrary metadata attached during parsing (language, title, author, …).
    pub metadata: HashMap<String, serde_json::Value>,
}

impl Document {
    /// Construct a plain-text document with no metadata.
    pub fn from_text(content: impl Into<String>) -> Self {
        Self {
            source_path: None,
            content: content.into(),
            mime_type: None,
            metadata: HashMap::new(),
        }
    }
}

// ── Chunk ─────────────────────────────────────────────────────────────────────

/// A single chunk produced by a [`Chunker`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Chunk {
    /// Unique identifier within the current indexing run.
    /// Format: `<source_hash>-<start_byte>` for deterministic IDs.
    pub id: String,

    /// The text content of this chunk.
    pub text: String,

    /// Positional and provenance metadata.
    pub metadata: ChunkMetadata,
}

/// Positional and provenance metadata for a [`Chunk`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChunkMetadata {
    /// Byte offset in the source document where this chunk starts.
    pub start_byte: usize,

    /// Byte offset (exclusive) where this chunk ends.
    pub end_byte: usize,

    /// Source file path, inherited from the parent `Document`.
    pub source_path: Option<std::path::PathBuf>,

    /// 1-based line number where the chunk starts in the source document.
    pub start_line: Option<usize>,

    /// 1-based line number where the chunk ends.
    pub end_line: Option<usize>,

    /// Index of this chunk within the ordered sequence produced from its
    /// parent document. Zero-based.
    pub chunk_index: usize,

    /// Total number of chunks produced from the same parent document.
    pub total_chunks: usize,

    /// Custom fields added by specific chunker implementations.
    pub extra: HashMap<String, serde_json::Value>,
}

// ── ChunkOpts ─────────────────────────────────────────────────────────────────

/// Parameters controlling how a [`Chunker`] splits a [`Document`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChunkOpts {
    /// Target token/character count per chunk.
    /// Implementations should treat this as a soft maximum.
    pub target_size: usize,

    /// Number of tokens/characters shared between consecutive chunks.
    /// Overlap helps retrieval when a relevant passage spans a chunk boundary.
    pub overlap: usize,

    /// If `true`, the chunker may produce smaller final chunks rather than
    /// padding to `target_size`.
    pub allow_smaller_last: bool,
}

impl Default for ChunkOpts {
    fn default() -> Self {
        Self {
            target_size: 512,
            overlap: 64,
            allow_smaller_last: true,
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Splits a [`Document`] into a sequence of [`Chunk`]s.
///
/// Implementations must be `Send + Sync` so they can be shared across async
/// task boundaries.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::chunker::{Chunker, Document, ChunkOpts};
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct FixedSizeChunker;
///
/// #[async_trait]
/// impl Chunker for FixedSizeChunker {
///     async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<cascade_types::chunker::Chunk>> {
///         // Split doc.content into windows of opts.target_size …
///         Ok(vec![])
///     }
/// }
/// ```
#[async_trait]
pub trait Chunker: Send + Sync {
    /// Split `doc` into chunks according to `opts`.
    ///
    /// The returned vec is ordered: `chunks[i].metadata.chunk_index == i`.
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<Chunk>>;

    /// A human-readable name for this chunker (used in logging and config).
    fn name(&self) -> &str {
        "unknown-chunker"
    }
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// A chunker that returns the entire document as a single chunk.
///
/// Useful as a stand-in during integration tests or when chunking is
/// intentionally disabled.
#[derive(Debug, Default)]
pub struct NoopChunker;

#[async_trait]
impl Chunker for NoopChunker {
    async fn chunk(&self, doc: &Document, _opts: &ChunkOpts) -> Result<Vec<Chunk>> {
        if doc.content.is_empty() {
            return Ok(vec![]);
        }
        let len = doc.content.len();
        Ok(vec![Chunk {
            id: "noop-0".to_owned(),
            text: doc.content.clone(),
            metadata: ChunkMetadata {
                start_byte: 0,
                end_byte: len,
                source_path: doc.source_path.clone(),
                start_line: Some(1),
                end_line: None,
                chunk_index: 0,
                total_chunks: 1,
                extra: HashMap::new(),
            },
        }])
    }

    fn name(&self) -> &str {
        "noop"
    }
}

// ── Object-safety check ───────────────────────────────────────────────────────

/// Ensures `Chunker` is object-safe by constructing a trait object.
/// This is compiled away in release builds.
fn _assert_object_safe(_: &dyn Chunker) {}
