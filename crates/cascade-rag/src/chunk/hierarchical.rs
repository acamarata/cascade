//! Hierarchical chunker: section/subsection with parent-child ID wiring.
//!
//! ## Algorithm (T-P4-E01-10)
//!
//! 1. Split text into top-level sections at heading boundaries (`#`-prefixed
//!    lines) or double-newline paragraph boundaries for flat prose.
//! 2. For each section create a **parent** [`Chunk`] whose text is the full
//!    section (capped at `max_chunk_chars * 3`).
//! 3. If the parent text exceeds `max_chunk_chars`, also emit **child** chunks
//!    produced by delegating to [`SemanticChunker`].  Each child's
//!    `parent_chunk_id` is set to the parent's `chunk_index` (local index,
//!    pre-DB).
//! 4. `heading_path` is derived from the first `#`-prefixed line of each
//!    section and is copied to every child chunk.
//! 5. Flat documents (no headings, no double-newlines) emit a single parent
//!    (and children if oversized).
//!
//! ## Async pipeline shim
//!
//! The type also implements the async `cascade_types::Chunker` trait (shim
//! forwarding to the local sync impl) so it can be wired into the strategy
//! dispatch table without a second code path.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::chunk::HierarchicalChunker

use async_trait::async_trait;
use std::collections::HashMap;
use std::path::Path;
use tracing::debug;

use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkMetadata, ChunkOpts, Chunker as TypesChunker, Document},
    error::Result,
};
use serde_json::json;

use super::semantic::SemanticChunker;
use super::{Chunk, Chunker, ChunkerConfig};

// ── Parent-size multiplier ────────────────────────────────────────────────────

/// A parent chunk holds up to this many times the regular `max_chunk_chars`.
///
/// Configurable via the spec guidance (CR-C: not hardcoded at call sites —
/// callers can adjust `max_chunk_chars` in [`ChunkerConfig`] to tune the cap).
const PARENT_SIZE_MULTIPLIER: usize = 3;

// ── HierarchicalChunker ───────────────────────────────────────────────────────

/// Hierarchical section/subsection chunker for the local RAG pipeline.
///
/// Parent chunks cover whole document sections; child chunks are sub-splits
/// of oversized parents produced by the [`SemanticChunker`].
///
/// # Parent-child wiring
///
/// `parent_chunk_id` on a child is the **`chunk_index`** of its parent chunk
/// (a local, sequential integer, not a DB row ID — DB IDs are assigned later
/// during ingest).
///
/// # Heading detection
///
/// A line starting with one or more `#` characters followed by a space is
/// treated as a heading.  This deliberately ignores `#` inside code blocks or
/// comments (they would not appear as the very first character of a line).
///
/// # Example
///
/// ```rust
/// use cascade_rag::chunk::hierarchical::HierarchicalChunker;
/// use cascade_rag::chunk::{ChunkerConfig, Chunker};
/// use std::path::PathBuf;
///
/// let c = HierarchicalChunker::new(ChunkerConfig::default());
/// let path = PathBuf::from("doc.md");
/// let text = "# Alpha\n\nFirst section.\n\n# Beta\n\nSecond section.";
/// let chunks = Chunker::chunk(&c, &path, text).unwrap();
/// // Two sections → two parents (both small, no children).
/// assert_eq!(chunks.len(), 2);
/// assert_eq!(chunks[0].heading_path.as_deref(), Some("# Alpha"));
/// assert_eq!(chunks[1].heading_path.as_deref(), Some("# Beta"));
/// ```
///
/// SPORT: MASTER-CRATES.md → cascade-rag::chunk::HierarchicalChunker
#[derive(Debug)]
pub struct HierarchicalChunker {
    /// Chunking configuration (size caps passed through to the inner semantic
    /// chunker for child splitting).
    pub config: ChunkerConfig,
}

impl HierarchicalChunker {
    /// Construct with a custom config.
    pub fn new(config: ChunkerConfig) -> Self {
        Self { config }
    }
}

impl Default for HierarchicalChunker {
    fn default() -> Self {
        Self::new(ChunkerConfig::default())
    }
}

// ── Section splitter ──────────────────────────────────────────────────────────

/// A raw text section with its byte offset in the source.
struct Section {
    /// Section text (may begin with a heading line).
    text: String,
    /// Byte offset in the original source where this section begins.
    byte_start: usize,
    /// Optional heading extracted from the first `#`-prefixed line.
    heading: Option<String>,
}

/// Returns `true` if `line` is a Markdown heading (`# …`, `## …`, etc.).
///
/// Only matches lines whose very first character is `#` followed by a space,
/// so `#` inside inline code, comments, or mid-sentence is never matched.
#[inline]
fn is_heading(line: &str) -> bool {
    let stripped = line.trim_start_matches('#');
    !stripped.is_empty() && stripped.starts_with(' ') && line.starts_with('#')
}

/// Split `text` into sections at heading boundaries **or** double-newline
/// paragraph boundaries (whichever fires first).
///
/// Rules:
/// - A heading line (starts with `#`) always starts a new section.
/// - Two or more consecutive blank lines start a new section for flat prose.
/// - The very first non-blank content always opens section 0.
fn split_sections(text: &str) -> Vec<Section> {
    let mut sections: Vec<Section> = Vec::new();
    let mut current_text = String::new();
    let mut current_byte_start: usize = 0;
    let mut current_heading: Option<String> = None;
    let mut byte_cursor: usize = 0;
    let mut blank_run: usize = 0;

    // `split_inclusive` preserves the `\n` so byte arithmetic is exact.
    for line in text.split_inclusive('\n') {
        let line_trimmed = line.trim_end_matches('\n').trim_end_matches('\r');
        let line_byte_len = line.len();

        if line_trimmed.is_empty() {
            blank_run += 1;
            // Flush on double-blank only if we already have content.
            if blank_run >= 2 && !current_text.trim().is_empty() {
                sections.push(Section {
                    text: current_text.trim().to_string(),
                    byte_start: current_byte_start,
                    heading: current_heading.take(),
                });
                current_text = String::new();
                current_byte_start = byte_cursor + line_byte_len;
            } else {
                current_text.push_str(line);
            }
        } else {
            blank_run = 0;
            if is_heading(line_trimmed) && !current_text.trim().is_empty() {
                // Heading fires a section boundary for the accumulated text.
                sections.push(Section {
                    text: current_text.trim().to_string(),
                    byte_start: current_byte_start,
                    heading: current_heading.take(),
                });
                current_text = String::new();
                current_byte_start = byte_cursor;
                current_heading = Some(line_trimmed.to_string());
            } else if is_heading(line_trimmed) && current_text.trim().is_empty() {
                // Opening heading with no prior content.
                current_byte_start = byte_cursor;
                current_heading = Some(line_trimmed.to_string());
            }
            current_text.push_str(line);
        }

        byte_cursor += line_byte_len;
    }

    // Flush remainder.
    if !current_text.trim().is_empty() {
        sections.push(Section {
            text: current_text.trim().to_string(),
            byte_start: current_byte_start,
            heading: current_heading.take(),
        });
    }

    // Fallback: whole document as one section.
    if sections.is_empty() {
        sections.push(Section {
            text: text.to_string(),
            byte_start: 0,
            heading: None,
        });
    }

    sections
}

// ── Sync Chunker impl (T-P4-E01-10) ──────────────────────────────────────────

impl Chunker for HierarchicalChunker {
    /// Split `text` into a hierarchy of parent + child [`Chunk`]s.
    ///
    /// # Invariants
    ///
    /// - Every parent has `parent_chunk_id = None`.
    /// - Every child has `parent_chunk_id = Some(parent.chunk_index)`.
    /// - Children carry the same `heading_path` as their parent.
    /// - `chunk_index` is a monotonically-increasing global counter across
    ///   all emitted chunks (parent emitted first, then its children, then
    ///   the next parent, etc.).
    fn chunk(&self, source_path: &Path, text: &str) -> cascade_types::error::Result<Vec<Chunk>> {
        if text.is_empty() {
            return Ok(vec![]);
        }

        let max_parent = self.config.max_chunk_chars * PARENT_SIZE_MULTIPLIER;
        let sections = split_sections(text);

        debug!(
            section_count = sections.len(),
            max_parent, "hierarchical: split sections"
        );

        let inner = SemanticChunker::with_config(self.config.clone());
        let mut result: Vec<Chunk> = Vec::new();
        let mut global_idx: usize = 0;

        for section in &sections {
            // Truncate the parent text to the parent size cap at a char boundary.
            let parent_text: String = if section.text.len() > max_parent {
                let mut end = max_parent;
                while end > 0 && !section.text.is_char_boundary(end) {
                    end -= 1;
                }
                section.text[..end].to_string()
            } else {
                section.text.clone()
            };

            let parent_byte_start = section.byte_start;
            let parent_byte_end = parent_byte_start + parent_text.len();

            let parent_line_start = text[..parent_byte_start]
                .bytes()
                .filter(|&b| b == b'\n')
                .count()
                + 1;
            let parent_line_end = text[..parent_byte_end.min(text.len())]
                .bytes()
                .filter(|&b| b == b'\n')
                .count()
                + 1;

            let parent_idx = global_idx;
            global_idx += 1;

            result.push(Chunk {
                chunk_id: None,
                source_path: source_path.to_path_buf(),
                chunk_index: parent_idx,
                text: parent_text.clone(),
                char_start: parent_byte_start,
                char_end: parent_byte_end,
                line_start: parent_line_start,
                line_end: parent_line_end,
                parent_chunk_id: None,
                heading_path: section.heading.clone(),
                metadata: HashMap::new(),
            });

            // Emit children only when the parent exceeds the regular size cap.
            if parent_text.len() > self.config.max_chunk_chars {
                let child_raw = Chunker::chunk(&inner, source_path, &parent_text)?;
                for mut child in child_raw {
                    // Shift byte offsets to be relative to the original source.
                    child.char_start += parent_byte_start;
                    child.char_end += parent_byte_start;
                    child.line_start = text[..child.char_start.min(text.len())]
                        .bytes()
                        .filter(|&b| b == b'\n')
                        .count()
                        + 1;
                    child.line_end = text[..child.char_end.min(text.len())]
                        .bytes()
                        .filter(|&b| b == b'\n')
                        .count()
                        + 1;

                    child.chunk_index = global_idx;
                    global_idx += 1;
                    // Wire to parent by local chunk_index (not DB id).
                    child.parent_chunk_id = Some(parent_idx as i64);
                    // Propagate heading_path from parent.
                    child.heading_path = section.heading.clone();

                    result.push(child);
                }
            }
        }

        debug!(chunk_count = result.len(), "hierarchical: emitted chunks");
        Ok(result)
    }

    fn strategy_name(&self) -> &str {
        "hierarchical-section"
    }
}

// ── Async TypesChunker shim ───────────────────────────────────────────────────

#[async_trait]
impl TypesChunker for HierarchicalChunker {
    /// Async shim: delegates to the sync [`Chunker`] impl and wraps results
    /// in [`TypesChunk`] structs for the cascade-types pipeline.
    async fn chunk(&self, doc: &Document, _opts: &ChunkOpts) -> Result<Vec<TypesChunk>> {
        let path = doc
            .source_path
            .clone()
            .unwrap_or_else(|| std::path::PathBuf::from("mem"));

        let local_chunks = Chunker::chunk(self, &path, &doc.content)?;

        Ok(local_chunks
            .into_iter()
            .map(|c| {
                let mut extra = HashMap::new();
                if let Some(pid) = c.parent_chunk_id {
                    extra.insert("parent_chunk_id".into(), json!(pid));
                }
                if let Some(ref hp) = c.heading_path {
                    extra.insert("heading_path".into(), json!(hp));
                }
                TypesChunk {
                    id: format!("{}-{}", path.display(), c.chunk_index),
                    text: c.text,
                    metadata: ChunkMetadata {
                        start_byte: c.char_start,
                        end_byte: c.char_end,
                        source_path: Some(path.clone()),
                        start_line: Some(c.line_start),
                        end_line: Some(c.line_end),
                        chunk_index: c.chunk_index,
                        total_chunks: 0,
                        extra,
                    },
                }
            })
            .collect())
    }

    fn name(&self) -> &str {
        "hierarchical-section"
    }
}

// ── Tests (T-P4-E01-10) ──────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn path() -> PathBuf {
        PathBuf::from("test.md")
    }

    fn chunker(max: usize) -> HierarchicalChunker {
        HierarchicalChunker::new(ChunkerConfig {
            max_chunk_chars: max,
            overlap_chars: 20,
            min_chunk_chars: 10,
            ..ChunkerConfig::default()
        })
    }

    // ── T1: 3-section document → 3 parents, correct heading_path ─────────────

    #[test]
    fn three_sections_three_parents() {
        let text = "# Alpha\n\nContent of section alpha.\n\n# Beta\n\nContent of section beta.\n\n# Gamma\n\nContent of section gamma.";
        let chunks = Chunker::chunk(&chunker(2000), &path(), text).unwrap();
        let parents: Vec<_> = chunks
            .iter()
            .filter(|c| c.parent_chunk_id.is_none())
            .collect();
        assert_eq!(parents.len(), 3, "expected 3 parent chunks");
        assert_eq!(parents[0].heading_path.as_deref(), Some("# Alpha"));
        assert_eq!(parents[1].heading_path.as_deref(), Some("# Beta"));
        assert_eq!(parents[2].heading_path.as_deref(), Some("# Gamma"));
    }

    // ── T2: oversize section produces children with correct parent_chunk_id ───

    #[test]
    fn oversize_section_produces_children() {
        // Build a section that is definitely > max_chunk_chars.
        let long_content = "This is a sentence. ".repeat(60); // ~1200 chars
        let text = format!("# Big Section\n\n{long_content}");
        let c = chunker(200);
        let chunks = Chunker::chunk(&c, &path(), &text).unwrap();

        let parents: Vec<_> = chunks
            .iter()
            .filter(|c| c.parent_chunk_id.is_none())
            .collect();
        let children: Vec<_> = chunks
            .iter()
            .filter(|c| c.parent_chunk_id.is_some())
            .collect();

        assert_eq!(parents.len(), 1, "one parent");
        assert!(
            !children.is_empty(),
            "children expected for oversize section"
        );

        let parent_idx = parents[0].chunk_index;
        for child in &children {
            assert_eq!(
                child.parent_chunk_id,
                Some(parent_idx as i64),
                "child parent_chunk_id must equal parent chunk_index"
            );
            assert_eq!(
                child.heading_path.as_deref(),
                Some("# Big Section"),
                "child heading_path must match parent"
            );
        }
    }

    // ── T3: level nesting — subsection heading (##) detected ─────────────────

    #[test]
    fn subsection_heading_detected() {
        let text = "# Level 1\n\nIntro paragraph.\n\n## Level 2\n\nSubsection content here.";
        let chunks = Chunker::chunk(&chunker(2000), &path(), text).unwrap();
        let paths: Vec<Option<&str>> = chunks.iter().map(|c| c.heading_path.as_deref()).collect();
        assert!(paths.contains(&Some("# Level 1")), "# heading not found");
        assert!(paths.contains(&Some("## Level 2")), "## heading not found");
    }

    // ── T4: children are textual sub-spans of parent ──────────────────────────

    #[test]
    fn children_are_subspans_of_parent() {
        let long_content = "Alpha beta gamma delta. ".repeat(50); // ~1200 chars
        let text = format!("# Doc\n\n{long_content}");
        let c = chunker(300);
        let chunks = Chunker::chunk(&c, &path(), &text).unwrap();

        let parent = chunks.iter().find(|c| c.parent_chunk_id.is_none()).unwrap();
        let children: Vec<_> = chunks
            .iter()
            .filter(|c| c.parent_chunk_id.is_some())
            .collect();

        assert!(!children.is_empty(), "expected children");
        for child in &children {
            assert!(
                parent.text.contains(child.text.as_str()),
                "child text must be a substring of parent text"
            );
        }
    }

    // ── T5: flat document (no headings) emits single parent ──────────────────

    #[test]
    fn flat_doc_single_parent() {
        let text = "No headings here. Just a plain paragraph that is not very long.";
        let chunks = Chunker::chunk(&chunker(2000), &path(), text).unwrap();
        let parents: Vec<_> = chunks
            .iter()
            .filter(|c| c.parent_chunk_id.is_none())
            .collect();
        assert_eq!(parents.len(), 1, "flat doc → one parent");
        assert!(
            parents[0].heading_path.is_none(),
            "no heading_path for flat doc"
        );
    }

    // ── T6: empty input → empty output ───────────────────────────────────────

    #[test]
    fn empty_input_empty_output() {
        let chunks = Chunker::chunk(&chunker(2000), &path(), "").unwrap();
        assert!(chunks.is_empty());
    }

    // ── T7: chunk_index is monotonically increasing across parents+children ───

    #[test]
    fn chunk_index_monotonic() {
        let long_content = "Word sentence fragment here. ".repeat(60);
        let text = format!("# A\n\n{long_content}\n\n# B\n\nShort.\n\n# C\n\n{long_content}");
        let chunks = Chunker::chunk(&chunker(200), &path(), &text).unwrap();
        for (i, chunk) in chunks.iter().enumerate() {
            assert_eq!(chunk.chunk_index, i, "chunk_index[{i}] must equal {i}");
        }
    }

    // ── T8: strategy_name ────────────────────────────────────────────────────

    #[test]
    fn strategy_name() {
        assert_eq!(
            HierarchicalChunker::default().strategy_name(),
            "hierarchical-section"
        );
    }
}
