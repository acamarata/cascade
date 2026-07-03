//! Markdown-aware chunker.
//!
//! Uses the heading hierarchy as natural chunk boundaries.  A new chunk is
//! started whenever a heading (ATX `#…` or Setext `===`/`---`) is encountered;
//! content under a heading stays together until the next heading of equal or
//! higher level.
//!
//! ## Key guarantees
//!
//! - **Code fences** (` ``` `) are atomic — never split mid-block.
//! - **YAML/TOML front-matter** (`---` delimiter at file start) is stripped from
//!   chunk text and stored in `ChunkMetadata::extra["frontmatter"]`.
//! - **`heading_path`** breadcrumb (`"H1 > H2 > H3"`) is stored in
//!   `ChunkMetadata::extra["heading_path"]` for the async pipeline and in
//!   `Chunk::heading_path` for the sync local-RAG pipeline.
//! - **Setext headings** (`===` → H1, `---` → H2) are detected without
//!   false-positives on bare horizontal rules (no preceding text line).
//! - **Size-cap fallback**: sections exceeding `max_chunk_chars` are further
//!   split by [`SemanticChunker`] with `parent_chunk_id` set on children.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk::MarkdownChunker
//!
//! Sprint ticket: T-P4-E01-11

use async_trait::async_trait;
use serde_json::json;
use std::collections::HashMap;
use std::path::Path;
use tracing::debug;

use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkMetadata, ChunkOpts, Chunker as TypesChunker, Document},
    error::Result,
};

use super::semantic::SemanticChunker;
use super::{Chunk, Chunker, ChunkerConfig};

// ── Async cascade-types pipeline impl ────────────────────────────────────────

/// Markdown-aware chunker splitting at heading boundaries.
///
/// Implements both:
/// - [`cascade_types::Chunker`] (async) — used by `StrategyChunker` for the
///   document indexing pipeline.
/// - [`super::Chunker`] (sync) — used directly by the local RAG pipeline.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::MarkdownChunker
#[derive(Debug, Default)]
pub struct MarkdownChunker;

#[async_trait]
impl TypesChunker for MarkdownChunker {
    /// Split a markdown document into heading-bounded chunks (async pipeline).
    ///
    /// # Metadata keys (in `extra`)
    ///
    /// | Key | Value |
    /// |---|---|
    /// | `frontmatter` | Raw YAML/TOML front-matter text (omitted when absent) |
    /// | `heading_path` | Breadcrumb: `"H1 title > H2 title"` (omitted when empty) |
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<TypesChunk>> {
        let (frontmatter, body) = strip_frontmatter(&doc.content);
        let sections = split_by_headings(body);
        let mut chunks: Vec<TypesChunk> = Vec::new();

        for section in &sections {
            let text = section.text.trim();
            if text.is_empty() {
                continue;
            }

            // Size-cap fallback: if section exceeds target_size, delegate to
            // semantic chunker so no single chunk is pathologically large.
            if text.len() > opts.target_size && opts.target_size > 0 {
                let sub_doc = Document {
                    content: text.to_string(),
                    source_path: doc.source_path.clone(),
                    mime_type: Some("text/plain".into()),
                    metadata: std::collections::HashMap::new(),
                };
                let sub_chunks =
                    TypesChunker::chunk(&SemanticChunker::default(), &sub_doc, opts).await?;
                for mut sc in sub_chunks {
                    if let Some(hp) = section.heading_path.as_deref() {
                        sc.metadata.extra.insert("heading_path".into(), json!(hp));
                    }
                    if let Some(fm) = &frontmatter {
                        sc.metadata.extra.insert("frontmatter".into(), json!(fm));
                    }
                    chunks.push(sc);
                }
                continue;
            }

            let byte_start = doc.content.find(text).unwrap_or(0);
            let byte_end = byte_start + text.len();

            let mut extra = HashMap::new();
            if let Some(fm) = &frontmatter {
                extra.insert("frontmatter".into(), json!(fm));
            }
            if let Some(hp) = &section.heading_path {
                extra.insert("heading_path".into(), json!(hp));
            }

            let idx = chunks.len();
            chunks.push(TypesChunk {
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
                    start_line: section.line_start,
                    end_line: section.line_end,
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
        debug!(target: "cascade_rag::chunk::markdown", chunks = total, "markdown async chunk");
        Ok(chunks)
    }

    fn name(&self) -> &str {
        "markdown"
    }
}

// ── Sync local-RAG pipeline impl ─────────────────────────────────────────────

impl Chunker for MarkdownChunker {
    /// Split a markdown document into heading-bounded chunks (sync local-RAG pipeline).
    ///
    /// Front-matter is stripped; `heading_path` is populated on each chunk.
    /// Sections exceeding `config.max_chunk_chars` are further split using
    /// [`SemanticChunker`] with `parent_chunk_id` set to the parent's index.
    fn chunk(&self, source_path: &Path, text: &str) -> cascade_types::error::Result<Vec<Chunk>> {
        let config = ChunkerConfig::default();
        self.chunk_with_config(source_path, text, &config)
    }

    fn strategy_name(&self) -> &str {
        "markdown"
    }
}

impl MarkdownChunker {
    /// Chunk with an explicit [`ChunkerConfig`] (used internally and by tests).
    pub fn chunk_with_config(
        &self,
        source_path: &Path,
        text: &str,
        config: &ChunkerConfig,
    ) -> cascade_types::error::Result<Vec<Chunk>> {
        let (frontmatter, body) = strip_frontmatter(text);
        let sections = split_by_headings(body);
        let mut chunks: Vec<Chunk> = Vec::new();

        for section in &sections {
            let section_text = section.text.trim();
            if section_text.is_empty() {
                continue;
            }

            // Size-cap fallback: oversized sections go through SemanticChunker.
            if section_text.len() > config.max_chunk_chars {
                let parent_idx = chunks.len() as i64;
                let sem = SemanticChunker {
                    config: config.clone(),
                };
                let sub = Chunker::chunk(&sem, source_path, section_text)?;
                for mut sc in sub {
                    sc.parent_chunk_id = Some(parent_idx);
                    sc.heading_path = section.heading_path.clone();
                    if let Some(fm) = &frontmatter {
                        sc.metadata.insert("frontmatter".to_string(), fm.clone());
                    }
                    sc.chunk_index = chunks.len();
                    chunks.push(sc);
                }
                continue;
            }

            let byte_start = text.find(section_text).unwrap_or(0);
            let byte_end = byte_start + section_text.len();
            let mut metadata = HashMap::new();
            if let Some(fm) = &frontmatter {
                metadata.insert("frontmatter".to_string(), fm.clone());
            }

            chunks.push(Chunk {
                chunk_id: None,
                source_path: source_path.to_path_buf(),
                chunk_index: chunks.len(),
                text: section_text.to_string(),
                char_start: byte_start,
                char_end: byte_end,
                line_start: section.line_start.unwrap_or(1),
                line_end: section.line_end.unwrap_or(1),
                parent_chunk_id: None,
                heading_path: section.heading_path.clone(),
                metadata,
            });
        }

        debug!(
            target: "cascade_rag::chunk::markdown",
            chunks = chunks.len(),
            "markdown sync chunk"
        );
        Ok(chunks)
    }
}

// ── Internal types ────────────────────────────────────────────────────────────

/// A parsed markdown section before it is converted to a `Chunk`.
#[derive(Debug)]
struct Section {
    /// Text of this section (heading line + body).
    text: String,
    /// Breadcrumb path at this section's position.
    heading_path: Option<String>,
    /// 1-based first line (None = unknown).
    line_start: Option<usize>,
    /// 1-based last line (None = unknown).
    line_end: Option<usize>,
}

// ── Front-matter stripping ────────────────────────────────────────────────────

/// Strip YAML/TOML front-matter delimited by `---` at the very start.
///
/// Returns `(Option<frontmatter_text>, rest_of_document)`.
///
/// # Rules
///
/// - First non-whitespace line must be exactly `---`; otherwise no stripping.
/// - Closing delimiter is the next line that is `---` or `+++` (TOML).
/// - If the opening delimiter has no matching closing delimiter, no stripping
///   occurs (avoids consuming the whole document).
fn strip_frontmatter(text: &str) -> (Option<String>, &str) {
    let trimmed = text.trim_start();
    // Must start with "---" followed by newline (not "--- more text")
    if !trimmed.starts_with("---\n") && trimmed != "---" {
        return (None, text);
    }
    let after_open = &trimmed[4..]; // skip "---\n"
                                    // Find the closing "---" or "+++" on its own line.
    for (i, line) in after_open.lines().enumerate() {
        if line == "---" || line == "+++" {
            // Compute byte offset of the closing delimiter in after_open.
            let close_byte: usize = after_open
                .lines()
                .take(i)
                .map(|l| l.len() + 1) // +1 for '\n'
                .sum();
            let fm = after_open[..close_byte].trim().to_string();
            let rest_start = close_byte + line.len() + 1; // skip delimiter + '\n'
            let rest = if rest_start <= after_open.len() {
                &after_open[rest_start..]
            } else {
                ""
            };
            return (Some(fm), rest);
        }
    }
    // No closing delimiter found — don't strip.
    (None, text)
}

// ── Heading detection helpers ─────────────────────────────────────────────────

/// Heading level (1–6) and title extracted from an ATX heading line.
fn parse_atx_heading(line: &str) -> Option<(usize, &str)> {
    let hashes = line.chars().take_while(|&c| c == '#').count();
    if hashes == 0 || hashes > 6 {
        return None;
    }
    let rest = &line[hashes..];
    if !rest.starts_with(' ') {
        return None;
    }
    let title = rest[1..].trim_end_matches('#').trim();
    if title.is_empty() {
        return None;
    }
    Some((hashes, title))
}

/// Level + title for a Setext heading given the heading line and the underline.
///
/// `underline` must be all `=` (H1) or all `-` (H2); must be at least 1 char.
/// The heading line must be non-empty (avoids false-positives on `---` alone).
fn parse_setext_heading<'a>(heading_line: &'a str, underline: &str) -> Option<(usize, &'a str)> {
    let title = heading_line.trim();
    if title.is_empty() {
        return None;
    }
    let trimmed = underline.trim();
    if trimmed.is_empty() {
        return None;
    }
    if trimmed.chars().all(|c| c == '=') {
        return Some((1, title));
    }
    if trimmed.chars().all(|c| c == '-') {
        return Some((2, title));
    }
    None
}

// ── Main split algorithm ──────────────────────────────────────────────────────

/// Split markdown `text` into heading-bounded [`Section`]s.
///
/// Algorithm:
/// 1. Track `heading_stack: Vec<Option<String>>` indexed 1–6 for heading level.
/// 2. On ATX or Setext heading: flush current section, start new one, update
///    `heading_stack` and build breadcrumb.
/// 3. Inside a `` ``` `` code fence, never treat a line as a heading.
/// 4. Setext underline consumed only if the previous line was non-empty text
///    (not itself a heading).
fn split_by_headings(text: &str) -> Vec<Section> {
    let mut sections: Vec<Section> = Vec::new();
    let mut current_text = String::new();
    let mut current_heading_path: Option<String> = None;
    let mut current_line_start: usize = 1;
    let mut in_code_fence = false;
    // heading_stack[0] unused; [1]=H1 … [6]=H6
    let mut heading_stack: Vec<Option<String>> = vec![None; 7];

    // We need one-line lookahead for Setext detection, so collect lines.
    let lines: Vec<&str> = text.lines().collect();
    let total_lines = lines.len();
    let mut i = 0usize;

    while i < total_lines {
        let line = lines[i];
        let line_no = i + 1; // 1-based

        // ── Code fence toggle ───────────────────────────────────────────────
        if line.trim_start().starts_with("```") || line.trim_start().starts_with("~~~") {
            in_code_fence = !in_code_fence;
            current_text.push_str(line);
            current_text.push('\n');
            i += 1;
            continue;
        }

        if in_code_fence {
            current_text.push_str(line);
            current_text.push('\n');
            i += 1;
            continue;
        }

        // ── Setext heading lookahead ────────────────────────────────────────
        // Only when the *next* line is an all-= or all-- underline.
        if i + 1 < total_lines {
            let next = lines[i + 1].trim();
            let all_eq = !next.is_empty() && next.chars().all(|c| c == '=');
            let all_dash = !next.is_empty() && next.chars().all(|c| c == '-');
            if all_eq || all_dash {
                if let Some((level, title)) = parse_setext_heading(line, lines[i + 1]) {
                    // Flush current section.
                    flush_section(
                        &mut sections,
                        &mut current_text,
                        current_heading_path.clone(),
                        current_line_start,
                        line_no.saturating_sub(1),
                    );
                    // Update heading stack.
                    heading_stack[level] = Some(title.to_string());
                    // Clear deeper levels.
                    for slot in heading_stack.iter_mut().skip(level + 1) {
                        *slot = None;
                    }
                    current_heading_path = Some(build_breadcrumb(&heading_stack));
                    // Start new section with the heading + underline.
                    current_text = format!("{line}\n{}\n", lines[i + 1]);
                    current_line_start = line_no;
                    i += 2; // consume heading + underline
                    continue;
                }
            }
        }

        // ── ATX heading ────────────────────────────────────────────────────
        if let Some((level, title)) = parse_atx_heading(line) {
            flush_section(
                &mut sections,
                &mut current_text,
                current_heading_path.clone(),
                current_line_start,
                line_no.saturating_sub(1),
            );
            heading_stack[level] = Some(title.to_string());
            for slot in heading_stack.iter_mut().skip(level + 1) {
                *slot = None;
            }
            current_heading_path = Some(build_breadcrumb(&heading_stack));
            current_text = format!("{line}\n");
            current_line_start = line_no;
            i += 1;
            continue;
        }

        // ── Regular line ────────────────────────────────────────────────────
        current_text.push_str(line);
        current_text.push('\n');
        i += 1;
    }

    // Flush final section.
    flush_section(
        &mut sections,
        &mut current_text,
        current_heading_path,
        current_line_start,
        total_lines,
    );

    // Ensure empty document returns no sections.
    sections
}

/// Flush `current_text` as a new section if non-empty, then reset it.
fn flush_section(
    sections: &mut Vec<Section>,
    current_text: &mut String,
    heading_path: Option<String>,
    line_start: usize,
    line_end: usize,
) {
    if !current_text.trim().is_empty() {
        sections.push(Section {
            text: current_text.clone(),
            heading_path,
            line_start: Some(line_start),
            line_end: Some(line_end),
        });
    }
    current_text.clear();
}

/// Build breadcrumb string from heading stack, e.g. `"Introduction > Basics"`.
fn build_breadcrumb(stack: &[Option<String>]) -> String {
    stack
        .iter()
        .filter_map(|s| s.as_deref())
        .collect::<Vec<_>>()
        .join(" > ")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    // Helper: sync chunk via default config.
    fn sync_chunk(text: &str) -> Vec<Chunk> {
        let m = MarkdownChunker;
        let path = PathBuf::from("test.md");
        m.chunk_with_config(&path, text, &ChunkerConfig::default())
            .expect("chunk should succeed")
    }

    // ── Front-matter ─────────────────────────────────────────────────────────

    #[test]
    fn frontmatter_stripped_from_chunk_text() {
        let doc = "---\ntitle: Hello\ndate: 2026-01-01\n---\n\n# Introduction\n\nSome content.";
        let chunks = sync_chunk(doc);
        assert!(!chunks.is_empty(), "expected at least one chunk");
        for c in &chunks {
            assert!(
                !c.text.contains("title: Hello"),
                "front-matter must not appear in chunk text; got: {:?}",
                c.text
            );
            // Front-matter stored in metadata.
            assert!(
                c.metadata.contains_key("frontmatter"),
                "chunk must carry frontmatter metadata"
            );
        }
    }

    #[test]
    fn no_frontmatter_doc_unchanged() {
        let doc = "# Hello\n\nParagraph here.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 1);
        assert!(!chunks[0].metadata.contains_key("frontmatter"));
    }

    #[test]
    fn frontmatter_without_closing_delimiter_not_stripped() {
        // No closing "---" → front-matter detection must NOT consume the whole doc.
        let doc = "---\ntitle: Oops\n\n# Heading\n\nContent.";
        let chunks = sync_chunk(doc);
        // Should still produce at least one chunk (not empty).
        assert!(!chunks.is_empty());
    }

    // ── ATX heading splits ────────────────────────────────────────────────────

    #[test]
    fn splits_on_h1_and_h2() {
        let doc = "# First\n\nContent one.\n\n## Sub\n\nContent two.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 2, "expected 2 chunks (H1 and H2 sections)");
        assert!(chunks[0].text.contains("First"));
        assert!(chunks[1].text.contains("Sub"));
    }

    #[test]
    fn three_h1s_give_three_chunks() {
        let doc = "# A\n\nText A.\n\n# B\n\nText B.\n\n# C\n\nText C.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 3);
    }

    #[test]
    fn content_before_any_heading_is_its_own_chunk() {
        let doc = "Preamble text here.\n\n# Section\n\nBody.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 2);
        assert!(chunks[0].text.contains("Preamble"));
        assert_eq!(chunks[0].heading_path, None);
    }

    #[test]
    fn empty_document_returns_no_chunks() {
        let chunks = sync_chunk("");
        assert!(chunks.is_empty());
    }

    #[test]
    fn whitespace_only_document_returns_no_chunks() {
        let chunks = sync_chunk("   \n\n   ");
        assert!(chunks.is_empty());
    }

    // ── Heading path (breadcrumb) ─────────────────────────────────────────────

    #[test]
    fn heading_path_reflects_nesting() {
        let doc = "# Top\n\nIntro.\n\n## Middle\n\nMiddle text.\n\n### Deep\n\nDeep text.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 3);

        // H1 chunk
        assert_eq!(chunks[0].heading_path.as_deref(), Some("Top"));

        // H2 chunk
        let h2_path = chunks[1].heading_path.as_deref().unwrap_or("");
        assert!(
            h2_path.contains("Top") && h2_path.contains("Middle"),
            "H2 breadcrumb must include H1 and H2; got: {h2_path}"
        );

        // H3 chunk
        let h3_path = chunks[2].heading_path.as_deref().unwrap_or("");
        assert!(
            h3_path.contains("Top") && h3_path.contains("Middle") && h3_path.contains("Deep"),
            "H3 breadcrumb must include H1>H2>H3; got: {h3_path}"
        );
    }

    #[test]
    fn heading_path_resets_deeper_levels_on_new_h1() {
        // After the second H1, H2 context from the first H1 must be cleared.
        let doc = "# First\n\n## Sub\n\nSub text.\n\n# Second\n\nSecond text.";
        let chunks = sync_chunk(doc);
        let second_path = chunks
            .last()
            .and_then(|c| c.heading_path.as_deref())
            .unwrap_or("");
        assert!(
            !second_path.contains("Sub"),
            "Second H1 must not carry Sub in its breadcrumb; got: {second_path}"
        );
        assert!(
            second_path.contains("Second"),
            "Second H1 breadcrumb must contain 'Second'; got: {second_path}"
        );
    }

    #[test]
    fn heading_path_h2_without_prior_h1() {
        // H2 appearing without an H1 must still produce a valid breadcrumb.
        let doc = "## Orphan Sub\n\nContent.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 1);
        let path = chunks[0].heading_path.as_deref().unwrap_or("");
        assert!(path.contains("Orphan Sub"));
    }

    // ── Code-fence integrity ──────────────────────────────────────────────────

    #[test]
    fn code_fence_not_split() {
        let doc = "# Section\n\nSome text.\n\n```rust\nfn main() {\n    // # Not a heading\n}\n```\n\nMore text.";
        let chunks = sync_chunk(doc);
        // All content is under H1 so there should be exactly 1 chunk.
        assert_eq!(chunks.len(), 1, "code block must not create a split");
        assert!(
            chunks[0].text.contains("fn main()"),
            "code block content must be present"
        );
        assert!(
            chunks[0].text.contains("# Not a heading"),
            "comment inside fence must NOT trigger a heading split"
        );
    }

    #[test]
    fn heading_after_code_fence_splits_correctly() {
        let doc = "# First\n\n```\ncode\n```\n\n# Second\n\nText.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 2);
        assert!(chunks[0].text.contains("code"));
        assert!(chunks[1].text.contains("Second"));
    }

    // ── Setext headings ───────────────────────────────────────────────────────

    #[test]
    fn setext_h1_detected() {
        let doc = "Title Here\n==========\n\nContent under setext H1.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 1, "setext H1 should start a section");
        let path = chunks[0].heading_path.as_deref().unwrap_or("");
        assert!(
            path.contains("Title Here"),
            "heading_path must include setext H1 title; got: {path}"
        );
    }

    #[test]
    fn setext_h2_detected() {
        let doc = "# Top\n\nIntro.\n\nSubsection\n-----------\n\nSub content.";
        let chunks = sync_chunk(doc);
        assert_eq!(chunks.len(), 2);
        let sub_path = chunks[1].heading_path.as_deref().unwrap_or("");
        assert!(
            sub_path.contains("Subsection"),
            "heading_path must include setext H2 title; got: {sub_path}"
        );
    }

    #[test]
    fn bare_horizontal_rule_not_treated_as_setext() {
        // A line of "---" preceded by an empty line is a horizontal rule, not
        // a Setext heading — the Setext rule requires a non-empty text line
        // immediately before the underline.
        let doc = "# Section\n\nText.\n\n---\n\nMore text.";
        let chunks = sync_chunk(doc);
        // Should be 1 chunk (everything under the H1); the "---" is not a new heading.
        assert_eq!(
            chunks.len(),
            1,
            "bare '---' after empty line must NOT create a new section"
        );
    }

    // ── Async pipeline ────────────────────────────────────────────────────────

    #[tokio::test]
    async fn async_chunk_heading_path_in_extra() {
        use cascade_types::chunker::{ChunkOpts, Document};
        use cascade_types::Chunker as TypesChunker;

        let text =
            "---\ntitle: Test\n---\n\n# Intro\n\nParagraph one.\n\n## Details\n\nDetail text.";
        let doc = Document {
            content: text.to_string(),
            source_path: Some(PathBuf::from("test.md")),
            mime_type: Some("text/markdown".into()),
            metadata: std::collections::HashMap::new(),
        };
        let opts = ChunkOpts::default();
        let chunks = TypesChunker::chunk(&MarkdownChunker, &doc, &opts)
            .await
            .expect("chunk");

        assert!(!chunks.is_empty());
        // All chunks must NOT contain raw front-matter.
        for c in &chunks {
            assert!(
                !c.text.contains("title: Test"),
                "front-matter must not appear in async chunk text"
            );
        }
        // At least one chunk must carry heading_path in extra.
        let has_path = chunks
            .iter()
            .any(|c| c.metadata.extra.contains_key("heading_path"));
        assert!(
            has_path,
            "at least one chunk must have heading_path in extra"
        );
    }
}
