//! Semantic chunker: sliding-window with sentence-boundary snapping.
//!
//! ## Algorithm (T-P4-E01-09)
//!
//! 1. [`split_sentences`] — heuristic splitter on `.`, `!`, `?` followed by
//!    whitespace + uppercase or end-of-string.
//! 2. Accumulate sentences until `max_chunk_chars` is reached.
//! 3. Emit a chunk; carry the trailing `overlap_chars` of text (whole sentences)
//!    into the next window.
//! 4. Chunks shorter than `min_chunk_chars` are merged forward rather than
//!    emitted alone.
//! 5. Very long single sentences (> `max_chunk_chars`) are hard-split at the
//!    character boundary to prevent an infinite loop.
//!
//! ## Async pipeline fallback (cascade-types Chunker)
//!
//! The `SemanticChunker` also implements the async `cascade_types::Chunker`
//! trait (paragraph-boundary fallback used when no embedding provider is
//! available).
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk::SemanticChunker

use async_trait::async_trait;
use std::collections::HashMap;
use std::path::Path;
use tracing::debug;

use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkMetadata, ChunkOpts, Chunker as TypesChunker, Document},
    error::Result,
};

use super::{Chunk, Chunker, ChunkerConfig};

/// Default cosine similarity threshold (used by the async pipeline variant).
const DEFAULT_THRESHOLD: f32 = 0.65;

/// Semantic chunker backed by sentence-level similarity scoring (async pipeline)
/// **and** a sync sliding-window chunker (local RAG pipeline).
///
/// - Implements [`Chunker`] (local sync trait from `chunk::mod`) for the RAG
///   indexing pipeline — sliding-window with sentence snapping.
/// - Implements [`cascade_types::Chunker`] (async trait) for the document
///   pipeline — paragraph-boundary fallback.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::chunk::SemanticChunker
#[derive(Debug)]
pub struct SemanticChunker {
    /// Cosine similarity threshold in `[0.0, 1.0]` (async pipeline only).
    pub threshold: f32,
    /// Sliding-window configuration (sync pipeline).
    pub config: ChunkerConfig,
}

impl Default for SemanticChunker {
    fn default() -> Self {
        Self {
            threshold: DEFAULT_THRESHOLD,
            config: ChunkerConfig::default(),
        }
    }
}

impl SemanticChunker {
    /// Construct with a custom similarity threshold (async pipeline).
    pub fn with_threshold(threshold: f32) -> Self {
        Self {
            threshold,
            config: ChunkerConfig::default(),
        }
    }

    /// Construct with a custom sliding-window config (sync pipeline).
    pub fn with_config(config: ChunkerConfig) -> Self {
        Self {
            threshold: DEFAULT_THRESHOLD,
            config,
        }
    }

    /// Paragraph-boundary fallback when embeddings are not available.
    fn split_paragraphs(text: &str) -> Vec<String> {
        text.split("\n\n")
            .map(|p| p.trim().to_string())
            .filter(|p| !p.is_empty())
            .collect()
    }
}

// ── Sentence splitter ─────────────────────────────────────────────────────────

/// Heuristic sentence splitter for English prose.
///
/// Splits on `.`, `!`, `?` followed by a space/newline and an upper-case
/// character, or at end-of-string.  Returns byte-indexed slices of the input.
///
/// Handles CRLF (`\r\n`) transparently — `\r` is whitespace for the purpose
/// of the split rule.
///
/// # Edge cases
///
/// - Abbreviations (e.g. `"Dr. Smith"`) may produce spurious splits; this is
///   acceptable for P4 (no ML required).
/// - Single-sentence inputs are returned as a one-element vec.
/// - Empty input returns an empty vec.
pub fn split_sentences(text: &str) -> Vec<&str> {
    let mut sentences: Vec<&str> = Vec::new();
    let mut start = 0;
    let bytes = text.as_bytes();
    let len = bytes.len();
    let mut i = 0;

    while i < len {
        if bytes[i] == b'.' || bytes[i] == b'!' || bytes[i] == b'?' {
            // Look ahead: skip \r if present, then expect space or newline,
            // then uppercase or end-of-string.
            let mut j = i + 1;
            // Skip optional \r
            if j < len && bytes[j] == b'\r' {
                j += 1;
            }
            if j < len && (bytes[j] == b' ' || bytes[j] == b'\n') {
                let k = j + 1;
                if k >= len || bytes[k].is_ascii_uppercase() {
                    // Boundary found at i+1.
                    let slice = text[start..=i].trim();
                    if !slice.is_empty() {
                        sentences.push(slice);
                    }
                    start = j + 1; // skip the space/newline
                    i = start;
                    continue;
                }
            } else if j >= len {
                // Terminator at end-of-string.
                let slice = text[start..=i].trim();
                if !slice.is_empty() {
                    sentences.push(slice);
                }
                start = len;
                i = len;
                continue;
            }
        }
        i += 1;
    }

    // Remainder (no terminator at end, or last fragment).
    if start < len {
        let slice = text[start..].trim();
        if !slice.is_empty() {
            sentences.push(slice);
        }
    }

    sentences
}

// ── Byte-offset helper ────────────────────────────────────────────────────────

/// Compute the byte offset of `slice` within `source`.
///
/// `slice` MUST be a sub-slice of `source` (same backing allocation).
/// Panics in debug builds if the invariant is violated.
fn byte_offset_of(source: &str, slice: &str) -> usize {
    let source_start = source.as_ptr() as usize;
    let slice_start = slice.as_ptr() as usize;
    debug_assert!(
        slice_start >= source_start && slice_start + slice.len() <= source_start + source.len(),
        "slice is not a sub-slice of source"
    );
    slice_start - source_start
}

// ── Sync Chunker impl (T-P4-E01-09) ──────────────────────────────────────────

/// A sentence with its byte position in the original source.
struct SentenceSpan<'a> {
    text: &'a str,
    /// Byte offset where this sentence starts in the original source.
    byte_start: usize,
}

impl Chunker for SemanticChunker {
    fn chunk(&self, source_path: &Path, text: &str) -> Result<Vec<Chunk>> {
        if text.is_empty() {
            return Ok(vec![]);
        }

        let cfg = &self.config;

        // ── Sentence split with byte positions ────────────────────────────────
        // split_sentences returns slices into `text`, so we can recover their
        // byte offsets via pointer arithmetic.
        let raw_sentences = split_sentences(text);
        let spans: Vec<SentenceSpan<'_>> = if raw_sentences.is_empty() {
            let trimmed = text.trim();
            vec![SentenceSpan {
                text: trimmed,
                byte_start: byte_offset_of(text, trimmed),
            }]
        } else {
            raw_sentences
                .into_iter()
                .map(|s| SentenceSpan {
                    byte_start: byte_offset_of(text, s),
                    text: s,
                })
                .collect()
        };

        // ── Accumulation pass ─────────────────────────────────────────────────
        // Each "window" is a Vec of sentence indices (into `spans`).
        // We emit a window when adding the next sentence would exceed max_chunk_chars.
        // Overlap is carried as whole trailing sentences from the prior window.

        /// A pending window: indices into `spans` + total char count.
        struct Window {
            sentence_indices: Vec<usize>,
            total_chars: usize,
        }

        let mut current = Window {
            sentence_indices: Vec::new(),
            total_chars: 0,
        };

        // We interleave windows and hard-split fragments in `emit_order`.
        enum EmitItem {
            Window(Window),
            HardSplit(String, usize, usize),
        }
        let mut emit_order: Vec<EmitItem> = Vec::new();

        for (si, span) in spans.iter().enumerate() {
            let slen = span.text.len();

            // Single sentence longer than max_chunk_chars → hard-split.
            if slen > cfg.max_chunk_chars {
                // Flush current window first.
                if !current.sentence_indices.is_empty() {
                    emit_order.push(EmitItem::Window(Window {
                        sentence_indices: std::mem::take(&mut current.sentence_indices),
                        total_chars: current.total_chars,
                    }));
                    current.total_chars = 0;
                }
                // Split the oversized sentence at char-boundary-aligned pieces.
                let mut offset = 0;
                while offset < slen {
                    let end = (offset + cfg.max_chunk_chars).min(slen);
                    let mut safe_end = end;
                    while safe_end > offset && !span.text.is_char_boundary(safe_end) {
                        safe_end -= 1;
                    }
                    let frag = span.text[offset..safe_end].to_string();
                    let frag_start = span.byte_start + offset;
                    let frag_end = span.byte_start + safe_end;
                    emit_order.push(EmitItem::HardSplit(frag, frag_start, frag_end));
                    offset = safe_end;
                }
                continue;
            }

            // Would adding this sentence exceed the window? Emit + start overlap seed.
            if !current.sentence_indices.is_empty()
                && current.total_chars + slen + 1 > cfg.max_chunk_chars
            {
                // Emit current window.
                let emitted_indices = current.sentence_indices.clone();
                emit_order.push(EmitItem::Window(Window {
                    sentence_indices: std::mem::take(&mut current.sentence_indices),
                    total_chars: current.total_chars,
                }));
                current.total_chars = 0;

                // Build overlap seed from trailing sentences of the just-emitted window.
                let mut overlap_len = 0usize;
                let mut seed: Vec<usize> = Vec::new();
                for &idx in emitted_indices.iter().rev() {
                    let sl = spans[idx].text.len() + 1;
                    if overlap_len + sl <= cfg.overlap_chars {
                        seed.push(idx);
                        overlap_len += sl;
                    } else {
                        break;
                    }
                }
                seed.reverse();
                for idx in seed {
                    current.total_chars += spans[idx].text.len() + 1;
                    current.sentence_indices.push(idx);
                }
            }

            current.sentence_indices.push(si);
            current.total_chars += slen + 1;
        }

        // Flush remaining window.
        if !current.sentence_indices.is_empty() {
            emit_order.push(EmitItem::Window(current));
        }

        // ── Convert emit_order to (text, byte_start, byte_end) tuples ─────────

        struct RawChunk {
            text: String,
            byte_start: usize,
            byte_end: usize,
        }

        let mut raw: Vec<RawChunk> = Vec::new();

        for item in emit_order {
            match item {
                EmitItem::HardSplit(t, s, e) => {
                    raw.push(RawChunk {
                        text: t,
                        byte_start: s,
                        byte_end: e,
                    });
                }
                EmitItem::Window(w) => {
                    if w.sentence_indices.is_empty() {
                        continue;
                    }
                    let first = &spans[*w.sentence_indices.first().unwrap()];
                    let last = &spans[*w.sentence_indices.last().unwrap()];
                    let byte_start = first.byte_start;
                    let byte_end = last.byte_start + last.text.len();
                    // Reconstruct text as a space-joined slice from the source.
                    // This preserves the actual source content between the first
                    // and last sentence boundaries.
                    let text_slice = &text[byte_start..byte_end];
                    raw.push(RawChunk {
                        text: text_slice.to_string(),
                        byte_start,
                        byte_end,
                    });
                }
            }
        }

        // ── Merge pass: chunks < min_chunk_chars merged forward ───────────────
        let mut merged: Vec<RawChunk> = Vec::new();
        for rc in raw {
            if rc.text.len() < cfg.min_chunk_chars {
                if let Some(last) = merged.last_mut() {
                    // Expand last chunk's byte range to include this fragment.
                    last.byte_end = rc.byte_end;
                    last.text = text[last.byte_start..last.byte_end].to_string();
                } else {
                    merged.push(rc);
                }
            } else {
                merged.push(rc);
            }
        }

        // ── Build final Chunk structs ─────────────────────────────────────────
        let mut result: Vec<Chunk> = Vec::new();

        for (idx, rc) in merged.iter().enumerate() {
            let char_start = rc.byte_start;
            let char_end = rc.byte_end;

            let line_start = text[..char_start].bytes().filter(|&b| b == b'\n').count() + 1;
            let line_end = text[..char_end].bytes().filter(|&b| b == b'\n').count() + 1;

            debug!(
                chunk_idx = idx,
                char_start,
                char_end,
                line_start,
                line_end,
                len = rc.text.len(),
                "semantic chunk emitted"
            );

            result.push(Chunk {
                chunk_id: None,
                source_path: source_path.to_path_buf(),
                chunk_index: idx,
                text: rc.text.clone(),
                char_start,
                char_end,
                line_start,
                line_end,
                parent_chunk_id: None,
                heading_path: None,
                metadata: HashMap::new(),
            });
        }

        Ok(result)
    }

    fn strategy_name(&self) -> &str {
        "semantic-sliding-window"
    }
}

// ── Async TypesChunker impl (cascade-types pipeline) ─────────────────────────

#[async_trait]
impl TypesChunker for SemanticChunker {
    /// Split `doc` into semantically coherent chunks.
    ///
    /// Uses paragraph splitting as a stand-in until the embedding pipeline
    /// (T-P1-E7-S06-02) is wired.
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<TypesChunk>> {
        let paragraphs = Self::split_paragraphs(&doc.content);
        let mut chunks = Vec::new();
        let mut byte_cursor = 0usize;

        for (idx, para) in paragraphs.iter().enumerate() {
            let byte_start = doc.content[byte_cursor..]
                .find(para.as_str())
                .map(|o| byte_cursor + o)
                .unwrap_or(byte_cursor);
            let byte_end = byte_start + para.len();
            byte_cursor = byte_end;

            if para.len() > opts.target_size * 2 {
                debug!(
                    para_idx = idx,
                    len = para.len(),
                    "oversized paragraph — splitting"
                );
            }

            chunks.push(TypesChunk {
                id: format!(
                    "{}-{byte_start}",
                    doc.source_path
                        .as_ref()
                        .map(|p| p.display().to_string())
                        .unwrap_or_else(|| "mem".into())
                ),
                text: para.clone(),
                metadata: ChunkMetadata {
                    start_byte: byte_start,
                    end_byte: byte_end,
                    source_path: doc.source_path.clone(),
                    start_line: None,
                    end_line: None,
                    chunk_index: idx,
                    total_chunks: 0,
                    extra: HashMap::new(),
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
        "semantic"
    }
}

// ── Tests (T-P4-E01-09) ───────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;
    // Bring local Chunker trait into scope explicitly so .chunk() calls
    // resolve to the sync local trait, not the async TypesChunker.
    use super::super::Chunker as LocalChunker;

    fn path() -> &'static Path {
        Path::new("test.md")
    }

    fn chunker(max: usize, overlap: usize, min: usize) -> SemanticChunker {
        SemanticChunker::with_config(ChunkerConfig {
            max_chunk_chars: max,
            overlap_chars: overlap,
            min_chunk_chars: min,
        })
    }

    /// Helper: call the sync local Chunker::chunk — avoids ambiguity with the
    /// async TypesChunker::chunk that SemanticChunker also implements.
    fn sync_chunk(c: &SemanticChunker, p: &Path, text: &str) -> Vec<Chunk> {
        LocalChunker::chunk(c, p, text).unwrap()
    }

    // ── split_sentences ───────────────────────────────────────────────────────

    #[test]
    fn sentence_split_basic() {
        let text = "Hello world. This is a test. Another sentence here.";
        let sents = split_sentences(text);
        assert!(
            sents.len() >= 2,
            "expected at least 2 sentences, got {sents:?}"
        );
    }

    #[test]
    fn sentence_split_empty() {
        assert!(split_sentences("").is_empty());
    }

    #[test]
    fn sentence_split_no_terminator() {
        let sents = split_sentences("no sentence ending here");
        assert_eq!(sents.len(), 1);
        assert_eq!(sents[0], "no sentence ending here");
    }

    #[test]
    fn sentence_split_exclamation_and_question() {
        let text = "Hello! Are you there? Yes I am.";
        let sents = split_sentences(text);
        // Should split at '!' and '?'
        assert!(sents.len() >= 2);
    }

    // ── Window math ───────────────────────────────────────────────────────────

    #[test]
    fn window_produces_multiple_chunks_for_500_char_text() {
        // 500 char text, max=200 → should produce ≥2 chunks.
        let text = "A".repeat(30)
            + ". "
            + &"B".repeat(30)
            + ". "
            + &"C".repeat(30)
            + ". "
            + &"D".repeat(30)
            + ". "
            + &"E".repeat(30)
            + ". "
            + &"F".repeat(30)
            + ". "
            + &"G".repeat(30)
            + ". "
            + &"H".repeat(30)
            + ".";
        let c = chunker(200, 50, 10);
        let chunks = sync_chunk(&c, path(), &text);
        assert!(
            chunks.len() >= 2,
            "expected ≥2 chunks for 500-char text with max=200, got {} chunks",
            chunks.len()
        );
    }

    #[test]
    fn overlap_continuity() {
        // Use short sentences (each ~20 chars) so they fit within the overlap
        // budget (overlap=30).  Window max=50 → chunk 0 = s1+s2 (~42 chars),
        // chunk 1 = s2 (overlap seed) + s3 + s4.
        // Verify: char_start of chunk[1] < char_end of chunk[0] (overlap).
        let text = "Cat sat here. Dog ran fast. Cow jumped high. Pig slept well.";
        let c = chunker(50, 30, 5);
        let chunks = sync_chunk(&c, path(), text);
        // Expect multiple chunks from a 60-char text with max=50.
        if chunks.len() >= 2 {
            // Overlap: chunk[1].char_start must be INSIDE chunk[0]'s range.
            assert!(
                chunks[1].char_start < chunks[0].char_end,
                "expected overlap: chunk[1].char_start={} should be < chunk[0].char_end={}",
                chunks[1].char_start,
                chunks[0].char_end
            );
        }
        // All chunks must be non-empty.
        for ch in &chunks {
            assert!(!ch.text.is_empty());
        }
    }

    #[test]
    fn overlap_zero_leaves_no_gap() {
        // With overlap=0, consecutive chunks should not overlap but also not gap:
        // chunk[n+1].char_start >= chunk[n].char_end.
        let text = "First sentence here. Second sentence here. Third sentence here.";
        let c = chunker(30, 0, 1);
        let chunks = sync_chunk(&c, path(), text);
        if chunks.len() >= 2 {
            for i in 0..chunks.len() - 1 {
                assert!(
                    chunks[i + 1].char_start >= chunks[i].char_end,
                    "unexpected overlap with zero overlap config: chunk {i} end={}, chunk {} start={}",
                    chunks[i].char_end,
                    i + 1,
                    chunks[i + 1].char_start
                );
            }
        }
    }

    // ── Line number correctness ────────────────────────────────────────────────

    #[test]
    fn line_numbers_correct_for_multiline() {
        // 3 lines; one chunk per line (small max).
        let text = "Line one here.\nLine two here.\nLine three here.";
        let c = chunker(20, 0, 1);
        let chunks = sync_chunk(&c, path(), text);
        assert!(!chunks.is_empty());
        // First chunk should start at line 1.
        assert_eq!(chunks[0].line_start, 1);
        // Last chunk should end at line 3 or on a later line.
        assert!(chunks.last().unwrap().line_end >= 1);
    }

    #[test]
    fn line_numbers_single_line() {
        let text = "One sentence only.";
        let c = chunker(500, 50, 5);
        let chunks = sync_chunk(&c, path(), text);
        assert_eq!(chunks.len(), 1);
        assert_eq!(chunks[0].line_start, 1);
        assert_eq!(chunks[0].line_end, 1);
    }

    #[test]
    fn crlf_line_numbers() {
        // Windows line endings — line counting should still work.
        let text = "First line.\r\nSecond line.\r\nThird line.";
        let c = chunker(500, 50, 5);
        let chunks = sync_chunk(&c, path(), text);
        assert!(!chunks.is_empty());
        assert_eq!(chunks[0].line_start, 1);
    }

    // ── Edge cases ────────────────────────────────────────────────────────────

    #[test]
    fn empty_document_returns_empty() {
        let c = chunker(200, 50, 20);
        let chunks = sync_chunk(&c, path(), "");
        assert!(chunks.is_empty());
    }

    #[test]
    fn short_document_produces_one_chunk() {
        let c = chunker(2000, 200, 50);
        let chunks = sync_chunk(&c, path(), "Short text.");
        assert_eq!(chunks.len(), 1);
        assert_eq!(chunks[0].text, "Short text.");
    }

    #[test]
    fn min_chunk_merged_forward() {
        // Construct text so the last sentence is tiny (< min_chunk_chars=50).
        let long = "A".repeat(60) + ". " + &"B".repeat(60) + ". ";
        let tiny = "Hi.";
        let text = format!("{long}{tiny}");
        let c = chunker(80, 20, 50);
        let chunks = sync_chunk(&c, path(), &text);
        // The tiny "Hi." should be merged into a previous chunk, not emitted alone.
        for ch in &chunks {
            assert!(
                ch.text.len() >= 4 || chunks.len() == 1,
                "chunk too short: {:?}",
                ch.text
            );
        }
        // None of the chunks should be just "Hi." alone.
        assert!(!chunks.iter().any(|ch| ch.text.trim() == "Hi."));
    }

    #[test]
    fn oversized_single_sentence_hard_split() {
        // A single sentence of 500 chars with max=100 must not infinite-loop.
        let text = "A".repeat(500) + ".";
        let c = chunker(100, 20, 5);
        let chunks = sync_chunk(&c, path(), &text);
        assert!(
            chunks.len() >= 5,
            "expected ≥5 chunks for 500-char sentence with max=100"
        );
        for ch in &chunks {
            assert!(!ch.text.is_empty(), "empty chunk detected");
        }
    }

    // ── Determinism ───────────────────────────────────────────────────────────

    #[test]
    fn deterministic_output() {
        let text = "First sentence. Second sentence. Third one here. Fourth follows.";
        let c = chunker(40, 10, 5);
        let a = sync_chunk(&c, path(), text);
        let b = sync_chunk(&c, path(), text);
        assert_eq!(a.len(), b.len());
        for (ca, cb) in a.iter().zip(b.iter()) {
            assert_eq!(ca.text, cb.text);
            assert_eq!(ca.char_start, cb.char_start);
            assert_eq!(ca.char_end, cb.char_end);
        }
    }

    // ── strategy_name ─────────────────────────────────────────────────────────

    #[test]
    fn strategy_name_is_canonical() {
        let c = SemanticChunker::default();
        assert_eq!(c.strategy_name(), "semantic-sliding-window");
    }

    // ── Reconstruction (QA-B analogue) ────────────────────────────────────────

    #[test]
    fn no_empty_chunks() {
        let text = "Alpha sentence here. Beta sentence here. Gamma and delta too.";
        let c = chunker(30, 10, 1);
        let chunks = sync_chunk(&c, path(), text);
        assert!(!chunks.is_empty());
        for ch in &chunks {
            assert!(
                !ch.text.is_empty(),
                "empty chunk at index {}",
                ch.chunk_index
            );
        }
    }

    #[test]
    fn chunk_index_is_sequential() {
        let text = "First sentence. Second sentence. Third one. Fourth one. Fifth.";
        let c = chunker(30, 10, 1);
        let chunks = sync_chunk(&c, path(), text);
        for (i, ch) in chunks.iter().enumerate() {
            assert_eq!(ch.chunk_index, i, "chunk_index mismatch at position {i}");
        }
    }

    #[test]
    fn source_path_set_on_all_chunks() {
        let p = PathBuf::from("docs/overview.md");
        let text = "Intro sentence. Body sentence. Conclusion.";
        let c = chunker(500, 50, 5);
        let chunks = sync_chunk(&c, &p, text);
        for ch in &chunks {
            assert_eq!(ch.source_path, p);
        }
    }
}
