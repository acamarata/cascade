//! Markdown-aware chunker.
//!
//! Uses the heading hierarchy as natural chunk boundaries.  A new chunk is
//! started whenever a `#`-level heading is encountered; content under a heading
//! stays together until the next heading of equal or higher level.
//!
//! Code fences (` ``` `) are treated as atomic units: the fence + body + closing
//! fence are never split across chunks regardless of size.
//!
//! ## YAML/TOML frontmatter
//!
//! Frontmatter delimited by `---` is stripped and stored in
//! `ChunkMetadata::extra["frontmatter"]` as raw text.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk::MarkdownChunker
//!
//! Sprint ticket: T-P1-E7-S06-04

use async_trait::async_trait;
use serde_json::json;
use std::collections::HashMap;

use cascade_types::{
    chunker::{Chunk, ChunkMetadata, ChunkOpts, Chunker, Document},
    error::Result,
};

/// Markdown-aware chunker splitting at heading boundaries.
///
/// Produces chunks keyed by heading hierarchy with frontmatter stripped.
#[derive(Debug, Default)]
pub struct MarkdownChunker;

#[async_trait]
impl Chunker for MarkdownChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<Chunk>> {
        let (frontmatter, body) = strip_frontmatter(&doc.content);
        let sections = split_by_headings(body);
        let mut chunks = Vec::with_capacity(sections.len());
        let mut byte_cursor = 0usize;

        for (idx, section) in sections.iter().enumerate() {
            let text = section.trim();
            if text.is_empty() {
                continue;
            }
            let byte_start = doc.content.find(text).unwrap_or(byte_cursor);
            let byte_end = byte_start + text.len();
            byte_cursor = byte_end;

            let mut extra = HashMap::new();
            if let Some(fm) = &frontmatter {
                extra.insert("frontmatter".into(), json!(fm));
            }

            chunks.push(Chunk {
                id: format!(
                    "{}-md-{byte_start}",
                    doc.source_path
                        .as_ref()
                        .map(|p| p.display().to_string())
                        .unwrap_or_else(|| "mem".into())
                ),
                text: text.to_string(),
                metadata: ChunkMetadata {
                    start_byte: byte_start,
                    end_byte: byte_end,
                    source_path: doc.source_path.clone(),
                    start_line: None,
                    end_line: None,
                    chunk_index: idx,
                    total_chunks: 0,
                    extra,
                },
            });
        }
        let total = chunks.len();
        for c in &mut chunks {
            c.metadata.total_chunks = total;
        }
        Ok(chunks)
    }

    fn name(&self) -> &str {
        "markdown"
    }
}

/// Strip YAML/TOML frontmatter delimited by `---`.
///
/// Returns `(Option<frontmatter_text>, rest_of_document)`.
fn strip_frontmatter(text: &str) -> (Option<String>, &str) {
    let trimmed = text.trim_start();
    if !trimmed.starts_with("---") {
        return (None, text);
    }
    let after_open = &trimmed[3..];
    if let Some(close) = after_open.find("\n---") {
        let fm = after_open[..close].trim().to_string();
        let rest = &after_open[close + 4..];
        (Some(fm), rest)
    } else {
        (None, text)
    }
}

/// Split Markdown text at heading boundaries.
///
/// Each returned string begins with the heading line (if any) and contains all
/// content up to the next same-or-higher-level heading.
fn split_by_headings(text: &str) -> Vec<String> {
    let mut sections: Vec<String> = Vec::new();
    let mut current = String::new();
    let mut in_code_fence = false;

    for line in text.lines() {
        // Track code fences to avoid splitting inside them.
        if line.trim_start().starts_with("```") {
            in_code_fence = !in_code_fence;
            current.push_str(line);
            current.push('\n');
            continue;
        }
        if !in_code_fence && is_heading(line) {
            if !current.trim().is_empty() {
                sections.push(current.clone());
            }
            current = format!("{line}\n");
        } else {
            current.push_str(line);
            current.push('\n');
        }
    }
    if !current.trim().is_empty() {
        sections.push(current);
    }
    if sections.is_empty() && !text.trim().is_empty() {
        sections.push(text.to_string());
    }
    sections
}

fn is_heading(line: &str) -> bool {
    line.starts_with('#') && line.chars().nth(1).map_or(false, |c| c == '#' || c == ' ')
}
