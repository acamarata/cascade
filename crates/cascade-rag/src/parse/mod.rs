//! Document parser implementations.
//!
//! All implementations satisfy the [`cascade_types::parser::Parser`] trait.
//! A `Parser` reads a file from disk and produces a [`cascade_types::chunker::Document`]
//! ready for the chunking pipeline.
//!
//! ## Parser registry
//!
//! [`ParserRegistry`] maps MIME types and file extensions to the correct parser.
//! The registry is constructed once at daemon start and shared via `Arc`.
//!
//! ```rust,no_run
//! # use cascade_rag::parse::ParserRegistry;
//! # use cascade_types::parser::ParserKind;
//! # use std::path::Path;
//! # async fn example() -> cascade_types::error::Result<()> {
//! let registry = ParserRegistry::default();
//! let doc = registry.parse(Path::new("README.md")).await?;
//! # Ok(())
//! # }
//! ```
//!
//! ## Format support
//!
//! | Format | Parser | Sprint |
//! |--------|--------|--------|
//! | Markdown | [`markdown::MarkdownParser`] | S02-02 |
//! | Source code | [`code::CodeParser`] | S02 |
//! | PDF | [`pdf::PdfParser`] | S03-01 |
//! | Plain text | `cascade_types::PlainTextParser` | S02-03 |
//! | DOCX | [`docx::DocxParser`] | S06 (T-P4-E01-18) |
//! | XLSX/XLS | [`xlsx::XlsxParser`] | S06 (T-P4-E01-19) |
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::parse

pub mod code;
#[cfg(feature = "docx-parser")]
pub mod docx;
#[cfg(feature = "html-parser")]
pub mod html;
pub mod markdown;
pub mod pdf;
pub mod structured;
#[cfg(feature = "xlsx-parser")]
pub mod xlsx;

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use cascade_types::{
    chunker::Document,
    error::{CascadeError, Result},
    parser::{Parser, ParserKind, PlainTextParser},
};
use serde::{Deserialize, Serialize};

// ── DocumentText ──────────────────────────────────────────────────────────────

/// Output of the [`DocumentParser`] pipeline: extracted plain text with provenance.
///
/// Unlike the lower-level [`Document`] type (which feeds the chunker), `DocumentText`
/// is the canonical human-readable representation of a file after parsing.  The
/// `metadata` map carries format-specific key-value pairs (e.g. `author`, `page_count`,
/// `sheet_names` for XLSX) that callers can use for display or filtering.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::DocumentText
///
/// # Ticket
/// T-P4-E01-13
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DocumentText {
    /// Absolute path to the source file that was parsed.
    pub source_path: PathBuf,

    /// Extracted plain text (UTF-8; lossy conversion applied when the file
    /// contains invalid UTF-8 sequences).
    pub text: String,

    /// Inferred title: first H1 heading for Markdown, filename stem for code,
    /// document title metadata for PDF/DOCX, etc.
    pub title: Option<String>,

    /// Format-specific metadata (e.g. `"author"`, `"page_count"`, `"language"`).
    pub metadata: HashMap<String, String>,

    /// Name of the parser that produced this result (e.g. `"markdown"`, `"code"`).
    pub parser_name: String,
}

// ── DocumentParser trait ──────────────────────────────────────────────────────

/// Synchronous, object-safe parser interface used by [`ParseDispatcher`].
///
/// Implementors **must** be `Send + Sync` so they can be stored in an `Arc`
/// and shared across threads.
///
/// # Object safety
/// This trait is intentionally object-safe: all methods take `&self` or
/// `&Path`, and there are no generic type parameters.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::DocumentParser
///
/// # Ticket
/// T-P4-E01-13
pub trait DocumentParser: Send + Sync {
    /// Returns `true` if this parser can handle the file at `path` (usually
    /// decided by extension).
    fn can_parse(&self, path: &Path) -> bool;

    /// Parse the file at `path` into a [`DocumentText`].
    ///
    /// # Errors
    /// Returns [`CascadeError`] when the file cannot be read or decoded.
    fn parse(&self, path: &Path) -> Result<DocumentText>;

    /// Canonical name used in [`DocumentText::parser_name`].
    fn parser_name(&self) -> &str;
}

// ── ParseDispatcher ───────────────────────────────────────────────────────────

/// Dispatches a file to the first registered [`DocumentParser`] that claims it,
/// falling back to a raw UTF-8 (lossy) read for unrecognised file types.
///
/// # Construction
///
/// ```rust
/// use cascade_rag::parse::{ParseDispatcher, DocumentText};
///
/// let dispatcher = ParseDispatcher::default();
/// ```
///
/// # Thread safety
/// `ParseDispatcher` is `Send + Sync`; the inner `Vec` is constructed once and
/// never mutated after creation.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::ParseDispatcher
///
/// # Ticket
/// T-P4-E01-13
pub struct ParseDispatcher {
    parsers: Vec<Box<dyn DocumentParser>>,
}

impl Default for ParseDispatcher {
    fn default() -> Self {
        #[allow(unused_mut)]
        let mut parsers: Vec<Box<dyn DocumentParser>> = vec![
            Box::new(markdown::MarkdownDocParser),
            Box::new(code::CodeDocParser),
            Box::new(structured::YamlParser),
            Box::new(structured::JsonParser),
            Box::new(structured::TomlParser),
        ];
        #[cfg(feature = "html-parser")]
        parsers.push(Box::new(html::HtmlParser));
        // T-P4-E01-17: pure-Rust PDF text-layer extraction via pdf-extract.
        #[cfg(feature = "pdf-parser")]
        parsers.push(Box::new(pdf::PdfDocParser));
        // T-P4-E01-18: DOCX paragraph text extraction via docx-rs.
        #[cfg(feature = "docx-parser")]
        parsers.push(Box::new(docx::DocxParser));
        // T-P4-E01-19: XLSX/XLS spreadsheet text extraction via calamine.
        #[cfg(feature = "xlsx-parser")]
        parsers.push(Box::new(xlsx::XlsxParser));
        Self { parsers }
    }
}

impl ParseDispatcher {
    /// Construct an empty dispatcher (no registered parsers).
    pub fn empty() -> Self {
        Self { parsers: vec![] }
    }

    /// Register an additional [`DocumentParser`].  Parsers are tried in
    /// registration order; the first match wins.
    pub fn register(&mut self, parser: Box<dyn DocumentParser>) {
        self.parsers.push(parser);
    }

    /// Dispatch `path` to the appropriate parser.
    ///
    /// Returns the first match.  If no registered parser claims the file, a
    /// raw UTF-8 (lossy) read is attempted as a fallback.
    ///
    /// # Errors
    /// Returns [`CascadeError::ParseFailed`] when the fallback read fails.
    pub fn dispatch(&self, path: &Path) -> Result<DocumentText> {
        for parser in &self.parsers {
            if parser.can_parse(path) {
                return parser.parse(path);
            }
        }
        // Fallback: raw UTF-8 lossy read
        let bytes = std::fs::read(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("fallback read failed: {e}"),
        })?;
        let text = String::from_utf8_lossy(&bytes).into_owned();
        let title = path
            .file_stem()
            .and_then(|s| s.to_str())
            .map(String::from);
        tracing::warn!(
            path = %path.display(),
            "no DocumentParser matched; falling back to raw UTF-8 lossy read"
        );
        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata: HashMap::new(),
            parser_name: "raw".into(),
        })
    }
}

// ── ParserRegistry ────────────────────────────────────────────────────────────

/// Maps file extensions and MIME types to parser implementations.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::ParserRegistry
pub struct ParserRegistry {
    parsers: Vec<Arc<dyn Parser>>,
}

impl Default for ParserRegistry {
    fn default() -> Self {
        let parsers: Vec<Arc<dyn Parser>> = vec![
            Arc::new(markdown::MarkdownParser),
            Arc::new(code::CodeParser),
            Arc::new(pdf::PdfParser),
            Arc::new(PlainTextParser),
        ];
        Self { parsers }
    }
}

impl ParserRegistry {
    /// Parse a file by auto-selecting the appropriate parser from the extension.
    ///
    /// Falls back to [`PlainTextParser`] for unrecognised extensions.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::ParserFailed`] if the selected parser returns an
    /// error.  Binary files that cannot be decoded as UTF-8 are skipped with a
    /// warning rather than propagating an error.
    pub async fn parse(&self, path: &Path) -> Result<Document> {
        let kind = ParserKind::from_path(path).unwrap_or(ParserKind::PlainText);
        let parser = self
            .parsers
            .iter()
            .find(|p| p.kind() == kind)
            .or_else(|| {
                self.parsers
                    .iter()
                    .find(|p| p.kind() == ParserKind::PlainText)
            })
            .ok_or_else(|| CascadeError::ParseFailed {
                path: path.to_path_buf(),
                detail: "no parser registered for this file type".into(),
            })?;
        parser.parse(path).await
    }

    /// Add a custom parser.  Custom parsers take precedence over built-in ones.
    pub fn register(&mut self, parser: Arc<dyn Parser>) {
        self.parsers.insert(0, parser);
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::NamedTempFile;

    /// T-P4-E01-13: DocumentText construction and JSON serialisation round-trip.
    #[test]
    fn document_text_json_roundtrip() {
        let mut metadata = HashMap::new();
        metadata.insert("language".to_string(), "rust".to_string());

        let dt = DocumentText {
            source_path: PathBuf::from("/tmp/hello.rs"),
            text: "fn main() {}".into(),
            title: Some("hello".into()),
            metadata,
            parser_name: "code".into(),
        };

        let json = serde_json::to_string(&dt).expect("serialize");
        let back: DocumentText = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(back.text, dt.text);
        assert_eq!(back.parser_name, "code");
        assert_eq!(back.title.as_deref(), Some("hello"));
        assert_eq!(back.metadata.get("language").map(String::as_str), Some("rust"));
    }

    /// T-P4-E01-13: ParseDispatcher falls back to raw read for unrecognised extensions.
    #[test]
    fn dispatcher_fallback_unknown_extension() {
        let mut f = NamedTempFile::with_suffix(".xyz").unwrap();
        write!(f, "hello world").unwrap();
        let dispatcher = ParseDispatcher::default();
        let result = dispatcher.dispatch(f.path()).expect("fallback should succeed");
        assert_eq!(result.parser_name, "raw");
        assert!(result.text.contains("hello world"));
    }

    /// T-P4-E01-13: ParseDispatcher trait dispatch routes .md to markdown parser.
    #[test]
    fn dispatcher_routes_markdown() {
        let mut f = NamedTempFile::with_suffix(".md").unwrap();
        write!(f, "# Hello\nsome text").unwrap();
        let dispatcher = ParseDispatcher::default();
        let result = dispatcher.dispatch(f.path()).expect("markdown parse");
        assert_eq!(result.parser_name, "markdown");
        assert_eq!(result.title.as_deref(), Some("Hello"));
    }

    /// T-P4-E01-13: ParseDispatcher trait dispatch routes .rs to code parser.
    #[test]
    fn dispatcher_routes_code() {
        let mut f = NamedTempFile::with_suffix(".rs").unwrap();
        write!(f, "fn main() {{}}").unwrap();
        let dispatcher = ParseDispatcher::default();
        let result = dispatcher.dispatch(f.path()).expect("code parse");
        assert_eq!(result.parser_name, "code");
        assert_eq!(result.metadata.get("language").map(String::as_str), Some("rust"));
    }
}
