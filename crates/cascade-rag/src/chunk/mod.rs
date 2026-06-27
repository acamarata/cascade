//! Chunking strategy implementations.
//!
//! ## Local RAG types
//!
//! This module defines two parallel type families:
//!
//! 1. **Local RAG types** ([`Chunk`], [`ChunkerConfig`], [`Chunker`]) — DB-mapped,
//!    sync, used by the sliding-window pipeline and stored in `rag_chunks`.
//! 2. **cascade-types async pipeline** ([`StrategyChunker`], [`FixedSizeChunker`]) —
//!    async, MIME-dispatched, implements `cascade_types::Chunker`.
//!
//! ## Strategy selection (async pipeline)
//!
//! | MIME type | Default strategy |
//! |-----------|-----------------|
//! | `text/markdown` | [`markdown::MarkdownChunker`] |
//! | `text/x-{rust,python,…}` | [`code::CodeChunker`] |
//! | `text/plain` | Fixed-size window (256 chars, 32 overlap) |
//! | anything else | [`semantic::SemanticChunker`] |
//!
//! ## Custom strategy
//!
//! Set `[rag.chunking.strategy]` in `cascade.toml` to override the default for
//! any MIME pattern.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk

pub mod code;
pub mod hierarchical;
pub mod markdown;
pub mod semantic;

use async_trait::async_trait;
use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkOpts, Chunker as TypesChunker, Document},
    error::Result,
};

// ── Local RAG types (T-P4-E01-08) ────────────────────────────────────────────
//
// These types are DB-mapped (see rag_chunks schema in T-P4-E01-02).
// They are separate from the cascade-types Chunk used by the async pipeline.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::{Path, PathBuf};

/// A chunk stored in the `rag_chunks` table.
///
/// Fields map 1-to-1 to the `rag_chunks` DB columns from T-P4-E01-02.
/// `chunk_id` is `None` before a DB insert and populated after.
///
/// # Example
///
/// ```rust
/// use cascade_rag::chunk::Chunk;
/// use std::path::PathBuf;
/// let c = Chunk {
///     chunk_id: None,
///     source_path: PathBuf::from("file.md"),
///     chunk_index: 0,
///     text: "hello world".into(),
///     char_start: 0,
///     char_end: 11,
///     line_start: 1,
///     line_end: 1,
///     parent_chunk_id: None,
///     heading_path: None,
///     metadata: std::collections::HashMap::new(),
/// };
/// let json = serde_json::to_string(&c).unwrap();
/// let back: Chunk = serde_json::from_str(&json).unwrap();
/// assert_eq!(back.text, "hello world");
/// ```
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::Chunk
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub struct Chunk {
    /// Set after DB insert; `None` during chunking.
    pub chunk_id: Option<i64>,
    /// Absolute path to the source file.
    pub source_path: PathBuf,
    /// 0-based index of this chunk within its source document.
    pub chunk_index: usize,
    /// Full text content of this chunk (UTF-8).
    pub text: String,
    /// Byte offset (inclusive) in the source where this chunk starts.
    pub char_start: usize,
    /// Byte offset (exclusive) in the source where this chunk ends.
    pub char_end: usize,
    /// 1-based line number where the chunk starts.
    pub line_start: usize,
    /// 1-based line number (inclusive) where the chunk ends.
    pub line_end: usize,
    /// ID of the parent chunk for hierarchical chunking; `None` for top-level.
    pub parent_chunk_id: Option<i64>,
    /// Heading path for structured documents, e.g. `"## Section > ### Sub"`.
    pub heading_path: Option<String>,
    /// Extensible key-value metadata attached by the chunker.
    pub metadata: HashMap<String, String>,
}

impl Chunk {
    /// Returns `"<line_start>-<line_end>"` for citation display.
    pub fn line_range(&self) -> String {
        format!("{}-{}", self.line_start, self.line_end)
    }
}

/// Unified chunking configuration covering both char-based local RAG chunkers
/// and the MIME-dispatched async pipeline.
///
/// # Char-based fields (local RAG pipeline — `SemanticChunker`, `HierarchicalChunker`, `CodeChunker`)
/// - `max_chunk_chars` — soft maximum characters per chunk (default 2000).
/// - `overlap_chars`   — characters carried from the previous chunk (default 200).
/// - `min_chunk_chars` — minimum characters; smaller chunks are merged (default 50).
///
/// # Token/size fields (async `StrategyChunker` / daemon config)
/// - `target_size` — soft target size in characters (default 512).
/// - `max_size`    — hard maximum before a re-split (default 2048).
/// - `strategy`    — override strategy name; `None` = auto-select from MIME type.
///
/// The `overlap` field satisfies both pipelines (aliases `overlap_chars` semantics
/// for the async pipeline).
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::ChunkConfig
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub struct ChunkConfig {
    /// Soft maximum character count per chunk (local RAG pipeline).
    pub max_chunk_chars: usize,
    /// Characters of overlap carried from the previous chunk into the next.
    pub overlap_chars: usize,
    /// Minimum character count; chunks smaller than this are merged forward.
    pub min_chunk_chars: usize,
    /// Soft target chunk size in characters (async pipeline / daemon config).
    pub target_size: usize,
    /// Hard maximum chunk size; chunks larger than this are split again.
    pub max_size: usize,
    /// Override strategy name.  `None` = auto-select from MIME type.
    pub strategy: Option<String>,
}

impl Default for ChunkConfig {
    fn default() -> Self {
        Self {
            max_chunk_chars: 2000,
            overlap_chars: 200,
            min_chunk_chars: 50,
            target_size: 512,
            max_size: 2048,
            strategy: None,
        }
    }
}

/// Backward-compatible alias — existing call sites using `ChunkerConfig` continue to compile.
pub type ChunkerConfig = ChunkConfig;

/// Sync, object-safe chunker trait for the local RAG pipeline.
///
/// Implementations split the text of a source file into [`Chunk`]s ready for
/// DB insertion.  This trait is intentionally sync (no `async`) so it can be
/// used as a `dyn Chunker` without an async runtime.
///
/// # Object safety
///
/// No generic methods — this trait is object-safe and can be stored as
/// `Box<dyn Chunker>`.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::Chunker
pub trait Chunker: Send + Sync {
    /// Split `text` (read from `source_path`) into ordered chunks.
    ///
    /// `source_path` is stored on each returned [`Chunk`] and used for DB
    /// foreign-key linking.
    fn chunk(&self, source_path: &Path, text: &str) -> cascade_types::error::Result<Vec<Chunk>>;

    /// Human-readable strategy name used in logs and the `rag_chunks.strategy`
    /// column.
    fn strategy_name(&self) -> &str;
}

// Object-safety assertion — compile-time only.
fn _assert_object_safe(_: &dyn Chunker) {}

/// Backward-compatible alias for `ChunkConfig` matching the prior `ChunkingConfig` name.
///
/// `ChunkingConfig` has been merged into [`ChunkConfig`]. This alias preserves any
/// call site that referenced `ChunkingConfig` from the daemon config layer.
pub type ChunkingConfig = ChunkConfig;

// ── StrategyChunker ───────────────────────────────────────────────────────────

/// Dispatch to the appropriate strategy based on the document's MIME type.
///
/// This is the public entry-point used by the indexing pipeline.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::chunk::StrategyChunker;
/// # use cascade_types::chunker::{Document, ChunkOpts};
/// # use cascade_types::Chunker;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let chunker = StrategyChunker::default();
/// let doc = Document::from_text("# Hello\n\nWorld paragraph.");
/// let chunks = chunker.chunk(&doc, &ChunkOpts::default()).await?;
/// # Ok(())
/// # }
/// ```
#[derive(Debug, Default)]
pub struct StrategyChunker {
    /// Chunking configuration (MIME-type thresholds, size limits).
    /// Used by `with_config`; not yet read in the dispatch body (future work).
    _config: ChunkConfig,
}

impl StrategyChunker {
    /// Construct with a custom chunking config.
    pub fn with_config(config: ChunkConfig) -> Self {
        Self { _config: config }
    }
}

#[async_trait]
impl TypesChunker for StrategyChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<TypesChunk>> {
        let mime = doc.mime_type.as_deref().unwrap_or("text/plain");

        // Dispatch based on MIME type.
        if mime == "text/markdown" || mime == "text/x-markdown" {
            TypesChunker::chunk(&markdown::MarkdownChunker, doc, opts).await
        } else if mime.starts_with("text/x-") {
            TypesChunker::chunk(&code::AsyncCodeChunker, doc, opts).await
        } else {
            // Fall through to semantic chunker as the default (async pipeline).
            TypesChunker::chunk(&semantic::SemanticChunker::default(), doc, opts).await
        }
    }

    fn name(&self) -> &str {
        "strategy-dispatch"
    }
}

// ── Fixed-size fallback ───────────────────────────────────────────────────────

/// Naive fixed-size character-window chunker.
///
/// Used as the FTS5 fallback when no model is available, and as a reference
/// implementation for correctness tests.
///
/// # Constraints
///
/// - `opts.target_size` sets the window size in UTF-8 characters.
/// - `opts.overlap` sets the number of characters shared between consecutive
///   chunks.  Must be < `opts.target_size`.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::FixedSizeChunker
#[derive(Debug, Default)]
pub struct FixedSizeChunker;

#[async_trait]
impl TypesChunker for FixedSizeChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<TypesChunk>> {
        use cascade_types::chunker::ChunkMetadata;

        let text = &doc.content;
        if text.is_empty() {
            return Ok(vec![]);
        }

        let chars: Vec<char> = text.chars().collect();
        let step = opts.target_size.saturating_sub(opts.overlap).max(1);
        let mut chunks = Vec::new();
        let mut start = 0usize;
        let mut idx = 0usize;

        while start < chars.len() {
            let end = (start + opts.target_size).min(chars.len());
            let window: String = chars[start..end].iter().collect();
            // Compute byte offsets from character offsets.
            let byte_start: usize = chars[..start].iter().map(|c| c.len_utf8()).sum();
            let byte_end: usize = chars[..end].iter().map(|c| c.len_utf8()).sum();
            chunks.push(TypesChunk {
                id: format!(
                    "{}-{byte_start}",
                    doc.source_path
                        .as_ref()
                        .map(|p| p.display().to_string())
                        .unwrap_or_else(|| "mem".into())
                ),
                text: window,
                metadata: ChunkMetadata {
                    start_byte: byte_start,
                    end_byte: byte_end,
                    source_path: doc.source_path.clone(),
                    start_line: None,
                    end_line: None,
                    chunk_index: idx,
                    total_chunks: 0, // patched below
                    extra: std::collections::HashMap::new(),
                },
            });
            idx += 1;
            if end == chars.len() {
                break;
            }
            start += step;
        }

        let total = chunks.len();
        for chunk in &mut chunks {
            chunk.metadata.total_chunks = total;
        }
        Ok(chunks)
    }

    fn name(&self) -> &str {
        "fixed-size"
    }
}

// ── Tests (T-P4-E01-08) ───────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn make_chunk(
        text: &str,
        char_start: usize,
        char_end: usize,
        line_start: usize,
        line_end: usize,
    ) -> Chunk {
        Chunk {
            chunk_id: None,
            source_path: PathBuf::from("test.md"),
            chunk_index: 0,
            text: text.to_string(),
            char_start,
            char_end,
            line_start,
            line_end,
            parent_chunk_id: None,
            heading_path: None,
            metadata: HashMap::new(),
        }
    }

    #[test]
    fn chunk_roundtrip_json() {
        let c = make_chunk("hello world", 0, 11, 1, 1);
        let json = serde_json::to_string(&c).expect("serialize");
        let back: Chunk = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(back.text, "hello world");
        assert_eq!(back.char_start, 0);
        assert_eq!(back.char_end, 11);
        assert_eq!(back.line_start, 1);
        assert_eq!(back.line_end, 1);
        assert!(back.chunk_id.is_none());
        assert!(back.parent_chunk_id.is_none());
    }

    #[test]
    fn chunk_line_range() {
        let c = make_chunk("line one\nline two", 0, 17, 3, 7);
        assert_eq!(c.line_range(), "3-7");
    }

    #[test]
    fn chunker_config_defaults() {
        let cfg = ChunkConfig::default();
        assert_eq!(cfg.max_chunk_chars, 2000);
        assert_eq!(cfg.overlap_chars, 200);
        assert_eq!(cfg.min_chunk_chars, 50);
        assert_eq!(cfg.target_size, 512);
        assert_eq!(cfg.max_size, 2048);
        assert!(cfg.strategy.is_none());
        // Alias round-trips.
        let _: ChunkerConfig = cfg.clone();
        let _: ChunkingConfig = cfg;
    }

    #[test]
    fn chunk_all_fields_present() {
        // Verify all 10 spec fields exist and are accessible.
        let mut meta = HashMap::new();
        meta.insert("lang".to_string(), "rust".to_string());
        let c = Chunk {
            chunk_id: Some(42),
            source_path: PathBuf::from("src/lib.rs"),
            chunk_index: 3,
            text: "fn foo() {}".to_string(),
            char_start: 100,
            char_end: 111,
            line_start: 5,
            line_end: 5,
            parent_chunk_id: Some(1),
            heading_path: Some("## Functions".to_string()),
            metadata: meta,
        };
        assert_eq!(c.chunk_id, Some(42));
        assert_eq!(c.chunk_index, 3);
        assert_eq!(c.metadata.get("lang").map(|s| s.as_str()), Some("rust"));
        assert_eq!(c.heading_path.as_deref(), Some("## Functions"));
    }
}
