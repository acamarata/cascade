//! Async pipeline implementation for code chunking.

use async_trait::async_trait;
use tracing::warn;

use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkMetadata, ChunkOpts, Chunker as TypesChunker, Document},
    error::Result as TypesResult,
};

use super::super::{Chunk, Chunker, ChunkerConfig};
use super::chunker::CodeChunker;
use super::language::detect_language_for;

// ── Shared async helper ───────────────────────────────────────────────────────

pub(super) async fn async_chunk_impl(
    doc: &Document,
    opts: &ChunkOpts,
) -> TypesResult<Vec<TypesChunk>> {
    let path = doc.source_path.clone();
    let lang = detect_language_for(path.as_deref(), doc.mime_type.as_deref());

    if lang.is_none() {
        warn!(
            path = ?path,
            mime = ?doc.mime_type,
            "CodeChunker (async): unsupported language; falling back to semantic"
        );
        return TypesChunker::chunk(&super::super::semantic::SemanticChunker::default(), doc, opts)
            .await;
    }

    let config = ChunkerConfig {
        max_chunk_chars: opts.target_size,
        min_chunk_chars: ChunkerConfig::default().min_chunk_chars,
        overlap_chars: opts.overlap,
    };
    let sync_chunker = CodeChunker::new(config);
    let source_path = path.unwrap_or_else(|| std::path::PathBuf::from("mem"));
    let text = doc.content.clone();

    match Chunker::chunk(&sync_chunker, &source_path, &text) {
        Ok(local_chunks) => {
            let total = local_chunks.len();
            Ok(local_chunks
                .into_iter()
                .map(|c| TypesChunk {
                    id: format!("{}-code-{}", source_path.display(), c.char_start),
                    text: c.text,
                    metadata: ChunkMetadata {
                        start_byte: c.char_start,
                        end_byte: c.char_end,
                        source_path: Some(c.source_path),
                        start_line: Some(c.line_start),
                        end_line: Some(c.line_end),
                        chunk_index: c.chunk_index,
                        total_chunks: total,
                        extra: {
                            let mut e = std::collections::HashMap::new();
                            for (k, v) in &c.metadata {
                                e.insert(k.clone(), serde_json::Value::String(v.clone()));
                            }
                            if let Some(hp) = c.heading_path {
                                e.insert(
                                    "heading_path".to_owned(),
                                    serde_json::Value::String(hp),
                                );
                            }
                            e
                        },
                    },
                })
                .collect())
        }
        Err(e) => {
            warn!(error = %e, "CodeChunker (async) fallback; delegating to semantic");
            TypesChunker::chunk(
                &super::super::semantic::SemanticChunker::default(),
                doc,
                opts,
            )
            .await
        }
    }
}

// ── AsyncCodeChunker ──────────────────────────────────────────────────────────

/// Async wrapper implementing `cascade_types::Chunker` for the indexing pipeline.
///
/// This is an alias of [`CodeChunker`] kept for backwards-compat.
#[derive(Debug, Default)]
pub struct AsyncCodeChunker;

#[async_trait]
impl TypesChunker for AsyncCodeChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> TypesResult<Vec<TypesChunk>> {
        async_chunk_impl(doc, opts).await
    }

    fn name(&self) -> &str {
        "code"
    }
}
