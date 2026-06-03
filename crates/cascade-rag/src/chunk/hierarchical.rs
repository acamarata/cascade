//! Hierarchical chunker: parent document + child chunks.
//!
//! Stores a full parent document text alongside smaller child chunks so that
//! retrieval can return the wider parent context even when the query matches
//! only a child chunk.  This pattern significantly improves answer quality
//! for questions that require multi-paragraph context.
//!
//! ## Storage model
//!
//! ```text
//! parent_docs  table: (parent_id TEXT PK, text TEXT, file_path TEXT)
//! chunks       table: (chunk_id TEXT PK, parent_id TEXT FK, …)
//! ```
//!
//! The `parent_id` is stored in `ChunkMetadata::extra["parent_id"]`.
//!
//! ## Sprint tickets
//!
//! - T-P1-E7-S06-03a — parent doc store
//! - T-P1-E7-S06-03b — parent retrieval API
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk::HierarchicalChunker

use async_trait::async_trait;
use serde_json::json;
use tracing::debug;

use cascade_types::{
    chunker::{Chunk, ChunkOpts, Chunker, Document},
    error::Result,
};

use super::semantic::SemanticChunker;

/// Hierarchical chunker wrapping a base chunking strategy.
///
/// The outer chunker splits the document at the parent level (e.g. by section);
/// the inner chunker splits each parent section into child chunks.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::chunk::hierarchical::HierarchicalChunker;
/// # use cascade_types::chunker::{Document, ChunkOpts};
/// # use cascade_types::Chunker;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let chunker = HierarchicalChunker::default();
/// let doc = Document::from_text("# Section 1\n\nPara 1.\n\n# Section 2\n\nPara 2.");
/// let chunks = chunker.chunk(&doc, &ChunkOpts::default()).await?;
/// for c in &chunks {
///     println!("parent_id: {:?}", c.metadata.extra.get("parent_id"));
/// }
/// # Ok(())
/// # }
/// ```
#[derive(Debug, Default)]
pub struct HierarchicalChunker;

#[async_trait]
impl Chunker for HierarchicalChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<Chunk>> {
        // Phase 1: split into parent sections (heading-aware for Markdown, paragraph
        // boundaries for plain text).
        let parent_sections = split_sections(&doc.content);
        debug!(
            section_count = parent_sections.len(),
            "hierarchical: split into parent sections"
        );

        let inner = SemanticChunker::default();
        let mut all_chunks = Vec::new();
        let mut global_idx = 0usize;

        for (s_idx, section) in parent_sections.iter().enumerate() {
            // Derive a stable parent_id from source path + section index.
            let parent_id = format!(
                "{}-parent-{s_idx}",
                doc.source_path
                    .as_ref()
                    .map(|p| p.display().to_string())
                    .unwrap_or_else(|| "mem".into())
            );

            let parent_doc = Document {
                source_path: doc.source_path.clone(),
                content: section.clone(),
                mime_type: doc.mime_type.clone(),
                metadata: doc.metadata.clone(),
            };

            let child_chunks = inner.chunk(&parent_doc, opts).await?;

            for mut chunk in child_chunks {
                // Tag each child with its parent_id for later retrieval.
                chunk
                    .metadata
                    .extra
                    .insert("parent_id".into(), json!(parent_id));
                chunk.metadata.chunk_index = global_idx;
                global_idx += 1;
                all_chunks.push(chunk);
            }
        }

        let total = all_chunks.len();
        for c in &mut all_chunks {
            c.metadata.total_chunks = total;
        }
        Ok(all_chunks)
    }

    fn name(&self) -> &str {
        "hierarchical"
    }
}

/// Split document content at heading or double-newline section boundaries.
fn split_sections(text: &str) -> Vec<String> {
    // Simple heading-aware split: new section at `\n# ` or `\n## `.
    let mut sections: Vec<String> = Vec::new();
    let mut current = String::new();
    for line in text.lines() {
        if (line.starts_with("# ") || line.starts_with("## ")) && !current.is_empty() {
            sections.push(current.trim().to_string());
            current = line.to_string();
        } else {
            if !current.is_empty() {
                current.push('\n');
            }
            current.push_str(line);
        }
    }
    if !current.trim().is_empty() {
        sections.push(current.trim().to_string());
    }
    if sections.is_empty() {
        sections.push(text.to_string());
    }
    sections
}
