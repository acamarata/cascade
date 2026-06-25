//! `CodeChunker` struct — sync [`Chunker`] + async `TypesChunker` impls.

use async_trait::async_trait;
use tracing::warn;

use std::path::Path;

use super::super::{Chunk, Chunker, ChunkerConfig};
use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkOpts, Chunker as TypesChunker, Document},
    error::{Result, Result as TypesResult},
};

use super::async_impl::async_chunk_impl;
use super::language::detect_language_for;

#[cfg(feature = "code-chunker")]
use super::ts_impl::ts_chunk;

/// Function-level code chunker backed by tree-sitter AST analysis.
///
/// Implements the sync [`Chunker`] trait for the local RAG pipeline.
/// Falls back to [`super::super::semantic::SemanticChunker`] for unsupported extensions.
///
/// Compiled only when `features = ["code-chunker"]`.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::chunk::code::CodeChunker;
/// # use cascade_rag::chunk::{Chunker, ChunkerConfig};
/// # use std::path::Path;
/// let chunker = CodeChunker::new(ChunkerConfig::default());
/// let chunks = chunker.chunk(Path::new("src/lib.rs"), "fn foo() {}").unwrap();
/// assert!(!chunks.is_empty());
/// ```
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::CodeChunker
pub struct CodeChunker {
    pub(super) config: ChunkerConfig,
}

impl CodeChunker {
    /// Create a new `CodeChunker` with the given configuration.
    pub fn new(config: ChunkerConfig) -> Self {
        Self { config }
    }
}

impl Default for CodeChunker {
    fn default() -> Self {
        Self::new(ChunkerConfig::default())
    }
}

impl std::fmt::Debug for CodeChunker {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CodeChunker")
            .field("max_chunk_chars", &self.config.max_chunk_chars)
            .finish()
    }
}

impl Chunker for CodeChunker {
    fn chunk(&self, source_path: &Path, text: &str) -> Result<Vec<Chunk>> {
        #[cfg(feature = "code-chunker")]
        {
            let lang = detect_language_for(Some(source_path), None);
            if let Some(lang) = lang {
                match ts_chunk(source_path, text, lang, &self.config) {
                    Ok(chunks) => return Ok(chunks),
                    Err(e) => {
                        warn!(
                            path = %source_path.display(),
                            language = lang.as_str(),
                            error = %e,
                            "tree-sitter chunking failed; falling back to semantic"
                        );
                    }
                }
            } else {
                warn!(
                    path = %source_path.display(),
                    "unsupported language extension; falling back to semantic chunker"
                );
            }
        }
        #[cfg(not(feature = "code-chunker"))]
        {
            let _ = source_path;
            let _ = text;
            warn!("code-chunker feature not enabled; falling back to semantic chunker");
        }

        // Fallback to SemanticChunker.
        let semantic =
            super::super::semantic::SemanticChunker::with_config(self.config.clone());
        Chunker::chunk(&semantic, source_path, text)
    }

    fn strategy_name(&self) -> &str {
        "code"
    }
}

// ── TypesChunker impl ─────────────────────────────────────────────────────────

#[async_trait]
impl TypesChunker for CodeChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> TypesResult<Vec<TypesChunk>> {
        async_chunk_impl(doc, opts).await
    }

    fn name(&self) -> &str {
        "code"
    }
}
