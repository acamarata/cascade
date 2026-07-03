//! Tree-sitter implementation for code chunking (feature-gated).
//!
//! All items in this module are compiled only when the `code-chunker` feature
//! is enabled.

#[cfg(feature = "code-chunker")]
use tree_sitter::{Language, Parser};

#[cfg(feature = "code-chunker")]
use std::collections::HashMap;
#[cfg(feature = "code-chunker")]
use std::path::Path;

#[cfg(feature = "code-chunker")]
use super::super::{Chunk, ChunkerConfig};
#[cfg(feature = "code-chunker")]
use super::language::SourceLanguage;

// ── Language → tree-sitter grammar ───────────────────────────────────────────

#[cfg(feature = "code-chunker")]
pub(super) fn get_ts_language(lang: SourceLanguage) -> Language {
    match lang {
        SourceLanguage::Rust => tree_sitter_rust::language(),
        SourceLanguage::TypeScript => tree_sitter_typescript::language_typescript(),
        SourceLanguage::JavaScript => tree_sitter_javascript::language(),
        SourceLanguage::Python => tree_sitter_python::language(),
    }
}

/// Top-level node kinds that represent a "chunk boundary" per language.
#[cfg(feature = "code-chunker")]
pub(super) fn is_chunk_node(kind: &str, lang: SourceLanguage) -> bool {
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
pub(super) fn is_inner_chunk_node(kind: &str, lang: SourceLanguage) -> bool {
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
pub(super) fn symbol_name(node: tree_sitter::Node<'_>, src: &[u8]) -> Option<String> {
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
pub(super) fn kind_label(kind: &str, lang: SourceLanguage) -> &'static str {
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
pub(super) fn preceding_doc_comment(
    node: tree_sitter::Node<'_>,
    src: &[u8],
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
pub(super) fn ts_chunk(
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
pub(super) fn split_oversized(
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
pub(super) fn byte_offset_for_line(text: &str, line: usize) -> usize {
    text.lines()
        .take(line)
        .map(|l| l.len() + 1) // +1 for '\n'
        .sum()
}
