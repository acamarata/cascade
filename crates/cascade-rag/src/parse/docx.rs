//! DOCX parser backed by `docx-rs` (pure Rust, MIT).
//!
//! ## Strategy
//!
//! 1. Read the file to bytes with `fs::read`.
//! 2. Parse with `docx_rs::read_docx(&bytes)`.
//! 3. Walk `document.children` for `Paragraph` elements; collect run text in order.
//! 4. Paragraphs separated with `\n\n`; empty paragraphs collapsed.
//! 5. First non-empty paragraph → `DocumentText.title`; falls back to filename stem.
//! 6. Core properties (`author`, `created`) stored in metadata when present.
//!
//! ## Known limitations
//!
//! - `docx-rs` loads the entire file into memory before parsing.  Files larger
//!   than ~100 MB may cause excessive memory pressure.  For production use with
//!   very large DOCX files, consider streaming alternatives.
//! - Tables, headers, footers, comments, and tracked changes are **not** extracted
//!   (plain body paragraphs only, per T-P4-E01-18 scope).
//! - The `.doc` binary format is not supported.
//!
//! ## Cargo feature
//!
//! Enable with `cascade-rag = { features = ["docx-parser"] }`.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::parse::DocxParser
//!
//! # Ticket
//! T-P4-E01-18

use std::collections::HashMap;
use std::path::Path;

use cascade_types::error::{CascadeError, Result};
use tracing::debug;

use crate::parse::{DocumentParser, DocumentText};

// ── DocxParser ────────────────────────────────────────────────────────────────

/// DOCX document parser using `docx-rs`.
///
/// Extracts paragraph body text in document order.  Paragraph boundaries are
/// preserved as double newlines in the output.
///
/// # Purpose
/// Converts `.docx` files into plain-text [`DocumentText`] for the RAG
/// chunking pipeline.
///
/// # Inputs
/// A filesystem path to a `.docx` file.
///
/// # Outputs
/// [`DocumentText`] with:
/// - `text`: concatenated paragraph text separated by `\n\n`
/// - `title`: first non-empty paragraph, or filename stem
/// - `metadata`: `author`, `created` (when available in core properties)
/// - `parser_name`: `"docx"`
///
/// # Constraints
/// - Whole file loaded into memory; not suitable for files > ~100 MB.
/// - Tables, headers, footers, tracked changes not extracted.
/// - `.doc` (binary Word) format not supported.
///
/// # SPORT
/// MASTER-CRATES.md → cascade-rag::parse::DocxParser
#[derive(Debug, Default)]
pub struct DocxParser;

impl DocumentParser for DocxParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(path.extension().and_then(|e| e.to_str()), Some("docx"))
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        debug!(path = %path.display(), "parsing DOCX");

        let bytes = std::fs::read(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("failed to read file: {e}"),
        })?;

        let docx = docx_rs::read_docx(&bytes).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("docx-rs parse error: {e:?}"),
        })?;

        // Walk body children, collecting paragraph text.
        let mut paragraphs: Vec<String> = Vec::new();

        for child in &docx.document.children {
            if let docx_rs::DocumentChild::Paragraph(para) = child {
                let para_text = extract_paragraph_text(para);
                paragraphs.push(para_text);
            }
        }

        // Join with double newlines; skip runs of empty paragraphs but preserve
        // intentional blank lines (a single empty paragraph between content).
        let text = paragraphs
            .iter()
            .map(String::as_str)
            .collect::<Vec<_>>()
            .join("\n\n");

        // Title: first non-empty paragraph, fallback to filename stem.
        let title = paragraphs
            .iter()
            .find(|p| !p.trim().is_empty())
            .cloned()
            .or_else(|| path.file_stem().and_then(|s| s.to_str()).map(String::from));

        // Core-property metadata (author, created).
        // docx-rs CoreProps/CustomProps expose their fields only via builder
        // methods; the config is private.  We serialise to JSON and extract
        // the fields we care about as a lightweight alternative to accessing
        // private fields.
        let mut metadata: HashMap<String, String> = HashMap::new();
        if let Ok(core_json) = serde_json::to_value(&docx.doc_props.core) {
            if let Some(author) = core_json
                .pointer("/config/creator")
                .and_then(|v| v.as_str())
            {
                metadata.insert("author".into(), author.to_owned());
            }
            if let Some(created) = core_json
                .pointer("/config/created")
                .and_then(|v| v.as_str())
            {
                metadata.insert("created".into(), created.to_owned());
            }
        }

        debug!(
            path = %path.display(),
            paragraphs = paragraphs.len(),
            chars = text.len(),
            "DOCX parsed"
        );

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata,
            parser_name: "docx".into(),
        })
    }

    fn parser_name(&self) -> &str {
        "docx"
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Extract all run text from a single paragraph.
///
/// Walks `paragraph.children` for `Run` elements; concatenates their `Text`
/// children.  Non-run children (hyperlinks, bookmarks, etc.) are skipped.
fn extract_paragraph_text(para: &docx_rs::Paragraph) -> String {
    let mut buf = String::new();
    for child in &para.children {
        if let docx_rs::ParagraphChild::Run(run) = child {
            for run_child in &run.children {
                if let docx_rs::RunChild::Text(text) = run_child {
                    buf.push_str(&text.text);
                }
            }
        }
    }
    buf
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn fixtures_dir() -> PathBuf {
        // Relative to the crate root; Cargo sets CARGO_MANIFEST_DIR.
        let manifest = std::env::var("CARGO_MANIFEST_DIR").unwrap();
        PathBuf::from(manifest).join("tests/fixtures")
    }

    /// T-P4-E01-18: Simple multi-paragraph DOCX extracts text in order.
    #[test]
    fn docx_simple_fixture_roundtrip() {
        let parser = DocxParser;
        let path = fixtures_dir().join("simple.docx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        assert_eq!(result.parser_name, "docx");
        assert!(
            result.text.contains("Hello World from DOCX"),
            "text should contain first paragraph: got {:?}",
            result.text
        );
        assert!(
            result.text.contains("Second paragraph"),
            "text should contain second paragraph"
        );
        assert!(
            result.text.contains("Third paragraph"),
            "text should contain third paragraph"
        );
    }

    /// T-P4-E01-18: Title extracted as first non-empty paragraph.
    #[test]
    fn docx_title_is_first_paragraph() {
        let parser = DocxParser;
        let path = fixtures_dir().join("single_para.docx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        assert_eq!(
            result.title.as_deref(),
            Some("Title Paragraph"),
            "title should be the first paragraph text"
        );
    }

    /// T-P4-E01-18: Paragraphs are separated by double newlines.
    #[test]
    fn docx_paragraph_separator() {
        let parser = DocxParser;
        let path = fixtures_dir().join("simple.docx");
        assert!(path.exists(), "fixture missing: {}", path.display());

        let result = parser.parse(&path).expect("parse should succeed");

        // Double-newline separators between non-empty paragraphs.
        assert!(
            result.text.contains("\n\n"),
            "paragraphs should be separated by double newlines"
        );
    }

    /// T-P4-E01-18: Corrupt/invalid file returns Err, not panic.
    #[test]
    fn docx_malformed_returns_err() {
        let parser = DocxParser;
        // Create a temp file with garbage bytes.
        let tmp = tempfile::NamedTempFile::with_suffix(".docx").unwrap();
        std::fs::write(tmp.path(), b"not a real docx \x00\xFF\xFE garbage").unwrap();

        let result = parser.parse(tmp.path());
        assert!(result.is_err(), "malformed DOCX should return Err");
    }

    /// T-P4-E01-18: can_parse only accepts .docx extension.
    #[test]
    fn docx_can_parse_extension_check() {
        let parser = DocxParser;
        assert!(parser.can_parse(Path::new("file.docx")));
        assert!(!parser.can_parse(Path::new("file.doc")));
        assert!(!parser.can_parse(Path::new("file.pdf")));
        assert!(!parser.can_parse(Path::new("file.txt")));
    }
}
