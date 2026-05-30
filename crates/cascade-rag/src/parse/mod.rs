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
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::parse

pub mod code;
pub mod markdown;
pub mod pdf;

use std::path::Path;
use std::sync::Arc;
use async_trait::async_trait;

use cascade_types::{
    chunker::Document,
    error::{CascadeError, Result},
    parser::{Parser, ParserKind, PlainTextParser},
};

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
            .or_else(|| self.parsers.iter().find(|p| p.kind() == ParserKind::PlainText))
            .ok_or_else(|| CascadeError::ParseFailed { path: path.to_path_buf(), detail: "no parser registered for this file type".into() })?;
        parser.parse(path).await
    }

    /// Add a custom parser.  Custom parsers take precedence over built-in ones.
    pub fn register(&mut self, parser: Arc<dyn Parser>) {
        self.parsers.insert(0, parser);
    }
}
