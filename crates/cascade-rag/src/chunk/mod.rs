//! Chunking strategy implementations.
//!
//! Each strategy implements [`cascade_types::Chunker`].  The pipeline
//! orchestrator in this module selects a strategy based on the document's
//! MIME type and the active [`crate::TierLevel`].
//!
//! ## Strategy selection
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
    chunker::{Chunk, ChunkOpts, Chunker, Document},
    error::Result,
};

// ── ChunkingConfig ────────────────────────────────────────────────────────────

/// Per-document-type chunking configuration.
///
/// Stored in `cascade.toml`; loaded by the daemon config system.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::ChunkingConfig
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct ChunkingConfig {
    /// Soft target chunk size in characters (not tokens).
    pub target_size: usize,
    /// Character overlap between consecutive chunks.
    pub overlap: usize,
    /// Minimum chunk size.  Chunks smaller than this are merged forward.
    pub min_size: usize,
    /// Maximum chunk size.  Chunks larger than this are split again.
    pub max_size: usize,
    /// Override strategy name.  `None` = auto-select from MIME type.
    pub strategy: Option<String>,
}

impl Default for ChunkingConfig {
    fn default() -> Self {
        Self {
            target_size: 512,
            overlap: 64,
            min_size: 64,
            max_size: 2048,
            strategy: None,
        }
    }
}

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
    config: ChunkingConfig,
}

impl StrategyChunker {
    /// Construct with a custom chunking config.
    pub fn with_config(config: ChunkingConfig) -> Self {
        Self { config }
    }
}

#[async_trait]
impl Chunker for StrategyChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<Chunk>> {
        let mime = doc.mime_type.as_deref().unwrap_or("text/plain");

        // Dispatch based on MIME type.
        if mime == "text/markdown" || mime == "text/x-markdown" {
            markdown::MarkdownChunker::default().chunk(doc, opts).await
        } else if mime.starts_with("text/x-") {
            code::CodeChunker::default().chunk(doc, opts).await
        } else {
            // Fall through to semantic chunker as the default.
            semantic::SemanticChunker::default().chunk(doc, opts).await
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
impl Chunker for FixedSizeChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<Chunk>> {
        use cascade_types::chunker::ChunkMetadata;
        use std::collections::HashMap;

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
            chunks.push(Chunk {
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
                    extra: HashMap::new(),
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
