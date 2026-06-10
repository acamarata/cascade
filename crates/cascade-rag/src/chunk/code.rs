//! Code-aware chunker: function/class-level AST extraction via tree-sitter.
//!
//! Parses source files using tree-sitter grammars and extracts function/method/
//! class nodes as individual [`Chunk`]s.  Each chunk carries rich metadata:
//! symbol name, kind, language, doc-comment, file path, and 1-based start/end lines.
//!
//! ## Language support
//!
//! | Extension | Language |
//! |-----------|----------|
//! | `.rs`     | Rust (`function_item`, `impl_item`) |
//! | `.ts`, `.tsx` | TypeScript (`function_declaration`, `method_definition`, `class_declaration`) |
//! | `.js`, `.jsx` | JavaScript (`function_declaration`, `method_definition`, `class_declaration`) |
//! | `.py`     | Python (`function_definition`, `class_definition`) |
//!
//! ## Fallback
//!
//! Files with unsupported extensions fall back to [`SemanticChunker`] with a
//! `warn!` log.  Malformed or unparseable files also fall back — never panic.
//!
//! ## Chunking strategy
//!
//! 1. Detect language from file extension (or MIME type).
//! 2. Parse with tree-sitter into a full AST.
//! 3. Walk root children, collecting all imports/preamble nodes (before the
//!    first function/class) into a single "context" chunk.
//! 4. Each top-level function/method/class node becomes its own chunk.
//!    Doc-comments immediately preceding the node are prepended.
//! 5. If a node's text exceeds `max_chunk_chars`, recursively split by inner
//!    functions/methods.
//! 6. `heading_path` is set to the qualified symbol name
//!    (e.g. `"impl MyStruct > fn my_method"`, `"class Foo > def bar"`).
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk::CodeChunker

use tracing::warn;
#[cfg(feature = "code-chunker")]
use tree_sitter::{Language, Parser};

use std::path::Path;

use super::{Chunk, Chunker, ChunkerConfig};
use cascade_types::error::Result;

// ── async pipeline types (always compiled) ───────────────────────────────────

use async_trait::async_trait;
use cascade_types::{
    chunker::{Chunk as TypesChunk, ChunkOpts, Chunker as TypesChunker, Document},
    error::Result as TypesResult,
};

// ── Language detection ────────────────────────────────────────────────────────

/// Detected source language.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SourceLanguage {
    Rust,
    TypeScript,
    JavaScript,
    Python,
}

impl SourceLanguage {
    /// Human-readable name used in metadata.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Rust => "rust",
            Self::TypeScript => "typescript",
            Self::JavaScript => "javascript",
            Self::Python => "python",
        }
    }

    /// Detect from file extension.
    pub fn from_extension(ext: &str) -> Option<Self> {
        match ext.to_lowercase().as_str() {
            "rs" => Some(Self::Rust),
            "ts" | "tsx" => Some(Self::TypeScript),
            "js" | "jsx" => Some(Self::JavaScript),
            "py" => Some(Self::Python),
            _ => None,
        }
    }

    /// Detect from MIME type prefix like `text/x-rust`.
    pub fn from_mime(mime: &str) -> Option<Self> {
        let lang = mime.strip_prefix("text/x-")?;
        match lang {
            "rust" => Some(Self::Rust),
            "typescript" | "tsx" => Some(Self::TypeScript),
            "javascript" | "jsx" => Some(Self::JavaScript),
            "python" => Some(Self::Python),
            _ => None,
        }
    }
}

/// Detect language from a document's source_path extension, falling back to MIME.
pub fn detect_language_for(path: Option<&Path>, mime: Option<&str>) -> Option<SourceLanguage> {
    if let Some(p) = path {
        if let Some(ext) = p.extension().and_then(|e| e.to_str()) {
            if let Some(lang) = SourceLanguage::from_extension(ext) {
                return Some(lang);
            }
        }
    }
    mime.and_then(SourceLanguage::from_mime)
}

// ── CodeChunker (sync local RAG pipeline) ────────────────────────────────────

/// Function-level code chunker backed by tree-sitter AST analysis.
///
/// Implements the sync [`Chunker`] trait for the local RAG pipeline.
/// Falls back to [`super::semantic::SemanticChunker`] for unsupported extensions.
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
    config: ChunkerConfig,
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
        let semantic = super::semantic::SemanticChunker::with_config(self.config.clone());
        Chunker::chunk(&semantic, source_path, text)
    }

    fn strategy_name(&self) -> &str {
        "code"
    }
}

// ── tree-sitter implementation (feature-gated) ────────────────────────────────

#[cfg(feature = "code-chunker")]
fn get_ts_language(lang: SourceLanguage) -> Language {
    match lang {
        SourceLanguage::Rust => tree_sitter_rust::language(),
        SourceLanguage::TypeScript => tree_sitter_typescript::language_typescript(),
        SourceLanguage::JavaScript => tree_sitter_javascript::language(),
        SourceLanguage::Python => tree_sitter_python::language(),
    }
}

/// Top-level node kinds that represent a "chunk boundary" per language.
#[cfg(feature = "code-chunker")]
fn is_chunk_node(kind: &str, lang: SourceLanguage) -> bool {
    match lang {
        SourceLanguage::Rust => matches!(kind, "function_item" | "impl_item"),
        SourceLanguage::TypeScript | SourceLanguage::JavaScript => matches!(
            kind,
            "function_declaration"
                | "method_definition"
                | "class_declaration"
                | "lexical_declaration"  // const Foo = () => ...
                | "export_statement"
        ),
        SourceLanguage::Python => matches!(kind, "function_definition" | "class_definition"),
    }
}

/// Return `true` for node kinds that contribute inner function/method nodes
/// (used when splitting oversized chunks).
#[cfg(feature = "code-chunker")]
fn is_inner_chunk_node(kind: &str, lang: SourceLanguage) -> bool {
    match lang {
        SourceLanguage::Rust => matches!(kind, "function_item"),
        SourceLanguage::TypeScript | SourceLanguage::JavaScript => {
            matches!(kind, "function_declaration" | "method_definition")
        }
        SourceLanguage::Python => matches!(kind, "function_definition"),
    }
}

/// Extract the primary symbol name from a node.
///
/// Returns `None` if the node has no identifiable name.
#[cfg(feature = "code-chunker")]
fn symbol_name<'a>(node: tree_sitter::Node<'_>, src: &'a [u8]) -> Option<String> {
    // For most nodes, the name is in the "name" field.
    if let Some(name_node) = node.child_by_field_name("name") {
        return name_node.utf8_text(src).ok().map(|s| s.to_owned());
    }
    // For Rust impl blocks, extract the type name.
    if node.kind() == "impl_item" {
        if let Some(type_node) = node.child_by_field_name("type") {
            return type_node.utf8_text(src).ok().map(|s| s.to_owned());
        }
    }
    None
}

/// Kind label for `heading_path` construction.
///
/// Returns a `'static` str — unknown node kinds fall back to a generic label
/// rather than returning the (non-static) input `kind`.
#[cfg(feature = "code-chunker")]
fn kind_label(kind: &str, lang: SourceLanguage) -> &'static str {
    match lang {
        SourceLanguage::Rust => match kind {
            "function_item" => "fn",
            "impl_item" => "impl",
            _ => "item",
        },
        SourceLanguage::TypeScript | SourceLanguage::JavaScript => match kind {
            "function_declaration" | "lexical_declaration" => "function",
            "method_definition" => "method",
            "class_declaration" => "class",
            "export_statement" => "export",
            _ => "item",
        },
        SourceLanguage::Python => match kind {
            "function_definition" => "def",
            "class_definition" => "class",
            _ => "item",
        },
    }
}

/// Extract the doc-comment immediately preceding `node` in the source.
///
/// For Rust: `line_comment` / `block_comment` chains ending just before node.
/// For Python: `expression_statement` containing a `string` (docstring).
/// For TS/JS: `comment` nodes.
#[cfg(feature = "code-chunker")]
fn preceding_doc_comment<'a>(
    node: tree_sitter::Node<'_>,
    src: &'a [u8],
    lang: SourceLanguage,
) -> Option<String> {
    let comment_kinds: &[&str] = match lang {
        SourceLanguage::Rust => &["line_comment", "block_comment"],
        SourceLanguage::TypeScript | SourceLanguage::JavaScript => &["comment"],
        SourceLanguage::Python => &["expression_statement"],
    };

    let mut sib = node.prev_named_sibling()?;
    let mut lines: Vec<String> = Vec::new();
    loop {
        let kind = sib.kind();
        if comment_kinds.contains(&kind) {
            let text = sib.utf8_text(src).ok()?;
            // For Python, only count expression_statement containing a string literal.
            if lang == SourceLanguage::Python {
                if !text.trim_start().starts_with('"')
                    && !text.trim_start().starts_with('\'')
                    && !text.trim_start().starts_with("r\"")
                    && !text.trim_start().starts_with("r'")
                {
                    break;
                }
            }
            lines.push(text.to_owned());
            match sib.prev_named_sibling() {
                Some(prev) => sib = prev,
                None => break,
            }
        } else {
            break;
        }
    }
    if lines.is_empty() {
        None
    } else {
        lines.reverse();
        Some(lines.join("\n"))
    }
}

/// Core tree-sitter chunking logic.
///
/// Returns `Err` only on parse failures; callers fall back to SemanticChunker.
#[cfg(feature = "code-chunker")]
fn ts_chunk(
    source_path: &Path,
    text: &str,
    lang: SourceLanguage,
    config: &ChunkerConfig,
) -> std::result::Result<Vec<Chunk>, Box<dyn std::error::Error + Send + Sync>> {
    let mut parser = Parser::new();
    parser.set_language(&get_ts_language(lang))?;

    let src_bytes = text.as_bytes();
    let tree = parser
        .parse(text, None)
        .ok_or("tree-sitter parse returned None")?;

    let root = tree.root_node();
    let mut chunks: Vec<Chunk> = Vec::new();
    let mut chunk_index = 0usize;

    // Accumulate preamble text (imports, top-level statements before first chunk node).
    let mut preamble_lines: Vec<(usize, &str)> = Vec::new(); // (line_number_0based, line)
    let text_lines: Vec<&str> = text.lines().collect();

    // Walk direct root children.
    let mut cursor = root.walk();
    let children: Vec<_> = root.named_children(&mut cursor).collect();

    for node in &children {
        if is_chunk_node(node.kind(), lang) {
            // Flush any accumulated preamble.
            if !preamble_lines.is_empty() {
                let first_line = preamble_lines[0].0;
                let last_line = preamble_lines.last().unwrap().0;
                let preamble_text = preamble_lines
                    .iter()
                    .map(|(_, l)| *l)
                    .collect::<Vec<_>>()
                    .join("\n");
                if preamble_text.len() >= config.min_chunk_chars {
                    let mut meta = HashMap::new();
                    meta.insert("language".to_owned(), lang.as_str().to_owned());
                    meta.insert("kind".to_owned(), "preamble".to_owned());
                    let byte_start = byte_offset_for_line(text, first_line);
                    let byte_end = byte_offset_for_line(text, last_line + 1);
                    chunks.push(Chunk {
                        chunk_id: None,
                        source_path: source_path.to_path_buf(),
                        chunk_index,
                        text: preamble_text,
                        char_start: byte_start,
                        char_end: byte_end,
                        line_start: first_line + 1,
                        line_end: last_line + 1,
                        parent_chunk_id: None,
                        heading_path: Some(format!(
                            "{} > preamble",
                            source_path
                                .file_name()
                                .and_then(|n| n.to_str())
                                .unwrap_or("file")
                        )),
                        metadata: meta,
                    });
                    chunk_index += 1;
                }
                preamble_lines.clear();
            }

            // Extract doc comment.
            let doc = preceding_doc_comment(*node, src_bytes, lang);

            // Build node text (prepend doc if any).
            let node_text = node.utf8_text(src_bytes).unwrap_or("").to_owned();
            let full_text = match &doc {
                Some(d) => format!("{d}\n{node_text}"),
                None => node_text.clone(),
            };

            let name = symbol_name(*node, src_bytes);
            let label = kind_label(node.kind(), lang);
            let heading = name
                .as_deref()
                .map(|n| format!("{label} {n}"))
                .unwrap_or_else(|| label.to_owned());

            let start_line = node.start_position().row; // 0-based
            let end_line = node.end_position().row; // 0-based

            // If oversized, try to split by inner functions.
            if full_text.len() > config.max_chunk_chars {
                let sub = split_oversized(
                    node,
                    src_bytes,
                    source_path,
                    text,
                    lang,
                    config,
                    &heading,
                    chunk_index,
                );
                chunk_index += sub.len();
                chunks.extend(sub);
            } else {
                let mut meta = HashMap::new();
                meta.insert("language".to_owned(), lang.as_str().to_owned());
                meta.insert("kind".to_owned(), label.to_owned());
                if let Some(n) = &name {
                    meta.insert("symbol_name".to_owned(), n.clone());
                }

                let byte_start = node.start_byte();
                let byte_end = node.end_byte();

                chunks.push(Chunk {
                    chunk_id: None,
                    source_path: source_path.to_path_buf(),
                    chunk_index,
                    text: full_text,
                    char_start: byte_start,
                    char_end: byte_end,
                    line_start: start_line + 1,
                    line_end: end_line + 1,
                    parent_chunk_id: None,
                    heading_path: Some(heading),
                    metadata: meta,
                });
                chunk_index += 1;
            }
        } else {
            // Accumulate to preamble.
            let start = node.start_position().row;
            let end = node.end_position().row;
            for line_idx in start..=end {
                if line_idx < text_lines.len() {
                    preamble_lines.push((line_idx, text_lines[line_idx]));
                }
            }
        }
    }

    // Flush remaining preamble.
    if !preamble_lines.is_empty() {
        let first_line = preamble_lines[0].0;
        let last_line = preamble_lines.last().unwrap().0;
        let preamble_text = preamble_lines
            .iter()
            .map(|(_, l)| *l)
            .collect::<Vec<_>>()
            .join("\n");
        if preamble_text.len() >= config.min_chunk_chars {
            let mut meta = HashMap::new();
            meta.insert("language".to_owned(), lang.as_str().to_owned());
            meta.insert("kind".to_owned(), "preamble".to_owned());
            let byte_start = byte_offset_for_line(text, first_line);
            let byte_end = byte_offset_for_line(text, last_line + 1);
            chunks.push(Chunk {
                chunk_id: None,
                source_path: source_path.to_path_buf(),
                chunk_index,
                text: preamble_text,
                char_start: byte_start,
                char_end: byte_end,
                line_start: first_line + 1,
                line_end: last_line + 1,
                parent_chunk_id: None,
                heading_path: Some(format!(
                    "{} > preamble",
                    source_path
                        .file_name()
                        .and_then(|n| n.to_str())
                        .unwrap_or("file")
                )),
                metadata: meta,
            });
        }
    }

    Ok(chunks)
}

/// Recursively split an oversized node by its inner function/method children.
#[cfg(feature = "code-chunker")]
fn split_oversized(
    node: &tree_sitter::Node<'_>,
    src: &[u8],
    source_path: &Path,
    full_text: &str,
    lang: SourceLanguage,
    config: &ChunkerConfig,
    parent_heading: &str,
    base_index: usize,
) -> Vec<Chunk> {
    let mut result = Vec::new();
    let mut idx = base_index;

    let mut cursor = node.walk();
    let children: Vec<_> = node.named_children(&mut cursor).collect();
    let mut found_inner = false;

    for child in &children {
        if is_inner_chunk_node(child.kind(), lang) {
            found_inner = true;
            let name = symbol_name(*child, src);
            let label = kind_label(child.kind(), lang);
            let heading = name
                .as_deref()
                .map(|n| format!("{parent_heading} > {label} {n}"))
                .unwrap_or_else(|| format!("{parent_heading} > {label}"));

            let node_text = child.utf8_text(src).unwrap_or("").to_owned();
            if node_text.len() > config.max_chunk_chars {
                // Recurse deeper.
                let sub = split_oversized(
                    child,
                    src,
                    source_path,
                    full_text,
                    lang,
                    config,
                    &heading,
                    idx,
                );
                idx += sub.len();
                result.extend(sub);
            } else {
                let mut meta = HashMap::new();
                meta.insert("language".to_owned(), lang.as_str().to_owned());
                meta.insert("kind".to_owned(), label.to_owned());
                if let Some(n) = &name {
                    meta.insert("symbol_name".to_owned(), n.clone());
                }
                let byte_start = child.start_byte();
                let byte_end = child.end_byte();
                let start_line = child.start_position().row;
                let end_line = child.end_position().row;

                result.push(Chunk {
                    chunk_id: None,
                    source_path: source_path.to_path_buf(),
                    chunk_index: idx,
                    text: node_text,
                    char_start: byte_start,
                    char_end: byte_end,
                    line_start: start_line + 1,
                    line_end: end_line + 1,
                    parent_chunk_id: None,
                    heading_path: Some(heading),
                    metadata: meta,
                });
                idx += 1;
            }
        }
    }

    // If no inner functions found, emit the whole node as one chunk (best effort).
    if !found_inner {
        let node_text = node.utf8_text(src).unwrap_or("").to_owned();
        let mut meta = HashMap::new();
        meta.insert("language".to_owned(), lang.as_str().to_owned());
        meta.insert("kind".to_owned(), kind_label(node.kind(), lang).to_owned());
        let start_line = node.start_position().row;
        let end_line = node.end_position().row;
        result.push(Chunk {
            chunk_id: None,
            source_path: source_path.to_path_buf(),
            chunk_index: base_index,
            text: node_text,
            char_start: node.start_byte(),
            char_end: node.end_byte(),
            line_start: start_line + 1,
            line_end: end_line + 1,
            parent_chunk_id: None,
            heading_path: Some(parent_heading.to_owned()),
            metadata: meta,
        });
    }

    result
}

/// Convert a 0-based line number to a byte offset in `text`.
#[cfg(feature = "code-chunker")]
fn byte_offset_for_line(text: &str, line: usize) -> usize {
    text.lines()
        .take(line)
        .map(|l| l.len() + 1) // +1 for '\n'
        .sum()
}

// ── TypesChunker impl on CodeChunker ─────────────────────────────────────────
//
// `StrategyChunker` in chunk/mod.rs calls `code::CodeChunker` as a
// `cascade_types::Chunker` (async).  We satisfy that here by delegating to
// the shared helper used by `AsyncCodeChunker`.

#[async_trait]
impl TypesChunker for CodeChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> TypesResult<Vec<TypesChunk>> {
        async_chunk_impl(doc, opts).await
    }

    fn name(&self) -> &str {
        "code"
    }
}

// ── async pipeline shared implementation ─────────────────────────────────────

async fn async_chunk_impl(doc: &Document, opts: &ChunkOpts) -> TypesResult<Vec<TypesChunk>> {
    use cascade_types::chunker::ChunkMetadata;

    let path = doc.source_path.clone();
    let lang = detect_language_for(path.as_deref(), doc.mime_type.as_deref());

    if lang.is_none() {
        warn!(
            path = ?path,
            mime = ?doc.mime_type,
            "CodeChunker (async): unsupported language; falling back to semantic"
        );
        return TypesChunker::chunk(&super::semantic::SemanticChunker::default(), doc, opts).await;
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
                                e.insert("heading_path".to_owned(), serde_json::Value::String(hp));
                            }
                            e
                        },
                    },
                })
                .collect())
        }
        Err(e) => {
            warn!(error = %e, "CodeChunker (async) fallback; delegating to semantic");
            TypesChunker::chunk(&super::semantic::SemanticChunker::default(), doc, opts).await
        }
    }
}

// ── async pipeline (cascade-types Chunker impl) ───────────────────────────────

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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(all(test, feature = "code-chunker"))]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn chunker() -> CodeChunker {
        CodeChunker::new(ChunkerConfig {
            max_chunk_chars: 4000,
            min_chunk_chars: 10,
            overlap_chars: 0,
        })
    }

    // ── Rust ──────────────────────────────────────────────────────────────────

    #[test]
    fn rust_three_functions_plus_preamble() {
        let src = r#"use std::fmt;

/// Adds two numbers.
fn add(a: i32, b: i32) -> i32 { a + b }

fn sub(a: i32, b: i32) -> i32 { a - b }

fn mul(a: i32, b: i32) -> i32 { a * b }
"#;
        let path = PathBuf::from("src/math.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");

        let fn_chunks: Vec<_> = chunks
            .iter()
            .filter(|c| c.metadata.get("kind").map(|s| s.as_str()) == Some("fn"))
            .collect();
        assert_eq!(
            fn_chunks.len(),
            3,
            "expected 3 fn chunks, got {}",
            fn_chunks.len()
        );

        // Heading path contains function name.
        assert!(
            fn_chunks[0]
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("add"),
            "heading_path missing 'add': {:?}",
            fn_chunks[0].heading_path
        );
        assert!(
            fn_chunks[1]
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("sub"),
            "heading_path missing 'sub': {:?}",
            fn_chunks[1].heading_path
        );
        assert!(
            fn_chunks[2]
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("mul"),
            "heading_path missing 'mul': {:?}",
            fn_chunks[2].heading_path
        );
    }

    #[test]
    fn rust_line_numbers_match() {
        let src = "use std::fmt;\n\nfn foo() {\n    // body\n}\n";
        let path = PathBuf::from("src/lib.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        let foo = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("foo"))
            .expect("fn foo chunk");
        // "fn foo()" is on line 3 (1-based).
        assert_eq!(
            foo.line_start, 3,
            "expected line_start=3, got {}",
            foo.line_start
        );
    }

    #[test]
    fn rust_doc_comment_attached() {
        let src = "/// Adds two numbers.\nfn add(a: i32, b: i32) -> i32 { a + b }\n";
        let path = PathBuf::from("src/lib.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        let add = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("add"))
            .expect("fn add chunk");
        assert!(
            add.text.contains("Adds two numbers"),
            "doc comment not attached: {:?}",
            add.text
        );
    }

    #[test]
    fn rust_impl_block() {
        let src = r#"struct Foo;

impl Foo {
    fn bar(&self) -> i32 { 42 }
    fn baz(&self) -> i32 { 0 }
}
"#;
        let path = PathBuf::from("src/foo.rs");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        // impl Foo should be one chunk (not oversized).
        let impl_chunk = chunks
            .iter()
            .find(|c| {
                c.metadata.get("kind").map(|s| s.as_str()) == Some("impl")
                    || c.heading_path.as_deref().unwrap_or("").starts_with("impl")
            })
            .expect("impl chunk");
        assert!(
            impl_chunk
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("Foo"),
            "impl heading_path missing 'Foo': {:?}",
            impl_chunk.heading_path
        );
    }

    // ── TypeScript ────────────────────────────────────────────────────────────

    #[test]
    fn typescript_class_and_function() {
        let src = r#"import { useState } from 'react';

class Greeter {
    name: string;
    greet() { return `Hello ${this.name}`; }
}

function helper(x: number): number {
    return x * 2;
}
"#;
        let path = PathBuf::from("src/app.ts");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");

        // Should have a class chunk and a function chunk.
        let class_chunk = chunks
            .iter()
            .find(|c| {
                c.metadata.get("kind").map(|s| s.as_str()) == Some("class")
                    || c.heading_path.as_deref().unwrap_or("").contains("class")
            })
            .expect("class chunk");
        assert!(
            class_chunk
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("Greeter"),
            "class chunk missing 'Greeter': {:?}",
            class_chunk.heading_path
        );

        let fn_chunk = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("helper"))
            .expect("function helper chunk");
        assert!(fn_chunk.line_start > 1);
    }

    // ── Python ────────────────────────────────────────────────────────────────

    #[test]
    fn python_class_with_methods() {
        let src = r#"import os

class Dog:
    def __init__(self, name):
        self.name = name

    def bark(self):
        return f"Woof! I am {self.name}"

def standalone(x):
    return x + 1
"#;
        let path = PathBuf::from("pets.py");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");

        let class_chunk = chunks
            .iter()
            .find(|c| c.heading_path.as_deref().unwrap_or("").contains("Dog"))
            .expect("class Dog chunk");
        assert!(
            class_chunk
                .heading_path
                .as_deref()
                .unwrap_or("")
                .contains("Dog"),
            "missing 'Dog' in heading_path: {:?}",
            class_chunk.heading_path
        );

        let standalone = chunks
            .iter()
            .find(|c| {
                c.heading_path
                    .as_deref()
                    .unwrap_or("")
                    .contains("standalone")
            })
            .expect("standalone fn chunk");
        assert!(standalone.line_start > 1);
    }

    // ── JavaScript ────────────────────────────────────────────────────────────

    #[test]
    fn javascript_function_chunks() {
        let src = r#"const PI = 3.14;

function square(x) {
    return x * x;
}

function cube(x) {
    return x * x * x;
}
"#;
        let path = PathBuf::from("math.js");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("chunks");
        let fn_chunks: Vec<_> = chunks
            .iter()
            .filter(|c| {
                c.heading_path.as_deref().unwrap_or("").contains("square")
                    || c.heading_path.as_deref().unwrap_or("").contains("cube")
            })
            .collect();
        assert_eq!(
            fn_chunks.len(),
            2,
            "expected 2 js fn chunks, got {fn_chunks:?}"
        );
    }

    // ── Oversize split ────────────────────────────────────────────────────────

    #[test]
    fn oversized_impl_splits_inner_fns() {
        // Build a large impl block that exceeds max_chunk_chars.
        let mut src = String::from("struct Big;\n\nimpl Big {\n");
        for i in 0..30 {
            src.push_str(&format!("    fn method_{i}(&self) -> i32 {{\n        // lots of padding padding padding padding padding padding\n        {i}\n    }}\n\n"));
        }
        src.push_str("}\n");

        let config = ChunkerConfig {
            max_chunk_chars: 400, // small enough to force split
            min_chunk_chars: 10,
            overlap_chars: 0,
        };
        let chunker = CodeChunker::new(config);
        let path = PathBuf::from("src/big.rs");
        let chunks = Chunker::chunk(&chunker, &path, &src).expect("chunks");

        // Should have multiple chunks instead of one giant impl.
        assert!(
            chunks.len() > 1,
            "expected split chunks, got {} chunks",
            chunks.len()
        );
        // Every fn chunk should have heading_path.
        for c in &chunks {
            assert!(c.heading_path.is_some(), "chunk missing heading_path");
        }
    }

    // ── Unknown extension fallback ────────────────────────────────────────────

    #[test]
    fn unknown_extension_falls_back_to_semantic() {
        let src = "This is some plain text content. It is not code. It has multiple sentences.";
        let path = PathBuf::from("file.xyz");
        let chunks = Chunker::chunk(&chunker(), &path, src).expect("fallback should not error");
        // SemanticChunker should produce at least one chunk for non-empty input.
        assert!(!chunks.is_empty(), "fallback produced no chunks");
    }

    #[test]
    fn unsupported_mime_falls_back() {
        // .yaml is not supported.
        let src = "key: value\nfoo: bar\n";
        let path = PathBuf::from("config.yaml");
        let result = Chunker::chunk(&chunker(), &path, src);
        assert!(result.is_ok(), "fallback should not return Err");
    }
}
